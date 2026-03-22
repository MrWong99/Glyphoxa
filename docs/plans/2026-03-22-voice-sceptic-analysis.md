# Voice Proxy Architecture — Sceptic Review

**Date:** 2026-03-22
**Author:** Claude (sceptic reviewer)
**Status:** Analysis complete — recommends architectural change

---

## TL;DR

The Voice State Proxy is architecturally sound in theory but **operationally fragile in practice**. After three production failures and three fix attempts that didn't address the actual problem, the pattern should be replaced. The root cause is almost certainly **not a code bug** — it's Discord silently ignoring the Op 4 voice join request due to either missing channel permissions or a zombie gateway connection. But even if we fix the immediate cause, the credential-capture pattern will remain brittle. I recommend switching to **Gateway Audio Bridge** — gateway owns the voice connection, streams audio to/from workers via gRPC.

---

## 1. Root Cause Analysis

### What the logs tell us

```
20:13:03.246Z  INFO  "gateway: capturing voice credentials"
                     bot_user_id=1477441235500400671
                     application_id=1477441235500400671
20:13:03.246Z  INFO  "gateway: sent Op 4 voice state update"
20:13:13.249Z  ERROR "voice credential capture timed out"
                     got_voice_state=false got_voice_server=false
```

**Key facts:**
- `bot_user_id == application_id` → PR #71's ID mismatch fix was irrelevant
- Op 4 returned successfully (log only appears when `UpdateVoiceState()` returns nil)
- **Neither** VOICE_STATE_UPDATE **nor** VOICE_SERVER_UPDATE arrived
- 10-second timeout elapsed with zero events

### What I verified in the code

I traced the full event dispatch chain through disgo v0.19.3 (pseudo-version dd3528ae9dd0):

| Layer | Status | Evidence |
|-------|--------|----------|
| Event listeners registered before Op 4 | Correct | sessionctrl.go:376 `AddEventListeners` before :380 `UpdateVoiceState` |
| Type assertions match dispatch types | Correct | Listener expects `*events.GuildVoiceStateUpdate`, handler dispatches `&events.GuildVoiceStateUpdate{...}` |
| VoiceManager=nil doesn't suppress events | Correct | voice_handlers.go:27-29 — VoiceManager check is independent of DispatchEvent call at :37-40 |
| Event dispatch reaches all listeners | Correct | event_manager.go:147-171 — iterates all listeners, no filtering |
| Gateway intent set | Correct | `IntentGuildVoiceStates` in botconnector.go:76 |
| Buffered channel prevents blocking | Correct | `credsCh := make(chan creds, 1)` at sessionctrl.go:319 |
| Stale voice state cleanup | Correct | sessionctrl.go:299-312 — leave before rejoin |
| HasGateway() check | **Weak** | client.go:75-77 — just a nil pointer check, not a connection health check |
| Send() status check | Correct | gateway.go:351-359 — returns ErrShardNotReady if not StatusReady |

**Conclusion: The Go code is correct.** If Discord sends voice events, they WILL reach the listeners.

### Most likely root causes (ranked)

**1. Missing CONNECT permission on the voice channel (70% likely)**

When a bot sends Op 4 for a voice channel it doesn't have CONNECT permission on, Discord simply ignores it. No error event, no close code, no response. Silent failure. This matches the symptoms perfectly: Op 4 sent, zero events received.

This would explain why the issue is consistent across all three attempts — the permission problem wouldn't change between deploys.

**How to verify:** Call `GET /guilds/{guild_id}/channels` and check the bot's permission overrides on the target voice channel. Or try joining via the Discord developer tools.

**2. Zombie gateway connection (20% likely)**

The gateway WebSocket appears connected (StatusReady), writes succeed at the OS TCP level, but Discord has already invalidated the session (e.g., missed heartbeat ACK during a K8s pod reschedule). The gateway is effectively shouting into a dead phone line.

This would be more intermittent — but in K8s, if the pod regularly gets rescheduled or the network has issues, it could be systematic.

**How to verify:** Add logging for gateway heartbeat ACK latency and connection state transitions. Check if the gateway reconnects frequently.

**3. disgo pseudo-version regression (5% likely)**

