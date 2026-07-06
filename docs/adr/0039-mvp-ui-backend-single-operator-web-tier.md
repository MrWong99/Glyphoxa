# MVP UI ↔ backend: single-operator self-host web tier

The MVP UI ships as a Claude Design handoff — three screens (Configuration, Campaign, Session) over a hardcoded shell — but the backend it talks to does not exist yet. `proto/` holds only `.gitkeep`; `buf.gen.yaml` carries the `protocolbuffers/go` + `grpc/go` plugins but **no** connect-go / protoc-gen-es / protoc-gen-connect-es; there is no `web/` SPA; and `cmd/glyphoxa` runs only the `voice` node plus `migrate`/`seed` — the `all`/`web` Mode and any `/api/v1` server are the deferred "task #6". What *does* exist is the in-process voice pipeline (orchestrator, Groq/ElevenLabs adapters, the `voiceevent` taxonomy + Bus), the AES-GCM `crypto.Cipher` (`Seal`/`Open`/`Last4`), and most of the schema (`tenant`/`campaign`/`agents`/`provider_config`/`transcript_chunk`). This ADR records the decisions that scope the **first integration increment**: the smallest web tier that makes the three designed screens drive the real voice pipeline, framed as the design is — single-operator self-host — without contradicting the multi-tenant ADRs it will later grow into.

## What this decides

- **Single-tenant fast-path auth, ADR-0016-shaped.** Discord OAuth + an opaque `glyphoxa_session` cookie (HttpOnly, Secure, SameSite=Lax) gate the app; one Tenant is auto-seeded and bound to the first operator. The `X-Tenant-Id` interceptor and `/t/:slug` route prefix are kept as **thin pass-throughs** so the multi-tenant surface fills in later without a rewrite. Deferred: `tenant_members` roles, the tenant switcher, `onboarding/create-tenant`, and the `api_keys` human-login field (ADR-0016 already drops the latter from the human page). The design has no login screen and hardcodes the user; this builds exactly the gate a self-host operator needs and nothing the screens don't show.

- **Hybrid credential source; DB-as-source is the end-state.** "Start session" uses the decrypted `provider_config` key when a real one is saved — discriminated by `credentials_last4 != "env"`, the placeholder the `seed` writes — and falls back to the adapter's ENV key otherwise. The serve path builds a `crypto.Cipher` from `$GLYPHOXA_SECRET`, keeps the `LoadAgent`-joined configs (the read half already exists), `Open`s the ciphertext, and injects per-Component keys into `buildConversation`, replacing today's hardcoded `groq.New("")` / `stteleven.New("")` / `ttseleven.New("")` (`wirenpc.go:438/580/595/608`). ENV stays the `-hardcoded` dev/CI path, untouched. Saving a key in Configuration becomes authoritative the moment it lands; DB-as-sole-source is the stated end-state.

- **Anonymous human lane in the transcript; per-participant attribution stays deferred.** NPC and Butler lines are named — we own that output and it carries `AddressTarget.Name` (ADR-0024). All human speech shares one "Player / DM" lane, because raw `STTFinal` carries no speaker and per-participant VAD is deferred (ADR-0019). Lines coalesce per turn (`TurnEnded`), matching the history commit and the mock's finished-line shape. The `kind ∈ {gm, player, npc, butler}` taxonomy the mock renders is derived in the relay. Named human speakers land later behind a `SpeakerID` on `STTFinal` — an additive field, not a relay rewrite.

- **`all` Mode drives sessions in-process; the control RPC is defined, not looped through.** The web handler starts/stops the voice loop via a direct in-process `SessionManager` that holds the loop's cancel func — no loopback RPC, no multi-replica backplane. The `voice.v1 VoiceControlService` proto (`claim_session` / `release_session` / `push_event`) is authored now so the split-Mode path and the SSE relay share one set of event names, but its gRPC transport is deferred until a deploy slice needs more than one replica. Honors ADR-0005 (no audio across process boundaries) and ADR-0014 (the Bus is dual-impl: in-proc channels for `all` Mode, gRPC for split).

- **Health and connection badges render instantly, upgrade async.** The Configuration "Healthy / Key needed" badge and the bot-connected tag render immediately from presence of a `provider_config` row / a saved token, then an async test-call (ElevenLabs `/v1/voices` is already wired; a Groq ping) and a real Discord gateway login upgrade or downgrade the badge and resolve the live bot tag (`Glyphoxa#4823` in the mock). No live call blocks page load.

- **MVP provider matrix = Groq (LLM) + ElevenLabs (STT + TTS) only.** Matches the design's "more providers soon" and the only adapters built. The Configuration voice dropdown is live data from ElevenLabs `ListVoices` (already implemented); ~~Groq exposes no list-models call, so its model select is a static allowlist~~ (superseded — see the 2026-07-06 amendment: the claim was factually wrong). Preview-voice wraps the existing `Synthesize`. OpenAI TTS (ADR-0023) and Gemini (ADR-0035) stay out of this increment.

## Amendment: live Groq model catalog + free-text model entry (2026-07-06, #227)

The premise "Groq exposes no list-models call" was factually wrong: Groq's
OpenAI-compatible surface serves `GET /models` (the health check's
`livePingGroq` was already calling it). The static allowlist it justified went
stale — deprecated ids lingered, new models never appeared — and the hardcoded
`groq.Models` var is deleted.

