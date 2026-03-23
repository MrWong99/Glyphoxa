# Voice Architecture for Distributed Mode

**Date:** 2026-03-22
**Status:** Proposal
**Author:** Claude (deep investigation)

## Problem Statement

In distributed mode, Glyphoxa splits into a gateway (slash commands, session orchestration) and a worker (voice pipeline). Both need the same bot token to interact with Discord, but **Discord enforces a single gateway WebSocket per bot token**. Voice connections require gateway events (`VOICE_STATE_UPDATE`, `VOICE_SERVER_UPDATE`) that only flow over the gateway WebSocket.

### Failed Approaches

| Approach | Why it fails |
|----------|-------------|
| Both connect | Second IDENTIFY invalidates first (close code 4005) |
| Sharding | Guild maps to one shard; both gateway + worker need the same guild |
| Gateway handoff (current) | Gateway suspends, worker takes over — `conn.Open()` hangs 60s |

---

## Root Cause Analysis

### How disgo Voice Connections Work

The voice connection flow in disgo (`voice/conn.go`):

```
conn.Open(ctx, channelID, selfMute, selfDeaf)
  │
  ├─ voiceStateUpdateFunc(ctx, guildID, channelID, ...)
  │    └─ Sends Opcode 4 (VoiceStateUpdate) via bot gateway
  │
  └─ Blocks on openedChan until voice is ready
       └─ Signaled when SessionDescription is received (final handshake step)
```

For `openedChan` to fire, two bot gateway dispatch events must arrive:

1. **VOICE_STATE_UPDATE** → `conn.HandleVoiceStateUpdate()` stores `SessionID`
2. **VOICE_SERVER_UPDATE** → `conn.HandleVoiceServerUpdate()` opens voice WebSocket with `State{Token, Endpoint, SessionID}`

The voice WebSocket then performs its own handshake: Identify → Ready → UDP → SelectProtocol → SessionDescription → `openedChan` signaled.

### Why the Handoff Fails

The current handoff (`gateway_bot.go:SuspendGateway` → worker creates `VoiceOnlyPlatform`) has this sequence:

```
1. Gateway calls SuspendGateway()
   └─ client.Gateway.Close(ctx) with CloseNormalClosure
   └─ Clears SessionID, ResumeURL, LastSequenceReceived

2. Worker creates VoiceOnlyPlatform
   └─ disgo.New(token, WithDefaultGateway())
   └─ client.OpenGateway(ctx) → IDENTIFY → Ready
   └─ Ready returns immediately (guilds listed as "unavailable")

3. Worker calls platform.Connect(ctx, channelID)
   └─ voiceMgr.CreateConn(guildID)
   └─ conn.Open(ctx, channelID, false, false)
   └─ Sends Opcode 4 → waits on openedChan → HANGS
```

**The most likely cause:** The worker sends Opcode 4 before Discord has fully hydrated the new session. After `IDENTIFY`, Discord sends `Ready` with guilds as `UnavailableGuild`. Guild hydration happens via subsequent `GUILD_CREATE` dispatch events, which arrive **asynchronously after Ready**. `OpenGateway()` returns as soon as `Ready` is received (see `gateway.go:649-656`), before any `GUILD_CREATE` events are processed.

Discord likely silently drops or queues Opcode 4 (Voice State Update) for guilds that haven't been hydrated via `GUILD_CREATE` yet. The voice events (`VOICE_STATE_UPDATE`, `VOICE_SERVER_UPDATE`) are never dispatched, so `openedChan` is never signaled, and `conn.Open()` blocks until the caller's context times out (60s from gRPC deadline).

**Additional contributing factors:**
- No wait-for-guild-ready logic in `VoiceOnlyPlatform` — it calls `Connect()` immediately after `OpenGateway()` returns
- The `Ready` → `SetSelfUser` dispatch in disgo uses an unbuffered channel (`readyChan`), creating a window where `OpenGateway` returns before event handlers have populated caches — though this is unlikely to be the direct cause since the network round-trip for voice events is much longer than the event dispatch delay
- Even if the timing issue were fixed, the fundamental problem remains: the gateway pod cannot handle slash commands while the worker holds the gateway connection

---

## Solution Options (Ranked)

### Option B: Voice State Proxy (RECOMMENDED)