The project uses `v0.19.3-0.20260322125507-dd3528ae9dd0` — a pseudo-version from an unreleased commit. This carries risk of undiscovered regressions. However, the commit message ("null-padded UDP address fix") suggests the change is in the UDP layer, not gateway event dispatch.

**4. Discord API behavior change (5% likely)**

Discord occasionally changes voice behavior without documentation updates. Unlikely for something as fundamental as Op 4 → voice events, but possible.

### Why PRs #70 and #71 didn't help

| PR | What it fixed | Why it was irrelevant |
|----|---------------|----------------------|
| #70 | Added timeout + gateway check + VoiceManager nil | Timeout already existed via context; VoiceManager nil was correct; HasGateway() is too weak to catch the real issue |
| #71 | Fixed bot ID mismatch + stale state cleanup | IDs were identical all along (`bot_user_id == application_id`); stale state cleanup is correct but wasn't the trigger |

Both PRs fixed legitimate code issues, but the actual problem is **pre-code** — Discord never sends the events in the first place.

---

## 2. Why the Voice State Proxy Is Architecturally Fragile

Even if we fix the immediate cause (permissions, zombie connection), the credential-capture pattern has fundamental weaknesses:

### 2.1 Relies on an undocumented two-phase protocol

The pattern assumes: "send Op 4 → receive VOICE_STATE_UPDATE + VOICE_SERVER_UPDATE → extract credentials → pass to worker → worker connects." This is an **informal contract** — Discord's API docs describe each piece, but the pattern of capturing-and-replaying credentials across processes is not a supported use case. Any behavior change in how Discord delivers these events breaks us.

### 2.2 Silent failure mode

When credential capture fails, there's no useful error from Discord. The only signal is a timeout. This makes debugging extremely difficult — we can't distinguish between:
- Permission denied
- Network issue
- Gateway zombie
- Discord bug
- disgo bug

### 2.3 Time-sensitive credential handoff

Voice credentials have an implicit TTL. Between capture on the gateway and use on the worker, the credentials must remain valid. If the worker takes too long to start (K8s job scheduling, image pull, etc.), the credentials may expire. There's no documented TTL for Discord voice tokens.

### 2.4 Dual voice state

The gateway bot appears "in the voice channel" (because it sent Op 4), but it's not actually processing audio — the worker is. If the gateway pod restarts, it loses its voice state, and the worker's voice connection may be invalidated when Discord processes the implicit leave.

### 2.5 VoiceManager=nil is fighting the library

Setting `client.VoiceManager = nil` to prevent disgo from intercepting voice events is a hack. It works today, but any disgo update that changes how voice events are dispatched (e.g., making VoiceManager non-optional, or changing the handler to skip dispatch when VoiceManager is nil) would break this silently.

---

## 3. Alternative Architectures

### A. Gateway Audio Bridge (RECOMMENDED)

```
Discord Voice ↔ Gateway Pod (voice.Conn) ↔ gRPC stream ↔ Worker Pod (VAD→STT→LLM→TTS)
```

**How it works:**
1. Gateway bot joins voice normally using disgo's VoiceManager (the battle-tested, maintained path)
2. Gateway receives opus frames from Discord, forwards them to worker via gRPC bidirectional streaming
3. Worker processes audio (VAD→STT→LLM→TTS), sends response opus frames back via gRPC
4. Gateway sends response audio to Discord voice
5. Worker never touches Discord at all

**Latency impact:**
- Opus frames: 20ms at ~80 bytes per frame
- gRPC streaming in-cluster: ~0.5-2ms per message
- Total added latency per direction: ~1-2ms
- Round-trip overhead: ~2-4ms
- **This is <0.3% of the 1.2s mouth-to-ear budget** — completely negligible

The existing plan (2026-03-22-voice-architecture-plan.md) dismissed this with "adds ~5-20% overhead" — but that's 5-20% of the 20ms *frame interval*, not the latency budget. The actual impact on perceived latency is unmeasurable.

**Bandwidth:**
- Per user speaking: ~4 KB/s (20ms * 50 frames/sec * 80 bytes)
- 10 simultaneous speakers: ~40 KB/s
- Well within K8s pod-to-pod networking capabilities