Decided instead:

- **Live catalog.** `ListModels` fetches the catalog through the tenant's
  decrypted key (the same hybrid credential policy as the health check),
  unfiltered, with `groq.DefaultModel` pinned first. A fetch failure degrades
  to just the default and never errors: the Configuration screen must stay
  usable without a catalog.
- **Free-text model entry.** The Configuration model control is a combobox
  that accepts any typed model id, catalog-listed or not — curation is exactly
  the staleness this amendment removes. A typed model saves via a model-only
  `SaveProviderConfig` (empty secret + existing rows), which re-upserts the
  sealed key verbatim; the secret stays write-only (ADR-0004).
- **End-to-end threading.** `provider_config.model` now reaches the engine:
  Agent-bound LLM config first, tenant-level row as fallback, empty meaning
  "adapter default" (resolved in the openaicompat adapter, never duplicated
  upstream).

## Why

The decisions are one realization seen from several sides: **the MVP UI is one self-host operator's console, so the cheapest correct increment is a single-operator gate over the single-binary pipeline that already exists.** Every choice keeps the multi-tenant / SaaS ADRs *reachable* rather than *contradicted* — the tenant interceptor is a pass-through, not absent; the credential source is a hybrid that names DB-as-source as the destination; the anonymous lane is an additive `SpeakerID` away from named humans; the in-proc `SessionManager` sits behind the same proto the split path will dial.

The credential hybrid is the load-bearing integration: without it, the Configuration screen's whole save/mask/Replace flow is theatre, because the voice loop reads ENV. Keying the fork on the `last4 == "env"` placeholder the seed already writes means the UI becomes authoritative with zero migration and dev/CI keep working. The anonymous human lane ships the live transcript *now* against the one thing STT cannot yet give us, instead of blocking the Session screen on the deferred attribution work. And driving sessions in-process avoids building a backplane the self-host shape will not exercise, while writing the control proto keeps the event-name contract identical across `all` and split Modes.

The whole increment is built **test-first, slice by slice** (ADR-0019): each vertical slice lands with the tests that prove it before the next stacks on it.

## Considered options

- **Full multi-tenant ADR-0016/0018 now** — rejected for this increment: builds `tenant_members` / onboarding / switcher UI the three MVP screens do not render. Deferred behind the thin `X-Tenant-Id` / `/t/:slug` pass-throughs, not dropped.
- **No auth at all** — rejected: taking the missing login literally leaves every RPC open and forces an auth retrofit before any shared use. The single-operator Discord-OAuth gate is small and ADR-0016-shaped.
- **DB-only credentials from day one** — rejected as the *transition*: forces every local/CI run through a seeded encrypted key. Adopted as the stated *end-state* once the UI is the only writer.
- **Keep ENV authoritative** — rejected: saving a key in Configuration would have no effect on a session, contradicting the UI's intent.
- **Attribute human speakers by Discord SSRC now** — deferred: pulls speaker attribution ahead of ADR-0019's per-participant VAD. The anonymous lane ships the transcript; the named-human path slots in behind an additive `SpeakerID`.
- **Loop `all` Mode through `VoiceControlService`** — deferred: loopback RPC overhead plus a multi-replica backplane the self-host shape does not need. The proto is written so the split path is cheap to add.

## Relationship to other ADRs

- **ADR-0002 / 0003 / 0016 / 0018 (tenancy & auth)** — this narrows their multi-tenant surface to a single-operator fast-path for the increment; the `X-Tenant-Id` interceptor and `/t/:slug` stay as pass-throughs so they fill in without a rewrite. Discord-only OAuth and server-side sessions are honored; the human-login `api_keys` field stays dropped (ADR-0016).
- **ADR-0004 (BYOK matrix)** — the hybrid credential source consumes the encrypted `provider_config` this defines; `crypto.Seal`/`Open`/`Last4` already exist.
- **ADR-0005 / 0034 (single-binary Modes, deployment)** — adds the `all`/`web` Mode the deployment ADRs assume; the in-proc `SessionManager` respects "no audio across process boundaries."
- **ADR-0009 (single Agent table / auto-Butler)** — CampaignService CRUD persists the polymorphic `agents`; the one-Butler trigger (`00002_auto_butler.sql`) stays the invariant. Two additive columns (`title`, speaker-color slot) back the editor.
- **ADR-0011 (transcript chunks)** — the last-session summary and reconnect replay read persisted transcript; whether the per-line Session view needs a finer `transcript_line` table than the 3–6-utterance chunk grain is settled in the implementing slice.
- **ADR-0013 / 0015 / 0017 / 0018 (web stack)** — this is the first build of the Vite + React SPA, the Connect end-to-end surface, and the codegen toolchain those ADRs decided but that `buf.gen.yaml` and `web/` do not yet contain.
- **ADR-0014 / 0020 (SSE + event taxonomy)** — the SSE relay is the unbuilt Hop-B; `voiceevent` already produces the dot-namespaced names it forwards.
- **ADR-0019 (orchestrator-first TDD; deferred per-participant VAD)** — the increment is built test-first, and the anonymous human lane is the visible consequence of that deferral.
- **ADR-0024 (Address Detection)** — supplies `AddressTarget.Name`, the only reliable speaker attribution the transcript has in this increment.