**Architecture:** Gateway bot owns the gateway connection permanently. It joins voice on behalf of the worker, captures the voice credentials, and sends them to the worker via gRPC. The worker connects directly to the Discord voice server (WebSocket + UDP) without needing a bot gateway connection at all.

```
┌─────────────────────────────────────────────┐
│  Gateway Pod (permanent gateway connection)  │
│                                              │
│  1. Receives /start slash command            │
│  2. Sends Opcode 4 (join voice channel)      │
│  3. Receives VOICE_STATE_UPDATE → sessionID  │
│  4. Receives VOICE_SERVER_UPDATE → token,    │
│     endpoint                                 │
│  5. Sends credentials to worker via gRPC     │
│  6. Keeps gateway open for slash commands    │
└──────────────────┬──────────────────────────┘
                   │ gRPC (voice credentials)
                   ▼
┌─────────────────────────────────────────────┐
│  Worker Pod (no bot gateway needed)          │
│                                              │
│  1. Receives voice credentials via gRPC      │
│  2. Creates voice.Conn directly (NewConn)    │
│  3. Calls HandleVoiceStateUpdate() +         │
│     HandleVoiceServerUpdate() manually       │
│  4. voice.Conn connects to voice WebSocket   │
│     (wss://endpoint) + UDP directly          │
│  5. Runs audio pipeline (VAD→STT→LLM→TTS)   │
│  6. Audio flows directly: Worker ↔ Discord   │
└─────────────────────────────────────────────┘
```

**Why this works:** The voice WebSocket (`wss://{endpoint}?v=8`) and UDP connection are **completely independent** of the bot gateway. The bot gateway is only needed to:
1. Send Opcode 4 (request to join voice) — gateway pod does this
2. Receive `VOICE_STATE_UPDATE` / `VOICE_SERVER_UPDATE` — gateway pod captures these

After capturing the credentials, the worker uses disgo's exported `voice.NewConn()` and `voice.Gateway.Open(ctx, State{...})` to connect directly to the voice server. No second bot gateway connection needed.

**Key disgo APIs that make this possible:**
- `voice.NewConn(guildID, userID, voiceStateUpdateFunc, removeFunc, opts...)` — exported, no dependency on `bot.Client`
- `conn.HandleVoiceStateUpdate(event)` — public method, accepts event data directly
- `conn.HandleVoiceServerUpdate(event)` — public method, triggers voice gateway connection
- `voice.WithConnDaveSessionCreateFunc(golibdave.NewSession)` — DAVE E2EE works without bot gateway

#### Code Changes

**1. Extend `StartSessionRequest` with voice credentials**

```go
// internal/gateway/contract.go

type StartSessionRequest struct {
    // ... existing fields ...

    // Voice credentials (populated by gateway before dispatch).
    // When set, the worker connects directly to the voice server
    // without opening a bot gateway connection.
    VoiceSessionID string
    VoiceToken     string
    VoiceEndpoint  string
    BotUserID      string  // bot's user snowflake (for voice.NewConn)
}
```

**2. Gateway captures voice credentials before dispatching to worker**

```go
// internal/gateway/sessionctrl.go — new method

func (gc *GatewaySessionController) captureVoiceCredentials(
    ctx context.Context, guildID, channelID string,
) (sessionID, token, endpoint string, err error) {
    gID, _ := snowflake.Parse(guildID)
    chID, _ := snowflake.Parse(channelID)

    type voiceCreds struct {
        sessionID string
        token     string
        endpoint  string
    }
    credsCh := make(chan voiceCreds, 1)

    var (
        gotState  bool
        gotServer bool
        mu        sync.Mutex
        creds     voiceCreds
    )

    // Register temporary event listeners for voice events.
    stateListener := bot.NewListenerFunc(func(e *events.GuildVoiceStateUpdate) {
        if e.GuildID != gID || e.UserID != gc.gwBot.Client().ID() {
            return
        }
        mu.Lock()
        defer mu.Unlock()
        creds.sessionID = e.SessionID
        gotState = true
        if gotServer {
            credsCh <- creds
        }
    })
    serverListener := bot.NewListenerFunc(func(e *events.VoiceServerUpdate) {
        if e.GuildID != gID || e.Endpoint == nil {
            return
        }
        mu.Lock()
        defer mu.Unlock()
        creds.token = e.Token
        creds.endpoint = *e.Endpoint
        gotServer = true
        if gotState {
            credsCh <- creds
        }
    })

    gc.gwBot.Client().AddEventListeners(stateListener, serverListener)
    defer gc.gwBot.Client().RemoveEventListeners(stateListener, serverListener)

    // Send Opcode 4 to join voice.
    if err := gc.gwBot.Client().UpdateVoiceState(ctx, gID, &chID, false, false); err != nil {
        return "", "", "", fmt.Errorf("send voice state update: %w", err)
    }

    select {
    case c := <-credsCh:
        return c.sessionID, c.token, c.endpoint, nil
    case <-ctx.Done():
        return "", "", "", ctx.Err()
    }
}
```