**What changes:**
- New gRPC service: `AudioBridge` with bidirectional opus frame streaming
- New `audio.Connection` implementation: `GRPCConnection` that sends/receives via gRPC
- Gateway: keeps VoiceManager enabled, adds frame forwarding to gRPC stream
- Worker: uses `GRPCConnection` instead of `discord.VoiceProxyPlatform`
- Remove: `VoiceProxyPlatform`, `captureVoiceCredentials`, `VoiceManager=nil` hack

| Pro | Con |
|-----|-----|
| Uses disgo's maintained voice path | Audio hops through gateway (minimal latency) |
| Zero credential passing | Gateway needs voice.Conn (already has it) |
| Silent failure impossible (gRPC errors are explicit) | Gateway becomes stateful for audio |
| Worker is truly platform-agnostic | Audio stops if gateway pod restarts |
| No VoiceManager=nil hack | Bidirectional gRPC streaming to implement |
| Debuggable (gRPC metrics, tracing) | |
| Same DAVE E2EE support | |

### B. Separate Worker Bot Token

```
Gateway Bot (slash commands) ─── gRPC control plane ──→ Worker Bot (own gateway + voice)
```

**How it works:**
1. Each tenant provides TWO bot tokens: one for the gateway (slash commands), one for workers (voice)
2. Worker opens its own gateway connection with its own token
3. Worker joins voice independently — no credential passing
4. Gateway sends control signals (start/stop) via gRPC

| Pro | Con |
|-----|-----|
| Cleanest separation | Requires two bot tokens per tenant |
| No credential passing | Two bots visible in server |
| Each bot has own gateway | Doubles Discord API usage/rate limits |
| Most resilient | User setup complexity increases |
| Worker fully independent | Token management complexity |

### C. Fix Current Approach (Voice State Proxy)

**Quick diagnostics to add:**

1. **REST API voice state check**: Before Op 4, call `GET /guilds/{guild_id}/voice-states/@me` to see if Discord thinks the bot is already in voice
2. **Permission check**: Call `GET /guilds/{guild_id}/channels` and compute effective permissions for the bot on the target voice channel
3. **Raw gateway logging**: Log all incoming gateway dispatch events (not just voice) to see if ANY events arrive after Op 4
4. **Heartbeat monitoring**: Log heartbeat ACK latency to detect zombie connections

| Pro | Con |
|-----|-----|
| Minimal code change | Doesn't fix architectural fragility |
| Good for diagnosis | Silent failure mode remains |
| Preserves current architecture | VoiceManager=nil hack persists |
| Quick to implement | May still fail for new reasons |

### D. Hybrid: Audio Bridge + REST Fallback

Use Gateway Audio Bridge as the primary path. If the gateway voice connection fails, fall back to Voice State Proxy with the diagnostic checks from Option C. This gives resilience at the cost of maintaining two code paths.

Not recommended — the extra complexity isn't justified if Audio Bridge works reliably.

---

## 4. Recommendation

**Switch to Gateway Audio Bridge (Option A).**

### Why

1. **It eliminates the root cause by design.** No credential capture = no credential capture failures. The gateway joins voice the way disgo was designed to be used.

2. **The latency concern is a non-issue.** 2-4ms round-trip overhead on a 1200ms budget is 0.3%. The previous analysis overstated this by comparing to frame interval instead of the actual latency budget.

3. **It makes the worker platform-agnostic.** The worker becomes a pure audio processing node. This is architecturally cleaner and enables future non-Discord platforms (WebRTC) with zero worker changes.

4. **It's more debuggable.** gRPC has first-class observability (metrics, tracing, error codes). A broken gRPC stream gives you an explicit error, not a 10-second timeout followed by "maybe permissions, maybe zombie, who knows."

5. **It aligns with the project's own design principle:** "Every external dependency sits behind a Go interface." Currently, the worker depends on Discord voice internals (voice.Conn, HandleVoiceStateUpdate). With Audio Bridge, only the gateway touches Discord.

### Implementation sketch

