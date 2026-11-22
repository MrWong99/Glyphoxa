# Glyphoxa architecture

The current-system overview. Every subsystem section names the ADR(s) that
govern it, under [docs/adr/](adr/). Domain vocabulary — **Mode**, **Operator**,
**GM**, **Bot**, **Guild**, **Voice Session**, **Butler**, **Character NPC**,
**Provider Config**, **BYOK**, **Slash Command**, **Knowledge Graph**,
**Transcript Line**, **Address Detection** — is defined once in
[CONTEXT.md](../CONTEXT.md) and used here exactly. The ADRs are indexed in
[docs/README.md](README.md#architecture-decision-records); the self-host setup
runbook is [docs/configuration.md](configuration.md).

> **Audit status (2026-11-20, against v0.16.0).** §1, §2.2–§2.6, §3, §5, the
> CI paragraph in §8 and the two closing tables were re-checked against the
> tree. §4.1–§4.4 still describe the single-Operator MVP web tier (ADR-0039);
> self-signup (ADR-0055), Player Invitations (ADR-0056) and the per-Tenant
> presence registry (§5) have since widened it — read those ADRs alongside §4.

**How to read this doc.** ADRs record *decisions*; the tree records what
*shipped*. Where the two disagree, this doc follows the tree and says so in the
margin. Decisions that are design-of-record but not yet built are collected in
[Design-of-record, not shipped](#design-of-record-not-shipped) — nothing in the
shipped sections implies code that does not exist.

---

## 1. Modes and process topology

> ADR-0005 ([single binary with Modes; no audio across process
> boundaries](adr/0005-single-binary-modes-no-audio-rpc.md)) ·
> ADR-0034 ([deployment artifacts](adr/0034-deployment-artifacts.md)) ·
> ADR-0039 ([single-operator web tier](adr/0039-mvp-ui-backend-single-operator-web-tier.md))

One binary, `cmd/glyphoxa`, runs in one of **three Modes**, selected by `-mode`.
**The default is `-mode all`** — the self-host target (ADR-0005/ADR-0034); it
auto-applies migrations at startup, so a bare `glyphoxa` reaches a serving,
migrated instance. An explicit `-mode voice`/`web` still overrides it.

| Mode | What the process is | What it runs |
|------|---------------------|--------------|
| `voice` | a **Voice Instance** | with a database and no `-guild`/`-channel`: a **claim-plane worker** in a shared pool (ADR-0057) that claims Voice Session intents, drives them, and serves the Slash Commands while it holds the elected `presence_owner` row. With `-guild`/`-channel` (or `-hardcoded`): the legacy standalone node for one static channel. No web API |
| `web` | a **Web Instance** | the Connect RPC API + the embedded SPA. Opens no Discord gateway; starts and stops Voice Sessions by writing claim-plane intents a `voice` worker picks up |
| `all` | both, in one process | Web Instance that also owns the per-Tenant Discord presence (Slash Commands) and drives the voice loop in-process |

Two deployment shapes follow. **`all`** is the single-process self-host shape
(compose, systemd, the chart's `web.mode: all`). **`web` + N × `voice`** is the
split shape the hosted service runs (the chart's default `web.mode: web` plus
`voice.replicas`): the web tier never touches Discord, and exactly one worker —
the `presence_owner` election winner — dispatches interactions.

**Audio frames never cross a process boundary** (ADR-0005). The Voice Instance
opens the Discord voice WebSocket itself and keeps Opus/PCM inside its own
address space; v1's gRPC AudioBridge is gone. Control and telemetry over RPC are
unaffected (ADR-0014).

In `all` Mode the web handler starts and stops the loop through an in-process
`internal/session.Manager`, which holds the loop's cancel func — no loopback RPC
(ADR-0039). A `web`-only replica uses `session.IntentControl` instead
(`internal/session/intentcontrol.go`): `StartSession` inserts a
`voice_session_intents` row and polls until a worker's claim loop
(`internal/session/claimloop.go`) claims it and reports it live; `StopSession`
flags the intent and polls for the wind-down. The live controls that need the
in-memory session (mute, `/say`, replay) degrade in that split.

Other subcommands sit beside the Modes: `glyphoxa migrate` (ADR-0031), `seed`,
`export`, `billing` (ADR-0054), `user`, and `version` (the stamped release and
whether the binary links the real Opus codec and DAVE — the untagged
`make build` does not, see [configuration.md](configuration.md)).

### Tree vs ADR

- ADR-0005 specifies a `voice_sessions(guild_id PK, voice_instance_id,
  claimed_at, heartbeat_at)` claim table plus `LISTEN/NOTIFY` handoff. What
  shipped is ADR-0057's claim plane in a separate table:
  `voice_session_intents` (`00035_voice_session_intents.sql`,
  `internal/storage/voiceintent.go`) holds one live intent per Tenant, which a
  worker claims with `FOR UPDATE SKIP LOCKED`, heartbeats while it drives the
  session, and finishes on stop or drain; an intent whose heartbeat goes stale
  is reaped, so a dead worker cannot strand a Tenant. `voice_sessions` stays the
  per-Campaign session record (`id`, `campaign_id`, `started_at`, `ended_at`,
  `status`, `line_count`, `end_reason`). **Still not built** from ADR-0005:
  claiming columns on `voice_sessions` itself and `LISTEN/NOTIFY` handoff —
  workers poll. Crash orphans are swept by `Manager.ReconcileOrphans` at boot
  and, in the pool, by `ReconcileWorkerOrphanedVoiceSessions`. Rollout notes:
  [devs/2026-07-19-voice-claim-plane-rollout.md](devs/2026-07-19-voice-claim-plane-rollout.md),
  [devs/2026-07-20-voice-fleet-rollout.md](devs/2026-07-20-voice-fleet-rollout.md).
- ADR-0031 says `all` Mode auto-applies migrations at startup. **Wired** (#282):
  the web/all boot preflight `ensureSchemaReady` (`cmd/glyphoxa/main.go`) runs
  `autoMigrate` (`cmd/glyphoxa/migrate.go`, `storage.MigrateUp` under the
  advisory lock) in `all` Mode. A `web`-only replica never migrates — it only
  verifies the schema is current and refuses to boot with the actionable
  `migrate up` error when it is behind, so N replicas cannot race the migration.
  `voice` Mode runs no preflight and assumes a current schema.

---

## 2. The voice pipeline

> ADR-0019 ([orchestrator-first TDD](adr/0019-orchestrator-first-tdd-voice.md)) ·
> ADR-0026 ([bus wiring: typed reactors composed into a Conversation](adr/0026-bus-wiring-reactors-and-conversation.md)) ·
> ADR-0020 ([shared voice event taxonomy](adr/0020-shared-voice-event-taxonomy.md)) ·
> ADR-0006 ([DAVE/MLS at session start](adr/0006-dave-mls-no-mid-session-migration.md))

A Voice Session is the Bot's presence in one Discord voice channel, bound to a
(Guild, Campaign, GM) tuple. Its stages, in order:

```
Discord Opus (DAVE-decrypted)
  → per-speaker Opus decode + resample + reframe   pkg/voice/wire/codec, pkg/voice/wire/codec/dsp
  → VAD (Silero, one session per Speaker Lane, §2.6) pkg/voice/vad/silero
  → STT (streaming websocket, batch POST fallback) pkg/voice/stt, pkg/voice/stt/elevenlabs
  → Address Detection (deterministic fuzzy chain)  pkg/voice/address
  → Agent turn (Hot Context → LLM → tool loop)     pkg/voice/agent, pkg/voice/agenttool
  → TTS, sentence-at-a-time                        pkg/voice/tts/elevenlabs
  → Opus encode + 20 ms playback pump → Discord    pkg/voice/wire
```

The stages are typed reactors on one in-process event bus
(`pkg/voice/voiceevent.Bus`), composed into an `orchestrator.Conversation`
(ADR-0026). The event names are the shared taxonomy of ADR-0020 —
`vad.speech_start`, `stt.partial`, `stt.final`, `address.routed`, `tts.invoked`,
`voice.first_opus`, `turn.ended`, `barge.detected`, `mute.changed`,
`spend.cap_reached`, `connection.state` — and the same names ride the SSE stream
to the browser. `internal/wirenpc` is the composition root that assembles the
whole loop against a live Discord session (`pkg/voice`).

Voice is DAVE/MLS end-to-end encrypted; the handshake runs at session start and
there is no mid-session migration (ADR-0006, `pkg/voice/dave.go`).

### 2.1 STT: batch and streaming

> ADR-0042 ([streaming STT + speculative memory recall](adr/0042-streaming-stt-speculative-memory-recall.md))

Two transports behind one `stt` interface:

- **Streaming** (default since 2026-11-22, #702) — ElevenLabs Scribe v2 Realtime
  over a persistent websocket per Speaker Lane
  (`pkg/voice/stt/elevenlabs/stream.go`, `pkg/voice/stt/streaming.go`,
  `pkg/voice/orchestrator/sttstream.go`). **Local VAD stays the endpointing
  authority** (`commit_strategy: "manual"`); provider-side endpointing was
  rejected because Barge-in needs a local, network-independent VAD anyway.
  Measured live: the commit round-trip is ~150–180 ms, speech-end → routed
  150–290 ms.
- **Batch** — one `/v1/speech-to-text` POST per endpointed utterance
  (`pkg/voice/stt/elevenlabs/transcribe.go`). Measured speech-end → routed:
  430–650 ms, so streaming buys roughly 350 ms per turn (the "~1.5 s" this
  section and ADR-0042 used to quote was never reproduced). It is the automatic
  per-utterance fallback whenever an utterance cannot stream, and the only path
  with `GLYPHOXA_STT_STREAMING=0`.

The Segmenter buffers every utterance locally and only mirrors frames to the
stream, so a fallback loses nothing. Three things keep streaming actually
streaming on a real table (ADR-0042 amendment 2026-11-22):

- **Idle keepalive.** The provider closes a realtime session 15 s after the last
  client *text* message; websocket pings do not count. The adapter sends an empty
  `input_audio_chunk` after 5 s of client silence.
- **Death notification.** The adapter reports a session's end the moment it
  happens (`stt.StreamConfig.OnClosed`); the manager redials at once if the
  session had been healthy (≥ 10 s), with bounded backoff otherwise, instead of
  finding out on the next utterance.
- **Any commit error retires the session**, so audio the provider still holds
  uncommitted can never fold into the next utterance's final.

`glyphoxa_voice_stt_final_total{provider,transport,reason}` counts every utterance
by the transport that produced its final — `stream`/`committed`,
`batch_fallback`/`stream_down|stream_died|commit_error|commit_timeout`, or
`batch`/`streaming_off|lane_cap` — which is the only series that tells a streamed
final from a fallback.

Streaming adds `STTPartial` to the taxonomy: mutable interim text of the
in-progress utterance. Only the committed `STTFinal` reaches Address Detection and
the Transcript.

### 2.2 Address Detection

> ADR-0024 ([deterministic fuzzy chain on raw STT](adr/0024-address-detection-deterministic-fuzzy-chain.md))

Runs on the **raw** `STTFinal` — no LLM transcript-correction pass in front of it,
which is what let v1 rewrite NPC names before the matcher saw them. ADR-0024 frames
it as an ordered chain (name → last-speaker → none). In the tree it is not a chain
of early-returns but a **weighted scoring stack** of `Heuristic`s, each contributing
to a per-Agent score against a threshold of 1.0. What production runs is
`address.DefaultHeuristicsWithin(continuationWindow())` (`pkg/voice/address/matcher.go`,
wired in `internal/wirenpc/roster.go`), **two** heuristics:

| Heuristic | Weight | Mirrors ADR-0024 stage |
|-----------|--------|------------------------|
| `NameMatch` | 1.0 (decisive) | 1 — name/alias: exact in any position; fuzzy (per-language phonetic encoder, then a Damerau-Levenshtein net, plus derived leading-consonant-truncation aliases) **only with vocative evidence** |
| `LastAddressed` | 1.0 (decisive) | 2 — last-speaker continuation, inside the Continuation Window and only for the speaker who opened the exchange |

Below the threshold ⇒ no target (the last stage); the utterance is still transcribed.
Three things from the ADR-0024 amendment of 2026-11-20 shape this:

- **A fuzzy name is not decisive on its own.** A non-exact hit counts only when its
  span carries the Vocative Flag ("Bard, …", "…, Barth?") and is not a single
  everyday word of the Campaign Language (`pkg/voice/address/commonwords.go`).
  Without that rule a short name's phonetic neighbours ("bereit", "Brot", "more",
  "great") routed ordinary table talk to NPCs. The precision/recall gate is
  `pkg/voice/address/corpus_test.go`.
- **Continuation is speaker-scoped and anchored to the reply.** Only the speaker
  whose named address opened the claim continues without a name (table-wide when
  speaker ids are unknown). The `AddressDetector` moves the window's anchor on every
  `FirstAudio` of a turn it routed and again on that turn's `TurnEnded{completed}`
  (`Matcher.ExtendContinuation`), so the 30s count from the end of the Agent's reply
  rather than from the address; a Barge-in revokes the claim
  (`ClearContinuation`, ADR-0027).
- **No lone-NPC fallback in the shipped stack.** `SoleActiveNPC` exists as an opt-in
  heuristic only; a lone Character NPC is opened by name like any other. The
  never-fed `ExpertOnRecentWord` and `RecentlyInterrupted` heuristics were removed.

`Detect` returns a *set*. **`MaxTargets` defaults to 1**, so naming two NPCs in one
breath fires one turn on the top-scored Agent (ADR-0038). An **Address-Only** Agent
is reachable only via `NameMatch`; no ambient heuristic can ever
route to it. A muted Agent stays in the fuzzy index and is still matched by name,
but is likewise excluded from every ambient heuristic — muting must never re-route an
explicit address to a different NPC.

No LLM in the loop: matching is a pure function over a mutex-guarded index/roster
snapshot, sub-millisecond, and fully unit-testable.

**Tree vs ADR.**

- ADR-0024 says "the Butler defaults `AddressOnly=true` … and responds to
  voice-address only from the GM." **Shipped (#280):** the loop loads the
  Campaign's Butler as an Address-Only Agent (`butlerSpec`,
  `internal/wirenpc/agentspec.go`), and `orchestrator.WithButlerGMGate` drops a
  Butler route unless the utterance's Speaker Lane belongs to a GM
  (`Config.GMSpeaker`). A non-GM naming the Butler gets no reply.
- Agents carry aliases, and both Address Detection and `/glyphoxa mute` resolve
  them — but the Campaign screen ships **no alias editor**
  (`web/src/screens/campaign/Campaign.tsx` passes `aliases` straight through). The
  console cannot author them.

### 2.3 The Agent turn: deliver-then-commit

> ADR-0012 ([turn-end commits delivered sentences only](adr/0012-deliver-then-commit-sentence.md)) ·
> ADR-0022 ([TTS provider interface](adr/0022-tts-provider-interface.md))

An addressed Agent assembles **Hot Context** (recent Transcript + retrieved
Transcript Chunks + KG facts + Persona), calls its LLM, and streams TTS
sentence-at-a-time (`pkg/voice/agent/sentence.go`).

Each sentence is one HTTP `POST /v1/text-to-speech/{voice}/stream` on the voice's
model — `eleven_v4_turbo` by default (ADR-0023 amendment 2026-11-22). The live
wiring pre-warms the connection on connect and barge, and on a speech start
after 20 s without pool activity (`internal/wirenpc/ttswarm.go`), so a reply's
first sentence does not dial.
Provider first audio, measured 2026-11-22 from the dev box (German lines):

| TTS path | First audio |
|---|---|
| `eleven_v4_turbo`, warm connection (default) | ~140 ms |
| `eleven_v4_turbo`, cold connection (TCP+TLS) | ~185–230 ms |
| `eleven_v3`, warm connection (pre-#702 default) | ~550–680 ms |

That is the `glyphoxa_voice_tts_ttfb_seconds` stage of `response_latency`; STT
(~1.5 s batch, §2.1) and the LLM's first sentence dominate the rest.

A sentence counts as **delivered** when its last Opus frame is forwarded to
Discord. At turn end — natural or barged — the Transcript utterance commits **only
delivered sentences**. Zero delivered ⇒ the utterance is not logged at all. This
is what keeps the Transcript equal to what the room actually heard, and it keeps
Address Detection and retrieval from ingesting words nobody heard.

**Tree vs ADR.** ADR-0012's per-utterance `was_interrupted` /
`interrupted_by_user_id` fields do not exist: `transcript_line`
(`00007_transcript_lines.sql`) carries no interruption columns. `BargeDetected`
now names the barging speaker (`SpeakerID`, §2.6), so the attribution is
available; nothing persists it yet.

### 2.4 Barge-in

> ADR-0027 ([per-participant confirm window cancels the whole turn](adr/0027-barge-in-confirm-window-cancels-turn.md))

A policy layer over the existing VAD events (`pkg/voice/orchestrator/barge.go`,
`floor.go`), not new VAD machinery. Two gates, in order:

1. **The Agent must be audibly speaking** — a barge can only fire after
   `voice.first_opus`. A turn that merely holds the floor (assembling Hot Context,
   waiting on the LLM) cannot be barged; gating on floor-held instead was a real
   self-cancel bug.
2. **Confirm window** — continuous voiced speech must persist past a threshold
   before the floor yields. Shorter bursts are **Soft-overlap** (a backchannel):
   they do not cancel the Agent, and they are still transcribed normally.

On `barge.detected` the turn is torn down at the forward boundary — stop
forwarding at once, discard the in-flight sentence and any pre-rendered audio,
cancel the upstream TTS and LLM. Delivered sentences commit per ADR-0012. There is
**no auto-resume**: the human's interruption is just the next utterance through the
same pipeline.

**Tree vs ADR.** ADR-0027 specifies the confirm window as a **per-Agent tunable**
measured **per participant**. Neither ships:

- The window is a package constant, `bargeConfirmWindow = 250 * time.Millisecond`
  (`internal/wirenpc/wirenpc.go`), passed once into
  `orchestrator.WithBargeInCoalesce` for the whole Conversation. There is no
  per-Agent knob.
- It **is** armed per participant since Speaker Lanes (§2.6):
  `pkg/voice/orchestrator/barge.go` keys pending windows by `SpeakerID`, so one
  speaker's burst never completes another speaker's window, and `BargeDetected`
  names the barging speaker. The 250 ms value predates lanes (at zero, the
  addressing speaker's own continued speech cancelled the turn it had just
  triggered) and has not been re-tuned since.
- `interrupted_by_user_id` is still not persisted (§2.3).

A second window, `floorCoalesceWindow`, guards a different failure: one utterance
split by VAD into two STT segments would open two turns, the second superseding the
first mid-synthesis. A `Floor.Take` landing inside the window yields to the
in-flight turn instead.

### 2.5 Multi-NPC: one floor, one Cast, one turn

> ADR-0038 ([single-target default, programmatic roster](adr/0038-multi-npc-single-target-default-programmatic-roster.md)) ·
> ADR-0025 ([Ensemble Turns — **design of record, not implemented**](adr/0025-ensemble-turns-speculative-lead-reaction.md))

A Voice Session may host several Character NPCs. There is exactly **one Barge-in
floor** for the whole scene, so the safe unit of work is one addressed turn. One
reply strategy — an `agent.Cast` holding `AgentID → *Replier` — takes the floor on
every `address.routed` and delegates to the single Replier whose Agent was
addressed. Each Agent self-filters; only one Replier ever runs per route.

`internal/wirenpc.Roster` ties one `address.Matcher` to one `agent.Cast`;
`AddNPC`/`RemoveNPC` move an NPC into or out of both halves in lockstep, so an NPC
is never routable-but-silent or speaking-but-unroutable. This is a **programmatic
API only** — no Slash Command or RPC adds an NPC mid-scene.

The **Ensemble Turn** (speculative fan-out → Lead race → Cross-talk Reaction →
queued follow-up) is a **design of record** and is *not implemented*: a
turn-taking layer for it was built but never wired, and was removed on
2026-11-20 (ADR-0025 names the last commit that contained it). Setting
`MaxTargets > 1` or `-1` still makes the matcher return a score-sorted set, but
the `AddressDetector` publishes only its top decision, so one utterance always
opens at most one turn.

### 2.6 Speaker Lanes

> ADR-0050 ([per-speaker utterance segmentation](adr/0050-per-speaker-utterance-segmentation.md))

Discord separates speakers on the wire, and the pipeline keeps them separate.
Inbound Opus is decoded with **one decoder per speaker** (`pkg/voice/wire/codec`
— a decoder is stateful per Opus stream), and each speaker gets a **Speaker
Lane**: its own Silero VAD session (`orchestrator.WithSpeakerLanes`, wired in
`internal/wirenpc/pipeline.go`) and, with streaming STT, its own STT stream
(capped by `GLYPHOXA_STT_STREAM_MAX_LANES`). `SpeakerID` — the Discord
snowflake — rides `VADSpeechStart`/`VADSpeechEnd`, `STTPartial`/`STTFinal` and
`BargeDetected` (`pkg/voice/voiceevent`).

Consequences:

- The Transcript names humans: `transcript.Relay` resolves each line's
  `SpeakerID` to a Player Character or the GM (`internal/speaker`, ADR-0050 as
  amended by ADR-0055) instead of one anonymous `player` lane.
- Barge-in confirm windows are per speaker (§2.4), and the Butler GM gate
  (§2.2) keys off the lane's speaker.
- The Rollover Tape and Highlights (ADR-0051, `internal/tape`,
  `internal/highlight`) build on the lanes for per-participant consent.

### 2.7 Provider adapters that exist

| Component | Shipped adapter packages | Wired into the live loop |
|-----------|--------------------------|--------------------------|
| STT | `pkg/voice/stt`, `pkg/voice/stt/elevenlabs` (batch + streaming) | yes |
| TTS | `pkg/voice/tts/elevenlabs` | yes |
| LLM | `pkg/voice/llm/groq`, `pkg/voice/llm/openaicompat`, `pkg/voice/llm/gemini`, `pkg/voice/llm/anthropic` | all three providers, chosen per Voice Session by `internal/llmbuild`: `groq` and `gemini` via `openaicompat` (ADR-0037), `anthropic` on its own Messages adapter |
| Embeddings | `pkg/voice/embeddings/ollama` | yes (`internal/embedworker`, `internal/recall`) |
| VAD | `pkg/voice/vad/silero` | yes |

Which LLM a session runs is one resolution, `llmbuild.DeploymentDefault.Select`:
the primary Agent's bound Provider Config, else the Tenant's newest `llm` row,
else the **deployment default** — `GLYPHOXA_LLM_DEFAULT_PROVIDER` /
`GLYPHOXA_LLM_DEFAULT_MODEL`, read once at boot, Groq `openai/gpt-oss-120b` when
unset (ADR-0036, amendment 2026-11-22). The voice loop, the Highlight
classifier, recap, assist, the planning chat and the Configuration RPCs all go
through it, so the adapter built, the model requested, the span label and the
price row always name the same provider. The Configuration screen offers two
voice-LLM slots, Groq and Anthropic, and shows which is in effect; Gemini as a
voice LLM is reachable as a deployment default or a bound config only.

The managed provider matrix is Groq or Anthropic (LLM) + ElevenLabs (STT + TTS)
(ADR-0039 and its 2026-11-22 amendment).
OpenAI TTS, named alongside ElevenLabs in
[ADR-0023](adr/0023-tts-provider-matrix-elevenlabs-openai.md), is not built.
Retries live in the orchestrator stages, not the adapters — `pkg/voice/retry`
wraps the STT, TTS and LLM start calls, classifying typed
`pkg/voice/providererr.HTTPError` values; the per-turn deadline is never extended
and a barge-in cancellation aborts mid-backoff (ADR-0044).

---

## 3. Agents and the Tool framework

> ADR-0009 ([single Agent table; Butler auto-created](adr/0009-single-agent-table-auto-butler.md)) ·
> ADR-0028 ([one internal Tool interface, simple registry](adr/0028-tool-framework-internal-interface-simple-registry.md)) ·
> ADR-0029 ([Tool Grants: least-privilege, per-grant scoping](adr/0029-tool-grants-least-privilege-scoping-config.md)) ·
> ADR-0030 ([side effects deferred to turn-commit](adr/0030-tool-side-effects-deferred-to-turn-commit.md))

One polymorphic `agents` table holds both Agent Roles — `butler` and `character`
— so one Matcher and one Cast *can* host a mixed roster on one code path. A trigger
(`00002_auto_butler.sql`) creates exactly one Butler per Campaign.

**The Butler is live in the voice loop.** It is auto-created by the
`00002_auto_butler.sql` trigger with `address_only = true` (a partial unique
index forbids a second) and is editable in the console — Persona, Voice, Tool
Grants. The loop loads it alongside the Campaign's Character NPCs
(`butlerSpec`, `internal/wirenpc/agentspec.go`) as an Address-Only Agent that
only the GM can voice-address (§2.2). It also answers the Butler planning chat
in the console (ADR-0062, `ChatService`). `/roll` stays a Bot-binary command
that runs the built-in `dice` Tool directly, never a Butler turn (§5).

**Tool** is the domain term (`pkg/tool`). A Tool is `{name, input JSON schema,
handler}`; its *backing* — built-in Go function or an out-of-process MCP Server —
is an implementation detail behind that interface. The `Registry` is a map.
The built-ins are registered in `pkg/tool/builtins.go`: `dice`,
`transcript_search`, `kg_query`, `remember_knowledge`, `recap`, and the spatial
Tools (ADR-0060). The MCP Server adapter is not built.

**Tool Grants** are `{tool_name, config?}` structs, persisted in
`tool_agent_grant` (`00013_tool_agent_grant.sql`) and editable from the Campaign
screen. Enforcement is two-layered:

- **Grant-stripping.** Ungranted Tools are filtered out before the prompt is
  built and never declared to the model.
- **Scope in the handler, never the LLM.** The handler receives the grant config
  at execution time — `Execute(ctx, args, grantConfig)` — so the same registered
  Tool behaves differently per caller. The model is told "you can remember
  knowledge"; "only about yourself" is applied by the handler. The LLM cannot
  widen its own scope by crafting arguments.

**Side effects.** Each Tool declares one bit, read-only or side-effecting.
Read-only Tools execute inline during generation. ADR-0030's general mechanism —
side-effecting Tools record intent and flush at turn-commit — **is not built**:
`pkg/tool/loop.go` refuses any Tool that is neither read-only nor
**proposal-mediated**. ADR-0052 is the shipped exception: `remember_knowledge`
writes a GM-reviewed Knowledge Proposal instead of mutating the Knowledge Graph,
so it runs inline.

---

## 4. The web tier

> ADR-0013 ([SPA: Vite + React — React 19 since the 2026-11 amendment](adr/0013-spa-vite-react-18.md)) ·
> ADR-0015 ([Buf Connect end-to-end](adr/0015-buf-connect-end-to-end.md)) ·
> ADR-0014 ([bus + SSE to the browser](adr/0014-grpc-bus-plus-sse.md)) ·
> ADR-0018 ([TanStack Router + Query + connect-query](adr/0018-tanstack-router-and-query.md)) ·
> ADR-0017 ([Radix + plain CSS tokens](adr/0017-radix-plus-plain-css-tokens.md)) ·
> ADR-0039 ([single-operator web tier](adr/0039-mvp-ui-backend-single-operator-web-tier.md)) ·
> ADR-0041 ([operator allowlist](adr/0041-operator-allowlist-access-policy.md))

### 4.1 Shape

A Vite + React 19 SPA (`web/`) built to `internal/spa/dist` and served from
`embed.FS` by the same binary (`internal/spa`), so the single-binary deployment
shape survives. The screens (`web/src/screens/`):

| Screen | Purpose |
|--------|---------|
| `landing`, `legal` | public landing page; Impressum, privacy policy, terms |
| `login` | Discord OAuth entry |
| `onboarding` | create a Tenant after an open-admission signup (ADR-0055) |
| `configuration` | Provider Configs (BYOK), Discord settings, bot-authorization link, spend caps |
| `campaign` | sub-views in the sidebar, addressed by path (ADR-0063): cast/Agents + Tool Grants, the Knowledge Graph wiki and graph view, Maps, Players |
| `session` | Start/Stop the Voice Session, live Transcript feed, NPC roster + mute, Highlights |

There is no separate transcripts screen — the live Transcript feed and its search
live inside `session`. The Campaign screen has no alias editor, so an Agent's
aliases (which Address Detection and `/glyphoxa mute` both resolve) can only be set
outside the console today.

Routing is TanStack Router with a code-based tree (`web/src/app/router.tsx`); data
fetching is TanStack Query + `@connectrpc/connect-query` (ADR-0018). Per the #65
addendum to ADR-0015, Connect-ES v2 folded `protoc-gen-connect-es` into
`protoc-gen-es`, so the browser client is built at runtime with
`createClient(Service, transport)` from a single generated `*_pb.ts`
(`web/src/lib/transport.ts`).

### 4.2 RPC surface

One `.proto` source of truth (`proto/glyphoxa/management/v1/management.proto`)
serves both Go (`connect-go`) and TypeScript. Shipped services, all under
`glyphoxa.management.v1` and all implemented in `internal/rpc`:

- `CampaignService` — Campaigns, Active Campaign, Agents, KG Nodes/Edges, Tool Grants
- `AuthService` — `GetCurrentUser` (the one unauthenticated procedure), `Logout`
- `ProviderService` — Provider Configs, Discord settings, invite resolution, spend caps
- `SessionService` — session snapshot, Start/Stop, mute, transcript search
- `VoiceService` — ElevenLabs voice catalog + preview, Groq model catalog, provider health
- `ChatService` — Butler Planning Threads (ADR-0062): list/get/create/rename/delete, `PlanningExchange`

**Tree vs ADR.** ADR-0015 also lists `TenantService` and a
`glyphoxa.voice.v1.VoiceControlService` (`claim_session` / `release_session` /
`push_event`). Neither is in `proto/`. Multi-tenant membership is deferred
(ADR-0039), and `all` Mode drives sessions in-process rather than through a
control RPC.

### 4.3 Real-time: SSE, not WebSocket

ADR-0014 defines two hops. **Hop A** (Voice Instance → web tier) is behind a `Bus`
interface: in-process Go channels for `all` Mode (`voiceevent.Bus`, one per
process), gRPC for the split Modes — the gRPC impl is not built. **Hop B** (web
tier → browser) is Server-Sent Events.

The carve-outs are plain `net/http`, deliberately outside Connect (ADR-0015):

- `GET /api/v1/sessions/{id}/events` — the live SSE tail (`transcript.Relay.ServeEvents`)
- `GET /api/v1/sessions/{id}` — the snapshot (`transcript.Relay.ServeSnapshot`)
- `/auth/discord/login`, `/auth/discord/callback` — OAuth redirects

Every plain mount except the OAuth redirects is a row in the guarded mount
table (`auth.MustGuardMounts`): one transport-agnostic policy
(`auth.Policy`) makes the session/CSRF/tenant decisions for BOTH the Connect
interceptor stack and these plain mounts, and each row declares its tenant
posture explicitly — an undeclared row fails the boot. The table replaced
per-mount hand-composition of `auth.RequireSession`/`RequireTenant`/
`RequireCSRF`, whose drift shipped #408 (clip/image mounts composed
session-only, so they 401'd in production while the Connect RPCs passed on
the same cookie). Connect server-streaming
lacks EventSource semantics (`Last-Event-ID`, proxy compatibility); the browser has
no bidirectional need on the Session screen, so WebSocket was rejected. SSE frames
call `queryClient.setQueryData(...)` to amend the cached snapshot rather than
maintaining a second React state tree (ADR-0018).

### 4.4 Access: single Operator, mandatory allowlist

ADR-0039 scopes the web tier to **one Operator on a self-host box**. Discord OAuth
plus an opaque `glyphoxa_session` cookie (HttpOnly, Secure, SameSite=Lax) gate the
app (ADR-0016); one Tenant is auto-seeded and claimed by the first allowlisted
Operator. The `X-Tenant-Id` interceptor and `/t/:slug` prefix stay as thin
pass-throughs so the multi-tenant surface fills in later without a rewrite.

Authorization is ADR-0041's **mandatory operator allowlist**:

- `GLYPHOXA_OPERATOR_IDS` — Discord User snowflakes, checked at the OAuth callback
  *before* any session issuance or Tenant write. There is **no** trust-on-first-use.
- A `web`/`all` Instance **refuses to boot** unless all three `DISCORD_OAUTH_*`
  variables and a non-empty allowlist are set — or `GLYPHOXA_DEV_MODE` is.
- Every non-dev boot sweeps sessions whose owner has left the allowlist
  (`storage.RevokeSessionsOutsideAllowlist`), so a restart applies a grant change
  fully.
- `GLYPHOXA_DEV_MODE` auto-authenticates a synthetic Operator **and forces the
  listen address to `127.0.0.1`**, making production misuse structurally
  ineffective.

Setup runbook: [docs/configuration.md](configuration.md).

---

## 5. Discord surface: standing presence and Slash Commands

> ADR-0010 ([slash command surface](adr/0010-slash-command-surface.md)) ·
> ADR-0047 ([invite resolver + bot authorization](adr/0047-discord-invite-resolver-bot-authorization.md)) ·
> ADR-0043 ([gateway fatal-vs-transient classification](adr/0043-gateway-fatal-transient-classification.md))

The presence is a **per-Tenant registry** of standing gateway clients
(`internal/presence/clients.go`, `tenants.go`; #489, ADR-0057), keyed by each
Tenant's resolved Bot token and shared with the voice Manager — never a second
connection per Voice Session. The token ladder (`wirenpc.ResolveDiscordToken`,
`internal/wirenpc/credentials.go`): a Tenant's saved BYOK Bot token (decrypted
with `GLYPHOXA_SECRET`) wins; with none saved, the deployment-wide
`DISCORD_BOT_TOKEN` is the fallback rung. Central-token Tenants therefore share
one client serving many Guilds; a BYOK Tenant gets its own client, whose token
death surfaces for that Tenant only, and a Tenant saving Discord settings
rebuilds only its own client. Commands register per-Guild, idempotently, and
survive with no Voice Session active. In the split shape only the
`presence_owner` election winner dispatches interactions.

Shipped commands (`internal/presence`):

| Command | Who | Handled by |
|---------|-----|------------|
| `/roll <dice>` | anyone in the Guild | the Bot binary, directly, via the built-in `dice` Tool |
| `/glyphoxa use <campaign>` | GM only | sets the durable Active Campaign |
| `/glyphoxa start` / `end` | GM only | the Session Manager (in-process) or a claim-plane intent (split) |
| `/glyphoxa search <query>` | GM only | tsvector search over the Active Campaign's Transcript Lines |
| `/glyphoxa recap` | GM only | a recap of the session so far |
| `/glyphoxa mute <npc>` / `muteall` | GM only | matcher-internal mute state |
| `/say <text>` | GM only | an NPC speaks the given text in the live session |
| `/direct as:<npc>` | GM only | a hidden GM note steering that NPC's next replies (ADR-0059) |
| `/where map:<map>` | GM only | sets the Party Marker so NPCs know their surroundings (ADR-0060) |

**Tree vs ADR.** `mute`/`muteall`, `recap`, `/say`'s final shape, `/direct`
and `/where` postdate ADR-0010. ADR-0010 says the Butler handles a command "when
reasoning is required" — no command reaches the Butler today; `/roll` is answered by
the Bot binary running the built-in `dice` Tool directly (§3). Per the ADR's #102
amendment, "GM only" in v1.0 means *the invoking Discord User's snowflake is on the
operator allowlist* — `tenant_members.role` does not exist yet. Discord's own command
permissions are a UX hint; the server-side check is the only safe place.

Commands are registered only by a process that owns Bot gateways: `all` Mode or
a `voice` worker (§1). `-mode web` exposes none.

Discord REST reads use plain `net/http`, not disgo's rest client, which leaks a
goroutine per call: `internal/discordtag` (bot tag) and `internal/discordinvite`
(invite → Guild → voice channels) share one seam shape. The bot-authorization URL
is built client-side (`web/src/screens/configuration/AddBotLink.tsx`) from a
non-secret `discord_application_id` echoed on `ListProviderConfigsResponse`; scope
is `bot applications.commands` because the Slash Commands would otherwise be
invisible in a freshly added Guild.

Gateway failures are classified by typed error at the reconnect loop
(`internal/wirenpc/gatewayfatal.go`): close 4004 → `invalid_bot_token`, 4013/4014 →
`disallowed_intents`, HTTP 403 → `bot_not_authorized`; everything else stays
transient and keeps its bounded backoff. `end_reason` is a stable machine prefix
plus prose, and the failure reaches the Operator asynchronously as a
`connection.state` SSE frame plus a persisted `failed` row — never as a
`StartSession` error.

---

## 6. Storage

> ADR-0008 ([Postgres-backed Knowledge Graph](adr/0008-postgres-knowledge-graph-layered.md)) ·
> ADR-0011 ([Transcript Chunks with async embeddings](adr/0011-transcript-chunks-async-embeddings.md)) ·
> ADR-0040 ([Transcript Line persistence](adr/0040-transcript-line-persistence.md)) ·
> ADR-0031 ([goose with embedded SQL migrations](adr/0031-postgres-migration-tooling.md)) ·
> ADR-0004 ([BYOK provider key matrix](adr/0004-byok-provider-key-matrix.md))

One Postgres, plus pgvector. All access goes through `internal/storage`.

### 6.1 Tables

`tenant`, `campaign`, `agents`, `provider_config`, `deployment_config`,
`tool_agent_grant`, `users`, `sessions`, `voice_sessions`, `transcript_line`,
`transcript_chunk`, `kg_node`, `kg_edge`.

### 6.2 The two Transcript grains

They are **separate records of the same speech**, and conflating them is the
mistake ADR-0040 exists to prevent:

| | **Transcript Line** (ADR-0040) | **Transcript Chunk** (ADR-0011) |
|---|---|---|
| Grain | one rendered line: one human utterance, or one coalesced Agent reply | 3–6 utterances |
| Serves | the Session screen's live feed and replay-on-reload; user-facing search | NPC knowledge retrieval (Hot Context) |
| Table | `transcript_line` | `transcript_chunk` |
| Written by | `transcript.Relay` — async UPSERT off a non-blocking queue | `transcript.Chunker` |
| Retrieval | tsvector (`00015_transcript_line_fts.sql`) | pgvector ANN, partial HNSW index on non-null embeddings |
| Ordering key | `seq` (the relay's monotonic frame seq) | — |

`voice_sessions.line_count` is `COUNT(*)` of Transcript Lines, made authoritative
by a flush barrier through the relay's own writer queue on Stop. The bus delivers
synchronously and must never block, so the relay tees each line into a buffered
queue and drops-and-logs on overflow rather than calling the DB inline.

Chunk embeddings are **async and eventually consistent**: insert with
`embedding = NULL`, `internal/embedworker` claims and `UPDATE`s. Retrieval filters
`WHERE embedding IS NOT NULL`; a Prometheus backlog gauge exposes a stalled
pipeline. Default model is Ollama `nomic-embed-text` (768-dim). Per ADR-0011's #120
amendment, **user-facing search reads the Line grain, not the chunk grain** — a
line hit carries an exact speaker and timestamp and can deep-link into the Session
screen.

`internal/recall` speculates retrieval over the `STTPartial`s that close a
sentence and, on a matching `STTFinal`, injects the prefetched chunks at zero
added latency. The final preempts that speculation — it joins an in-flight embed
of the same text and cancels one of another — and otherwise embeds inline under a
hard budget (500 ms by default, `GLYPHOXA_RECALL_BUDGET`), degrading to no-memory
past it (ADR-0042 and its amendments).

### 6.3 Knowledge Graph

Typed **Nodes** (`Character`, `NPC`, `Location`, `Faction`, `Item`, `PlotThread`,
`Note`) and typed directional **Edges** in plain Postgres tables — not a graph
database (ADR-0008). v1.0 is a structured wiki: form-based UI
(`web/src/screens/campaign/KnowledgePanel.tsx`), tsvector search, `gm_private`
visibility.

- Edges are **strictly directional, no auto-inverse**; mutual relationships are
  two Edges. Validity is **object-side-only**: `resides_in` must target a Location,
  `member_of` a Faction, `participated_in` a PlotThread, `parent_of` a
  Character/NPC on both ends. The social types (`knows`, `owns`, `enemy_of`,
  `ally_of`, `mentioned_in`) are unconstrained — TTRPG worlds legitimately contain
  sentient swords that know kings.
- An NPC **Node** and a Character NPC **Agent** are separate records. The optional
  link is a nullable, unique `kg_node.agent_id`, set manually from the Campaign
  screen; creating an Agent never auto-creates a Node.
- `gm_private` filtering applies to neighbour expansion too: an Edge may exist to a
  private Node without surfacing it into an NPC's Hot Context (`internal/kgfacts`).

### 6.4 Migrations

goose v3 with plain SQL files in `internal/storage/migrations/`, embedded via
`//go:embed`, sequential zero-padded prefixes, up + down in one file. The provider
takes a Postgres advisory-lock session locker (`internal/storage/migrate.go`) so
simultaneous instance startups serialize — the same Postgres coordination substrate
ADR-0005 chose, no new infrastructure. Applied with `glyphoxa migrate up`, or
automatically at startup in the default `-mode all` (`autoMigrate` in
`cmd/glyphoxa`, under the same advisory lock) — the serving `web`/`voice` Modes
assume a current schema and fail fast if behind (ADR-0031).

### 6.5 Credentials

Provider Configs are Tenant-scoped and encrypted at rest with AES-GCM
(`internal/storage/crypto`, keyed by `$GLYPHOXA_SECRET`) — **BYOK** (ADR-0004).
The secret is write-only; only `credentials_last4` is ever read back.

Credential resolution is a documented **hybrid** (ADR-0039): a saved key wins,
discriminated by `credentials_last4 != "env"` (the placeholder `seed` writes);
otherwise the adapter's own ENV variable is used. DB-as-sole-source is the stated
end-state. Without `$GLYPHOXA_SECRET` the web tier still boots and reads — only
*saving* a key fails, loudly (`CodeFailedPrecondition`).

### 6.6 Blob storage seam

Binary payloads — Epic 8 Highlight clips and generated images, later ADR-0011
audio extracts and export bundles — live behind a small streaming `Store`
interface in `internal/blob` (ADR-0048): `Put`/`Get`/`Delete`, keyed by the
tenant-scoped path `t/<tenant_id>/<owner-kind>/<owner-id>/<name>`. The package
owns both key construction (`Key`) and validation (`ValidateKey`); the tenant
prefix is mandatory and the backend derives `tenant_id` from the key, so a
prefix-less key never reaches SQL.

**All ADR-0034 deployment shapes store blobs in Postgres** (`blob` table, bytea,
`00021_blob.sql`). The Helm shape runs voice and web as separate Deployments with
no shared filesystem, but every shape already shares Postgres — so a Voice
Instance writes and a Web Instance serves with zero new configuration, in
compose, systemd, and Helm alike. A **32 MiB per-blob cap** (a code constant,
`blob.MaxSize`) enforced at `Put` keeps a bytea backend honest; anything larger
forces the backend conversation. **S3-compatible storage is the documented growth
path** behind the same seam — a local-filesystem backend is deliberately not
built (the Helm split can't reach it without RWX volumes). Deletion goes through
the seam, not FK cascade: a deterministic `Key()` plus `Delete` is the mechanism
owning rows use, which survives the backend swap; the owner-side wiring and
retention semantics are Epic 8 / ADR-0051.

---

## 7. Observability and spend

> ADR-0032 ([slog + thin Prometheus; tracing deferred](adr/0032-observability-slog-prometheus-deferred-tracing.md)) ·
> ADR-0044 ([retry policy and metric placement](adr/0044-provider-retry-policy-and-metric-placement.md)) ·
> ADR-0045 ([usage metering: event shape, labels, estimates](adr/0045-provider-usage-metering-estimates.md)) ·
> ADR-0046 ([spend meter: price map and cap mechanics](adr/0046-spend-meter-price-map-cap-mechanics.md))

**Logs.** Stdlib `log/slog`, one process-wide handler chosen by env (JSON in prod,
text in dev), installed with `slog.SetDefault` so third-party libraries route
through it too (`internal/observe/logging.go`). No third-party logging library.

**Metrics.** A deliberately small, hand-curated `prometheus/client_golang` surface
(`internal/observe/prometheus.go`) served on its **own** listener (`-metrics-addr`,
default `:9090`) alongside `/healthz` and `/readyz` — off the public API port, in
all three Modes. Labels are bounded: `component` and `provider`, **never**
`tenant_id`, never model, never session.

**Tracing.** None. v1's OTel apparatus produced spans that never crossed a process
boundary; it is deferred until the architecture actually spans processes.

**Metric semantics worth knowing** (ADR-0044): `ProviderCall`/`ProviderError` fire
once per *logical* call, after retries resolve — per-attempt detail is slog only.
`observe.CallOutcome` classifies `ok` / `timeout` / `error` / `canceled`; a
`canceled` outcome (a barge-in) counts the call but does **not** bump the error
counter — an interruption is not a vendor fault. `tts_total` is a *deliver* span
(synthesis plus paced playback of one sentence), not synthesis time, because the
lockstep tee paces the drain to Discord's 20 ms sender.

**Spend metering** (ADR-0045/0046). Usage rides an additive `EventUsage` stream
event, so old cassettes replay unchanged. Three counters —
`llm_tokens_total{provider,direction}`, `tts_characters_total{provider}`,
`stt_audio_seconds_total{provider}` — with documented, tested estimate fallbacks
that are never zero (`ceil(chars/4)` per direction; submitted TTS characters;
summed frame durations).

`internal/spend.Meter` implements `observe.UsageSink` and is teed into the
session's recorder at `session.Manager.Start` — **only when a cap is configured**;
with no caps there is no meter and no tee, byte-for-byte today's behavior. Prices
are code constants (`internal/spend/prices.go`), keyed `(component, provider,
model)`, every number labelled an estimate. Caps are per-Tenant nullable columns
(`00017_tenant_spend_caps.sql`), snapshotted at session start:

- **Soft cap** → publish `SpendCapReached{soft}`; the orchestrator `TurnGate`
  refuses *new* turns. In-flight turns complete; **transcription continues**.
- **Hard cap** → publish `SpendCapReached{hard}` and cancel the session context.
  The row closes with status `ended` (a deliberate policy stop, not a fault) and
  `end_reason` prefix `spend_cap_hard`.

---

## 8. Deployment

> ADR-0034 ([one image, Helm for k8s, systemd for self-host](adr/0034-deployment-artifacts.md)) ·
> ADR-0031 ([migration tooling](adr/0031-postgres-migration-tooling.md)) ·
> ADR-0033 ([CI/test strategy](adr/0033-ci-test-strategy.md))

**One OCI image**, `Dockerfile`, multi-stage, with `mode` selected by argument at
runtime — not one image per Mode. The runtime stage carries the binary, the
embedded migrations, the embedded Silero model, and the SPA assets. The Vite bundle
is **context-fed**, not built inside `docker build`: CI's `web` job produces it and
drops it into `internal/spa/dist`, exactly like the gitignored `gen/` proto stubs.
The committed placeholder `internal/spa/dist/index.html` keeps a fresh checkout
compiling with no node step, and image smoke checks fail when the embedded root is
still the placeholder.

**Kubernetes** deploys via the Helm chart in `deploy/charts/glyphoxa/`: separate
`web` and `voice` Deployments (`deploy/charts/glyphoxa/templates/web-deployment.yaml`,
`deploy/charts/glyphoxa/templates/voice-deployment.yaml`), a Postgres StatefulSet, and migrations as a
**pre-install/pre-upgrade hook Job** (`deploy/charts/glyphoxa/templates/migrate-job.yaml`) — not an
init-container on every replica. The advisory lock makes concurrent migration
*safe*; the hook makes it *run once and observably*.

**Self-host** — the v1.0 target — ships the full ADR-0034 on-ramp: the
`Dockerfile`, a root **`compose.yml`** (app + Postgres/pgvector) as the
zero-to-running path, and a **systemd unit** (`deploy/glyphoxa.service`) running
`glyphoxa -mode all` as a non-root user. Both lean on `all`-Mode **startup
auto-migrate** (ADR-0031, wired in `runWeb` via `autoMigrate`): the process
applies the embedded migrations under the advisory lock at boot, so
`docker compose up` / `systemctl start` reach the login screen against a migrated
database with **no manual `migrate up` step**. A web-only replica does *not*
auto-migrate — per ADR-0031 the serving Modes assume a current schema and fail
fast — so N replicas never race; only `all` Mode (and the Helm migrate hook) owns
the migration. Secrets come from the environment (`.env` for compose,
`EnvironmentFile=/etc/glyphoxa/env` for systemd) or the OS keyring, never baked
into the image.

CI (`.github/workflows/ci.yml`) gates every PR on `buf · proto · web · test ·
integration · audio · bench · image · helm · e2e · lint`. The `audio` and `lint`
jobs build the production tags (`opus dave nolibopusfile`), and `audio` also
vets the `live`/`bench`-tagged files so the scheduled live runs cannot rot
unseen. `release-image.yml` publishes an image only after the tagged commit's
`ci.yml` run is green and the built image passes `scripts/container-smoke.sh`. The default suite is **keyless**:
heavy tests are build-tag isolated and live provider runs are tiered (ADR-0033),
with cassette-based LLM determinism (ADR-0021, `pkg/voice/voicecassette`,
`tests/voice-cassettes`).

---

## Design-of-record, not shipped

These ADRs are accepted decisions with **no implementation in the tree**. They are
listed so nobody mistakes an ADR for a feature — and so the next implementer knows
the architecture question is already answered.

| ADR | Decision | State in the tree |
|-----|----------|-------------------|
| [0025](adr/0025-ensemble-turns-speculative-lead-reaction.md) | Ensemble Turns: speculative Lead + Cross-talk Reaction | Not implemented. Deferred by [0038](adr/0038-multi-npc-single-target-default-programmatic-roster.md); the unwired turn-taking code was removed on 2026-11-20 (the ADR records the last commit that had it). The live matcher is single-target (`MaxTargets` 1). |
| [0030](adr/0030-tool-side-effects-deferred-to-turn-commit.md) | Side-effecting Tool intents flush at turn-commit | Not built. `pkg/tool/loop.go` refuses Tools that are neither read-only nor proposal-mediated (ADR-0052). |

ADRs 0048–0053 were listed here until 2026-11; all six have shipped since
(`internal/blob`, `internal/jobs`, Speaker Lanes §2.6, `internal/tape` +
`internal/highlight`, Knowledge Proposals, `internal/bundle`).

## Where the tree and the ADRs disagree

Every entry below is the tree's word against an ADR's, collected so the
disagreement is documented rather than discovered.

| ADR says | The tree does | Why / where |
|----------|---------------|-------------|
| ADR-0005: `voice_sessions` claim table with `voice_instance_id` / heartbeat / `LISTEN/NOTIFY` | a separate, polled `voice_session_intents` claim plane | ADR-0057 superseded the mechanism (§1) |
| ADR-0015: `TenantService`, `glyphoxa.voice.v1.VoiceControlService` | six services, all `management.v1` | Tenants are provisioned by OAuth signup (ADR-0055); cross-process session control is the Postgres claim plane, not an RPC |
| ADR-0010: permissions from `tenant_members.role` | operator allowlist membership | `tenant_members` does not exist (ADR-0010 #102 amendment, ADR-0041) |
| ADR-0010: the Butler answers reasoning commands | no Slash Command reaches the Butler; it answers GM voice-address and the console planning chat | `/roll` runs the `dice` Tool directly (§3, §5) |
| ADR-0027: per-Agent barge-in confirm window | `bargeConfirmWindow` package const, 250 ms, armed per speaker | `internal/wirenpc/wirenpc.go` (§2.4) |
| ADR-0012/0027: `was_interrupted`, `interrupted_by_user_id` | neither field is persisted | `BargeDetected` carries the speaker; no column stores it (§2.3) |
| ADR-0023: TTS matrix is ElevenLabs + OpenAI | ElevenLabs only | OpenAI TTS adapter not built |
| ADR-0011: default embeddings via Ollama; ADR-0004 names more providers | `pkg/voice/embeddings/ollama` only | the only shipped embeddings adapter |
| ADR-0024: an ordered address chain | a 2-heuristic scoring stack (`NameMatch` + `LastAddressed`) | `DefaultHeuristicsWithin` (§2.2); the unfed heuristics were removed and ADR-0024 amended 2026-11-20 |

## See also

- [CONTEXT.md](../CONTEXT.md) — the domain glossary. Terms in bold here are defined there.
- [docs/README.md](README.md#architecture-decision-records) — the index of all 64 ADRs.
- [docs/configuration.md](configuration.md) — environment variables and the self-host setup runbook.
- [docs/agents/live-npc-run.md](agents/live-npc-run.md) — running a live NPC in `voice` Mode.
- [docs/devs/voice-tests.md](devs/voice-tests.md) — voice test conventions, cassettes, clips.