**3. Update `GatewaySessionController.Start()` — join voice before dispatch**

```go
// internal/gateway/sessionctrl.go — modified Start()

func (gc *GatewaySessionController) Start(ctx context.Context, req SessionStartRequest) error {
    // ... existing validation and session creation ...

    if gc.dispatcher != nil {
        // Capture voice credentials BEFORE dispatching to worker.
        // Gateway stays connected — no suspend/resume needed.
        voiceCtx, voiceCancel := context.WithTimeout(ctx, 10*time.Second)
        defer voiceCancel()

        vsID, vToken, vEndpoint, err := gc.captureVoiceCredentials(
            voiceCtx, req.GuildID, req.ChannelID)
        if err != nil {
            _ = gc.orch.Transition(ctx, sessionID, SessionEnded, err.Error())
            return fmt.Errorf("gateway: capture voice credentials: %w", err)
        }

        startReq := StartSessionRequest{
            // ... existing fields ...
            VoiceSessionID: vsID,
            VoiceToken:     vToken,
            VoiceEndpoint:  vEndpoint,
            BotUserID:      gc.gwBot.Client().ID().String(),
        }

        // Dispatch to worker (no SuspendGateway call needed!)
        // ...
    }
}
```

**4. New worker voice platform — `VoiceProxyPlatform`**

```go
// pkg/audio/discord/voice_proxy.go

// VoiceProxyPlatform connects to a Discord voice server using
// pre-captured credentials (sessionID, token, endpoint) instead of
// opening its own bot gateway connection. This is used in distributed
// mode where the gateway pod owns the gateway and passes voice
// credentials to the worker via gRPC.
type VoiceProxyPlatform struct {
    conn      voice.Conn
    guildID   snowflake.ID
    readyCh   chan struct{}
    closeOnce sync.Once
}

func NewVoiceProxyPlatform(
    guildIDStr, botUserIDStr string,
    opts ...voice.ConnConfigOpt,
) (*VoiceProxyPlatform, error) {
    guildID, err := snowflake.Parse(guildIDStr)
    if err != nil { return nil, fmt.Errorf("parse guild ID: %w", err) }
    botUserID, err := snowflake.Parse(botUserIDStr)
    if err != nil { return nil, fmt.Errorf("parse bot user ID: %w", err) }

    vp := &VoiceProxyPlatform{
        guildID: guildID,
        readyCh: make(chan struct{}, 1),
    }

    // No-op: the gateway pod handles Opcode 4.
    noopStateUpdate := func(ctx context.Context, guildID snowflake.ID,
        channelID *snowflake.ID, selfMute, selfDeaf bool) error {
        return nil
    }

    allOpts := append([]voice.ConnConfigOpt{
        voice.WithConnEventHandlerFunc(func(_ voice.Gateway, _ voice.Opcode,
            _ int, data voice.GatewayMessageData) {
            if _, ok := data.(voice.GatewayMessageDataSessionDescription); ok {
                select {
                case vp.readyCh <- struct{}{}:
                default:
                }
            }
        }),
    }, opts...)

    vp.conn = voice.NewConn(guildID, botUserID, noopStateUpdate, func() {}, allOpts...)
    return vp, nil
}

// Connect feeds the pre-captured voice credentials into the Conn, which
// triggers the voice WebSocket + UDP handshake internally.
func (vp *VoiceProxyPlatform) Connect(
    ctx context.Context, channelIDStr, voiceSessionID, voiceToken, voiceEndpoint string,
) (audio.Connection, error) {
    channelID, err := snowflake.Parse(channelIDStr)
    if err != nil { return nil, fmt.Errorf("parse channel ID: %w", err) }

    // Feed the credentials that the gateway captured.
    vp.conn.HandleVoiceStateUpdate(botgateway.EventVoiceStateUpdate{
        VoiceState: discord.VoiceState{
            GuildID:   vp.guildID,
            ChannelID: &channelID,
            UserID:    vp.conn.GuildID(), // overridden in NewConn
            SessionID: voiceSessionID,
        },
    })
    vp.conn.HandleVoiceServerUpdate(botgateway.EventVoiceServerUpdate{
        Token:    voiceToken,
        GuildID:  vp.guildID,
        Endpoint: &voiceEndpoint,
    })

    select {
    case <-vp.readyCh:
        return newConnection(vp.conn, vp.guildID), nil
    case <-ctx.Done():
        vp.conn.Close(ctx)
        return nil, fmt.Errorf("voice proxy connect: %w", ctx.Err())
    }
}
```