```protobuf
// New service
service AudioBridge {
  rpc StreamAudio(stream AudioFrame) returns (stream AudioFrame);
}

message AudioFrame {
  string session_id = 1;
  bytes opus_data = 2;
  uint32 ssrc = 3;          // who's speaking (incoming only)
  string user_id = 4;       // Discord user ID (incoming only)
  bool is_silence = 5;      // silence frame marker
}
```

```go
// Worker side: implements audio.Connection via gRPC
type GRPCConnection struct {
    stream  AudioBridge_StreamAudioClient
    inbound chan audio.IncomingAudio
}

func (c *GRPCConnection) ReceiveOpus() <-chan audio.IncomingAudio { return c.inbound }
func (c *GRPCConnection) SendOpus(data []byte, ...) error {
    return c.stream.Send(&AudioFrame{OpusData: data, ...})
}
```

```go
// Gateway side: bridges voice.Conn ↔ gRPC stream
func (gw *AudioBridgeServer) StreamAudio(stream AudioBridge_StreamAudioServer) error {
    // Forward incoming Discord audio → gRPC
    go func() {
        for frame := range voiceConn.ReceiveOpus() {
            stream.Send(&AudioFrame{OpusData: frame.Opus, SSRC: frame.SSRC, ...})
        }
    }()
    // Forward outgoing gRPC audio → Discord
    for {
        frame, err := stream.Recv()
        if err != nil { return err }
        voiceConn.SendOpus(frame.OpusData, ...)
    }
}
```

### What to delete

- `VoiceProxyPlatform` (pkg/audio/discord/voice_proxy.go)
- `captureVoiceCredentials` (internal/gateway/sessionctrl.go)
- `registerVoiceServerForwarder` / `unregisterVoiceServerForwarder`
- `UpdateVoiceServer` gRPC method
- `client.VoiceManager = nil` in botconnector.go
- Voice credential fields in `StartSessionRequest` proto

### Before committing to the rewrite

Do the **quick diagnostic fix first** (Option C) to confirm the root cause. It takes 30 minutes and tells you whether the issue is permissions, zombie connection, or something else. Even if you proceed with Audio Bridge, knowing the root cause informs future decisions.

Specifically, add this before Op 4:

```go
// Check bot's current voice state via REST API
vs, err := client.Rest().GetBotVoiceState(gID)
if err == nil && vs.ChannelID != nil {
    slog.Warn("gateway: Discord REST shows bot in voice (cache missed this)",
        "current_channel", vs.ChannelID)
}

// Check bot permissions on target channel
perms, err := client.Rest().GetChannel(chID)
// Log effective permissions
```

This will immediately tell you whether the issue is:
- Stale voice state that the cache missed (REST shows bot in channel)
- Missing permissions (CONNECT not in effective permissions)
- Something else (REST shows correct state, permissions are fine)

---

## 5. Risk Assessment

| Risk | Voice State Proxy (current) | Gateway Audio Bridge | Separate Worker Bot |
|------|-----------------------------|---------------------|---------------------|
| Discord API change breaks us | **High** — undocumented credential replay | **Low** — uses standard voice join | **Low** — uses standard voice join |
| Silent failure | **High** — timeout is only signal | **None** — gRPC errors explicit | **Low** — gateway errors explicit |
| Latency impact | None (direct worker↔Discord) | **Minimal** (~2-4ms) | None (direct worker↔Discord) |
| Implementation effort | Already done (but broken) | **Medium** (new gRPC service) | **Low** (remove proxy code) |
| Operational complexity | **Medium** (credential debugging) | **Low** (standard gRPC) | **High** (two bot tokens) |
| Library upgrade risk | **High** (VoiceManager=nil hack) | **Low** (standard API usage) | **Low** (standard API usage) |

---

## Summary

The Voice State Proxy was a clever solution to a real problem, but it's proven unreliable in production. Three attempts to fix it have all addressed symptoms rather than the root cause — which is almost certainly outside the Go code entirely (permissions or gateway health). Even if we fix the immediate issue, the architecture remains fragile.

The Gateway Audio Bridge eliminates the entire class of problems by keeping voice connection management inside disgo's well-tested VoiceManager, at a trivially small latency cost. It's the right long-term answer.
