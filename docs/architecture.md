# Glyphoxa architecture

The current-system overview. Every subsystem section names the ADR(s) that
govern it, under [docs/adr/](adr/). Domain vocabulary — **Mode**, **Operator**,
**GM**, **Bot**, **Guild**, **Voice Session**, **Butler**, **Character NPC**,
**Provider Config**, **BYOK**, **Slash Command**, **Knowledge Graph**,
**Transcript Line**, **Address Detection** — is defined once in
[CONTEXT.md](../CONTEXT.md) and used here exactly. The decisions ledger lives in
[DESIGN.md](../DESIGN.md); the self-host setup runbook is
[docs/configuration.md](configuration.md).

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
**The shipped default is `-mode voice`**, not `all` — see [Tree vs ADR](#tree-vs-adr) below.

| Mode | What the process is | What it runs |
|------|---------------------|--------------|
| `voice` | a **Voice Instance** | the Discord Bot + the voice pipeline for one Guild/channel passed as flags; no web API, no Slash Commands |
| `web` | a **Web Instance** | the Connect RPC API + the embedded SPA. Starts no Voice Session, opens no Discord gateway, and therefore **registers no Slash Commands** |
| `all` | both, in one process | Web Instance that also owns the standing Discord presence (Slash Commands) and drives the voice loop in-process |

**`all` is the only Mode with the whole product in it.** The standing presence and
the Slash Command surface are built inside `if withVoice` in
`cmd/glyphoxa/main.go`, so a `web`-only replica serves the console and nothing on
Discord. This is deliberate — the split-Mode scale path expects exactly one
Bot-owning process.

**Audio frames never cross a process boundary** (ADR-0005). The Voice Instance
opens the Discord voice WebSocket itself and keeps Opus/PCM inside its own
address space; v1's gRPC AudioBridge is gone. Control and telemetry over RPC are
unaffected (ADR-0014).

In `all` Mode the web handler starts and stops the loop through an in-process
`internal/session.Manager`, which holds the loop's cancel func — no loopback RPC
(ADR-0039). A `web`-only replica answers `GetSession` but rejects `StartSession`.

Two other subcommands sit beside the Modes: `glyphoxa migrate` (ADR-0031) and
`glyphoxa seed`.

### Tree vs ADR

- ADR-0005 names `all` the default Mode. The binary defaults `-mode` to `voice`
  (`cmd/glyphoxa/main.go`), a recorded MVP choice, not silent drift — the comment
  at the flag says so.
- ADR-0005 specifies a `voice_sessions(guild_id PK, voice_instance_id,
  claimed_at, heartbeat_at)` claim table plus `LISTEN/NOTIFY` handoff. **Not
  built.** The shipped `voice_sessions` table
  (`internal/storage/migrations/00006_voice_sessions.sql`,
  `00008_voice_session_end_reason.sql`) is a per-Campaign session record — `id`,
  `campaign_id`, `started_at`, `ended_at`, `status`, `line_count`, `end_reason` —
  with no instance-claiming columns. One Voice Instance per deployment is the v1.0
  shape; orphan rows left by a crash are swept at boot
  (`Manager.ReconcileOrphans`, ADR-0043's "rows are the source of truth" spirit).
- ADR-0031 says `all` Mode auto-applies migrations at startup. **Not wired.**
  `storage.MigrateUp` exists and is called only from the `migrate` subcommand
  (`cmd/glyphoxa/migrate.go`); every Mode assumes a current schema.

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
  → VAD (Silero, ONE shared session — see §2.6)    pkg/voice/vad/silero
  → STT (batch POST, or streaming websocket)       pkg/voice/stt, pkg/voice/stt/elevenlabs
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

- **Batch** (default) — one `/v1/speech-to-text` POST per endpointed utterance
  (`pkg/voice/stt/elevenlabs/transcribe.go`). Dominates response latency (~1.5 s).
- **Streaming** — ElevenLabs Scribe v2 Realtime over a persistent websocket
  (`pkg/voice/stt/elevenlabs/stream.go`, `pkg/voice/stt/streaming.go`), opt-in via
  `GLYPHOXA_STT_STREAMING`, with automatic fallback to the batch adapter when the
  stream is down. **Local VAD stays the endpointing authority**
  (`commit_strategy: "manual"`); provider-side endpointing was rejected because
  Barge-in needs a local, network-independent VAD anyway.

Streaming adds `STTPartial` to the taxonomy: mutable interim text of the
in-progress utterance. Only the committed `STTFinal` reaches Address Detection and
the Transcript.

### 2.2 Address Detection

> ADR-0024 ([deterministic fuzzy chain on raw STT](adr/0024-address-detection-deterministic-fuzzy-chain.md))

Runs on the **raw** `STTFinal` — no LLM transcript-correction pass in front of it,
which is what let v1 rewrite NPC names before the matcher saw them. The chain, per
utterance (`pkg/voice/address`):

1. explicit name/alias match (fuzzy: per-language phonetic encoder, then a
   Damerau-Levenshtein net; plus derived leading-consonant-truncation aliases,
   exact-only and utterance-initial),
2. last-speaker continuation,
3. single active non-Address-Only NPC fallback,
4. no target (still transcribed).

`Detect` returns a *set*. **`MaxTargets` defaults to 1**, so naming two NPCs in one
breath fires one turn on the top-scored Agent (ADR-0038). An **Address-Only** Agent
is reachable only through stage 1 and is excluded from stages 2 and 3. A muted Agent
stays in the fuzzy index and is still matched by name, but is likewise excluded from
every ambient heuristic — muting must never re-route an explicit address to a
different NPC.

No LLM in the loop: matching is a pure function over a mutex-guarded index/roster
snapshot, sub-millisecond, and fully unit-testable.

**Tree vs ADR.**

- ADR-0024 says "the Butler defaults `AddressOnly=true` … and responds to
  voice-address only from the GM." **The Butler cannot enter the Matcher by
  construction** — see §3. `AddressOnly` is a real matcher capability with zero
  in-loop users today, and no GM-vs-participant gate exists anywhere.
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

A sentence counts as **delivered** when its last Opus frame is forwarded to
Discord. At turn end — natural or barged — the Transcript utterance commits **only
delivered sentences**. Zero delivered ⇒ the utterance is not logged at all. This
is what keeps the Transcript equal to what the room actually heard, and it keeps
Address Detection and retrieval from ingesting words nobody heard.

**Tree vs ADR.** ADR-0012's per-utterance `was_interrupted` /
`interrupted_by_user_id` fields do not exist: `transcript_line`
(`00007_transcript_lines.sql`) carries no interruption columns, and
`BargeDetected` carries only the moment of the cut. Both wait on speaker
attribution (§2.6).

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
- It is measured over the **one shared VAD session**, not per participant (§2.6).
  A non-zero window is therefore load-bearing rather than cosmetic: at zero, the
  addressing speaker's own continued speech cancelled the turn it had just
  triggered. 250 ms is documented in-code as *the minimum* until per-participant
  VAD lands, not as a tuned sensitivity.
- Consequently the "any human may barge in, and you get `interrupted_by_user_id`
  for free" property is not realized.

A second window, `floorCoalesceWindow`, guards a different failure: one utterance
split by VAD into two STT segments would open two turns, the second superseding the
first mid-synthesis. A `Floor.Take` landing inside the window yields to the
in-flight turn instead.

### 2.5 Multi-NPC: one floor, one Cast, one turn

> ADR-0038 ([single-target default, programmatic roster](adr/0038-multi-npc-single-target-default-programmatic-roster.md)) ·
> ADR-0025 ([Ensemble Turns — **design-of-record, deferred**](adr/0025-ensemble-turns-speculative-lead-reaction.md))

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
queued follow-up) is the **design-of-record** and is *not implemented*. Its
multi-target decision set is reachable today by setting `MaxTargets > 1` or `-1`;
its turn-taking layer is not built. ADR-0025 is deferred, not superseded.

### 2.6 One shared VAD lane; no speaker attribution

This is the single most load-bearing gap between the ADRs and the tree, and several
sections above point back here.

Discord separates speakers on the wire, and the codec honours that: inbound Opus is
decoded with **one libopus decoder per speaker** (`pkg/voice/wire/codec` — a decoder
is stateful per Opus stream, so feeding two SSRCs into one produces garbage), and
`voice.Frame` carries a `UserID`. **That is where speaker identity stops.** The
decoded PCM feeds **one shared Silero VAD session**, so:

- Every voice event downstream of VAD is speaker-less. There is no `SpeakerID`
  field on any event in `pkg/voice/voiceevent`, and no **Speaker Lanes**.
- Human speech shares **one anonymous lane** in the Transcript: the relay renders
  every human as `player` (ADR-0039). Only NPC and Butler lines are named, from
  `AddressTarget.Name`.
- Barge-in cannot attribute the interrupter (§2.4), and its confirm window cannot
  distinguish the addressing speaker's own continued speech from a genuine
  interruption — hence the 250 ms floor.

ADR-0050 specifies the fix — N Speaker Lanes, `SpeakerID` added additively to
`STTPartial` / `STTFinal` / `VADSpeechStart` / `BargeDetected` — and is **not
implemented**. ADR-0051 (Rollover Tape) blocks on it, because per-speaker consent
exclusion is impossible without lanes. See
[Design-of-record, not shipped](#design-of-record-not-shipped).

### 2.7 Provider adapters that exist

| Component | Shipped adapter packages | Wired into the live loop |
|-----------|--------------------------|--------------------------|
| STT | `pkg/voice/stt`, `pkg/voice/stt/elevenlabs` (batch + streaming) | yes |
| TTS | `pkg/voice/tts/elevenlabs` | yes |
| LLM | `pkg/voice/llm/groq`, `pkg/voice/llm/openaicompat`, `pkg/voice/llm/gemini`, `pkg/voice/llm/anthropic` | `groq` (via `openaicompat`, ADR-0037) |
| Embeddings | `pkg/voice/embeddings/ollama` | yes (`internal/embedworker`, `internal/recall`) |
| VAD | `pkg/voice/vad/silero` | yes |

The MVP provider matrix is Groq (LLM) + ElevenLabs (STT + TTS) (ADR-0039).
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

**Tree vs ADR: the Butler is an undeletable DB row with no code path.** It is
auto-created by the `00002_auto_butler.sql` trigger with `address_only = true`
(a partial unique index forbids a second), it is editable in the console — Persona,
Voice, Tool Grants — and **nothing it holds drives anything**. Three facts, each
checkable:

- The live loop never loads it. `loadSeededNPCs` calls `storage.CharacterAgents`
  (`internal/wirenpc/agentspec.go`), Character NPCs only.
- **It cannot enter the Matcher by construction, not merely by omission.**
  Production builds the matcher at `internal/wirenpc/roster.go` via
  `address.NewMatcher`, and that file's single `address.Agent` derivation site,
  `matcherAgent`, hardcodes `AgentRole: "character"` for every Agent it registers.
  `address.NewWholeWordMatcher(butler, npcs)` is the only constructor that accepts
  a Butler — it panics unless `butler.AgentRole == "butler"` — and it has no
  non-test caller.
- No Slash Command reaches it either: `/roll` is answered by the Bot binary
  invoking the built-in `dice` Tool directly (§5), never by the Butler reasoning.

So the Butler exists in the schema, in the console, and in the ADRs. It does not
exist at runtime.

**Tool** is the domain term (`pkg/tool`). A Tool is `{name, input JSON schema,
handler}`; its *backing* — built-in Go function or an out-of-process MCP Server —
is an implementation detail behind that interface. The `Registry` is a map.
v1.0 ships **exactly one** built-in Tool, `dice` (`pkg/tool/dice.go`,
`pkg/tool/builtins.go`); the MCP Server adapter is not built.

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
Read-only Tools (`dice`) execute inline during generation. Side-effecting Tools
are, per ADR-0030, meant to record intent and flush at turn-commit — **that
machinery is not built**. `pkg/tool/loop.go` hard-refuses any Tool that is not
read-only. ADR-0052 subsequently narrows ADR-0030: `remember_knowledge` is to be
proposal-mediated rather than turn-committed. Neither exists in the tree yet.

---

## 4. The web tier

> ADR-0013 ([SPA: Vite + React 18](adr/0013-spa-vite-react-18.md)) ·
> ADR-0015 ([Buf Connect end-to-end](adr/0015-buf-connect-end-to-end.md)) ·
> ADR-0014 ([bus + SSE to the browser](adr/0014-grpc-bus-plus-sse.md)) ·
> ADR-0018 ([TanStack Router + Query + connect-query](adr/0018-tanstack-router-and-query.md)) ·
> ADR-0017 ([Radix + plain CSS tokens](adr/0017-radix-plus-plain-css-tokens.md)) ·
> ADR-0039 ([single-operator web tier](adr/0039-mvp-ui-backend-single-operator-web-tier.md)) ·
> ADR-0041 ([operator allowlist](adr/0041-operator-allowlist-access-policy.md))

### 4.1 Shape

A Vite + React 18 SPA (`web/`) built to `internal/spa/dist` and served from
`embed.FS` by the same binary (`internal/spa`), so the single-binary deployment
shape survives. Four screens exist on disk (`web/src/screens/`):

| Screen | Purpose |
|--------|---------|
| `login` | Discord OAuth entry |
| `configuration` | Provider Configs (BYOK), Discord settings, bot-authorization link, spend caps |
| `campaign` | Campaigns, Agents, Tool Grants, Knowledge Graph Nodes/Edges |
| `session` | Start/Stop the Voice Session, live Transcript feed, NPC roster + mute |

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

Both session reads are wrapped in `auth.RequireSession`. Connect server-streaming
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

There is exactly **one Bot** identity (one token) for the whole deployment. Its
gateway client is boot-owned (`internal/presence`) and shared with the voice
Manager — never a second connection per Voice Session. Commands register per-Guild,
idempotently, and survive with no Voice Session active.

Shipped commands (`internal/presence`):

| Command | Who | Handled by |
|---------|-----|------------|
| `/roll <dice>` | anyone in the configured Guild | the Bot binary, directly, via the built-in `dice` Tool |
| `/glyphoxa use <campaign>` | GM only | sets the durable Active Campaign |
| `/glyphoxa start` / `end` | GM only | the same in-process `session.Manager` the Session screen drives |
| `/glyphoxa search <query>` | GM only | tsvector search over the Active Campaign's Transcript Lines |
| `/glyphoxa mute <npc>` / `muteall` | GM only | matcher-internal mute state |

**Tree vs ADR.** ADR-0010's `/say <text> as:<agent>` is **not registered**;
`mute`/`muteall` postdate the ADR. ADR-0010 says the Butler handles a command "when
reasoning is required" — no command reaches the Butler today; `/roll` is answered by
the Bot binary running the built-in `dice` Tool directly (§3). Per the ADR's #102
amendment, "GM only" in v1.0 means *the invoking Discord User's snowflake is on the
operator allowlist* — `tenant_members.role` does not exist yet. Discord's own command
permissions are a UX hint; the server-side check is the only safe place.

Commands are registered only by a process that owns the Bot gateway, i.e. `all`
Mode (§1). `-mode web` exposes none.

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

`internal/recall` speculates retrieval over `STTPartial` and, on a matching
`STTFinal`, injects the prefetched chunks at zero added latency; on mismatch it
runs inline under a hard ~250 ms budget and degrades to no-memory (ADR-0042).

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
ADR-0005 chose, no new infrastructure. Applied with `glyphoxa migrate up`.

### 6.5 Credentials

Provider Configs are Tenant-scoped and encrypted at rest with AES-GCM
(`internal/storage/crypto`, keyed by `$GLYPHOXA_SECRET`) — **BYOK** (ADR-0004).
The secret is write-only; only `credentials_last4` is ever read back.

Credential resolution is a documented **hybrid** (ADR-0039): a saved key wins,
discriminated by `credentials_last4 != "env"` (the placeholder `seed` writes);
otherwise the adapter's own ENV variable is used. DB-as-sole-source is the stated
end-state. Without `$GLYPHOXA_SECRET` the web tier still boots and reads — only
*saving* a key fails, loudly (`CodeFailedPrecondition`).

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

**Self-host** — the v1.0 target — is a systemd unit running `glyphoxa -mode all`.
Secrets come from the environment or the OS keyring, never baked into the image.

CI (`.github/workflows/ci.yml`) gates every PR on `buf · proto · web · test ·
integration · audio · image · helm · e2e · lint`. The default suite is **keyless**:
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
| [0025](adr/0025-ensemble-turns-speculative-lead-reaction.md) | Ensemble Turns: speculative Lead + Cross-talk Reaction | Deferred by [0038](adr/0038-multi-npc-single-target-default-programmatic-roster.md). `MaxTargets` reaches the multi-target set; the turn-taking layer is not built. |
| [0030](adr/0030-tool-side-effects-deferred-to-turn-commit.md) | Side-effecting Tool intents flush at turn-commit | Not built. `pkg/tool/loop.go` hard-refuses non-read-only Tools. |
| [0048](adr/0048-blob-storage-seam-postgres-v1.md) | a blob-storage seam package, Postgres bytea backend | No blob package exists; nothing binary is persisted. |
| [0049](adr/0049-background-job-runner.md) | One DB-backed job runner over a job table | No job table exists. `internal/embedworker` stays a bespoke poll loop, as the ADR allows. |
| [0050](adr/0050-per-speaker-utterance-segmentation.md) | N Speaker Lanes; `SpeakerID` on voice events | No `SpeakerID` anywhere. Human speech is still one anonymous lane (§2.6). |
| [0051](adr/0051-rollover-tape-consent-retention.md) | Consent-gated 120 s Rollover Tape; Highlights | No audio is persisted. Blocks on 0048 + 0050. |
| [0052](adr/0052-kg-write-proposals.md) | Agent KG writes land as GM-reviewed Knowledge Proposals | No proposal table, no `remember_knowledge` Tool. |
| [0053](adr/0053-campaign-bundle-format.md) | Campaign Bundle: versioned gzipped-JSON export | No bundle package exists; there is no export or import path. |

## Where the tree and the ADRs disagree

Every entry below is the tree's word against an ADR's, collected so the
disagreement is documented rather than discovered.

| ADR says | The tree does | Why / where |
|----------|---------------|-------------|
| ADR-0005: default Mode is `all` | `-mode` defaults to `voice` | recorded MVP choice; migrates with #6 (`cmd/glyphoxa/main.go`) |
| ADR-0005: `voice_sessions` claim table with `voice_instance_id` / heartbeat / `LISTEN/NOTIFY` | a per-Campaign session record, no claiming | one Voice Instance in the v1.0 self-host shape; orphans swept at boot |
| ADR-0031: `all` Mode auto-migrates at startup | only `glyphoxa migrate up` migrates | `storage.MigrateUp` exists but is unwired from the Mode entrypoints |
| ADR-0015: `TenantService`, `glyphoxa.voice.v1.VoiceControlService` | five services, all `management.v1` | multi-tenant deferred (ADR-0039); `all` Mode drives sessions in-process |
| ADR-0010: `/say <text> as:<agent>` | not registered | deferred; `/glyphoxa mute` + `muteall` shipped instead (#211) |
| ADR-0010: permissions from `tenant_members.role` | operator allowlist membership | `tenant_members` does not exist (ADR-0010 #102 amendment, ADR-0041) |
| ADR-0010/0024: the Butler answers reasoning commands and voice-address | an undeletable DB row with no code path; it cannot enter the Matcher by construction | `matcherAgent` hardcodes `AgentRole: "character"`; `NewWholeWordMatcher` has no non-test caller (§2.2, §3, §5) |
| ADR-0019/0027: per-participant VAD sessions | one shared VAD session over the decoded stream | speaker identity stops at the codec (§2.6) |
| ADR-0027: per-Agent barge-in confirm window | `bargeConfirmWindow` package const, 250 ms, one per Conversation | `internal/wirenpc/wirenpc.go`; a floor, not a sensitivity (§2.4) |
| ADR-0012/0027: `was_interrupted`, `interrupted_by_user_id` | neither field exists | blocks on speaker attribution (§2.3, §2.6) |
| ADR-0023: TTS matrix is ElevenLabs + OpenAI | ElevenLabs only | OpenAI TTS adapter not built |
| ADR-0011: default embeddings via Ollama; ADR-0004 names more providers | `pkg/voice/embeddings/ollama` only | the only shipped embeddings adapter |

## See also

- [CONTEXT.md](../CONTEXT.md) — the domain glossary. Terms in bold here are defined there.
- [DESIGN.md](../DESIGN.md) — the full ADR ledger and open questions.
- [docs/adr/](adr/) — all 53 ADRs.
- [docs/configuration.md](configuration.md) — environment variables and the self-host setup runbook.
- [docs/agents/live-npc-run.md](agents/live-npc-run.md) — running a live NPC in `voice` Mode.
- [docs/devs/voice-tests.md](devs/voice-tests.md) — voice test conventions, cassettes, clips.