**5. Update `workerFactory.CreateRuntime` — use proxy platform when credentials present**

```go
// cmd/glyphoxa/worker_factory.go — modified CreateRuntime (step 3)

if req.VoiceSessionID != "" && req.VoiceToken != "" && req.VoiceEndpoint != "" {
    // Distributed mode: use pre-captured voice credentials.
    proxyPlatform, err := discord.NewVoiceProxyPlatform(
        req.GuildID, req.BotUserID,
        voice.WithConnDaveSessionCreateFunc(golibdave.NewSession),
    )
    if err != nil { /* cleanup and return */ }

    conn, err = proxyPlatform.Connect(sessionCtx,
        req.ChannelID, req.VoiceSessionID, req.VoiceToken, req.VoiceEndpoint)
    if err != nil { /* cleanup and return */ }

    platform = proxyPlatform // for teardown
} else {
    // Full mode or legacy: open own gateway (existing code).
    voicePlatform, err := discord.NewVoiceOnlyPlatform(...)
    // ...
}
```

**6. Handle voice server updates during session (reconnection)**

```go
// New gRPC method: gateway → worker

// internal/gateway/contract.go
type WorkerClient interface {
    // ... existing methods ...
    UpdateVoiceServer(ctx context.Context, sessionID, token, endpoint string) error
}

// The gateway registers an ongoing listener for VOICE_SERVER_UPDATE.
// If the voice server changes mid-session (Discord migration), it
// forwards the new credentials to the worker.
```

**7. Session teardown — gateway leaves voice**

```go
// internal/gateway/sessionctrl.go — modified Stop()

func (gc *GatewaySessionController) Stop(ctx context.Context, sessionID string) error {
    // ... existing logic ...

    // Leave voice channel (send Opcode 4 with channelID=nil).
    if gc.gwBot != nil {
        guildID := gc.guildIDForSession(sessionID)
        if guildID != 0 {
            _ = gc.gwBot.Client().UpdateVoiceState(ctx, guildID, nil, false, false)
        }
    }

    // No ResumeGateway needed — gateway never suspended!
    return nil
}
```

**8. gRPC proto updates**

```protobuf
message StartSessionRequest {
    // ... existing fields ...
    string voice_session_id = 9;
    string voice_token = 10;
    string voice_endpoint = 11;
    string bot_user_id = 12;
}

// New RPC for mid-session voice server updates
service WorkerService {
    // ... existing RPCs ...
    rpc UpdateVoiceServer(UpdateVoiceServerRequest) returns (google.protobuf.Empty);
}

message UpdateVoiceServerRequest {
    string session_id = 1;
    string token = 2;
    string endpoint = 3;
}
```

#### Tradeoffs

| Pro | Con |
|-----|-----|
| Gateway stays connected — slash commands always work | Voice credentials pass through gRPC (sensitive, but internal) |
| No suspend/resume dance | Mid-session voice server changes need forwarding |
| Worker needs no bot gateway (less resource usage) | Slightly more complex session start (capture + forward) |
| DAVE E2EE works unchanged on worker | Must handle gateway leaving voice on session end |
| Clean separation: gateway = Discord, worker = audio | New `VoiceProxyPlatform` to maintain |
| No timing/race issues | Bot shows as "in voice" on gateway pod (cosmetic) |

---

### Option A: Gateway-Owned Voice (Audio Proxy)

**Architecture:** Gateway bot joins voice AND handles audio I/O. Audio data (opus frames) is streamed between gateway and worker via bidirectional gRPC streaming.

```
Discord ↔ Gateway (voice + audio I/O) ↔ gRPC stream ↔ Worker (VAD→STT→LLM→TTS)
```

**Pros:**
- Simplest conceptual model — worker never touches Discord
- No voice credential passing
- Gateway handles all Discord interactions

**Cons:**
- **Latency penalty:** Every audio frame adds gRPC round-trip (~2-10ms). With 20ms opus frames, this adds ~5-20% overhead. May push past the 1.2s mouth-to-ear target.
- **Gateway becomes bottleneck:** All audio flows through it, increasing CPU/bandwidth
- **Major refactor:** Need bidirectional gRPC audio streaming, opus frame serialization, mixer changes
- **Gateway resource usage:** Gateway pods need audio processing capabilities

**Verdict:** Feasible but suboptimal. The latency penalty and gateway bottleneck make this worse than Option B for a voice-centric application.

---

### Option D: Fix Current Approach

**Potential fix:** Wait for `GUILD_CREATE` before joining voice.

```go
// In NewVoiceOnlyPlatform, after OpenGateway:

readyCh := make(chan struct{}, 1)
client.AddEventListeners(bot.NewListenerFunc(func(e *events.GuildReady) {
    if e.Guild.ID == gID {
        select {
        case readyCh <- struct{}{}:
        default:
        }
    }
}))

if err := client.OpenGateway(ctx); err != nil { ... }

select {
case <-readyCh:
    // Guild is ready, safe to join voice
case <-ctx.Done():
    return nil, ctx.Err()
}
```

**Pros:**
- Minimal code change — just add a wait
- Tests the root cause theory directly

**Cons:**
- **Doesn't solve the fundamental problem:** Gateway can't handle slash commands while worker holds the gateway
- **Fragile:** Depends on Discord's guild hydration timing
- **Still requires suspend/resume dance**
- **Single point of failure:** If worker crashes, gateway must resume quickly or bot goes offline

**Verdict:** Worth trying as a quick diagnostic test to confirm the root cause theory, but not a production solution. Even if it works, the architectural problems remain.

---

### Option C: Per-Tenant Bot Tokens (Already Implemented)

Each tenant already provides their own bot token via `WithBotToken(token)`. The problem isn't shared tokens — it's that a single tenant's bot token can only have one gateway connection.

**Not a solution** — this is already the case.

---

## Recommendation

**Option B (Voice State Proxy)** is the clear winner:

1. **Correctness:** Eliminates the single-gateway problem by design
2. **Performance:** Direct worker→Discord voice connection (no audio proxy overhead)
3. **Reliability:** Gateway never suspends — slash commands always work
4. **Simplicity:** Uses disgo's existing public APIs (`voice.NewConn`, `HandleVoiceStateUpdate`, `HandleVoiceServerUpdate`)
5. **Compatibility:** DAVE E2EE works unchanged; existing audio pipeline unaffected

### Implementation Order

1. Add `VoiceProxyPlatform` in `pkg/audio/discord/voice_proxy.go`
2. Add `captureVoiceCredentials` to `GatewaySessionController`
3. Extend `StartSessionRequest` with voice credential fields
4. Update gRPC proto + codegen
5. Update `workerFactory.CreateRuntime` to use proxy when credentials present
6. Add `UpdateVoiceServer` gRPC method for mid-session reconnection
7. Update `Stop()` to leave voice channel via gateway
8. Remove `SuspendGateway`/`ResumeGateway` (dead code after migration)
9. Remove `VoiceOnlyPlatform` (only needed for legacy full mode fallback, or keep for `--mode=full`)

### Testing Strategy

- Unit test `VoiceProxyPlatform` with mock `voice.Conn` (verify `HandleVoiceStateUpdate`/`HandleVoiceServerUpdate` are called correctly)
- Unit test `captureVoiceCredentials` with mock event dispatch
- Integration test: gateway captures credentials → worker connects to voice → audio flows
- Manual test with Discord: `/start` → verify bot joins voice, NPCs respond, `/stop` → bot leaves
