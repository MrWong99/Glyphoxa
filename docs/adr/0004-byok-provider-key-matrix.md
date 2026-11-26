# BYOK provider keys with two-providers-per-component matrix

Provider keys are BYOK (Bring-Your-Own-Keys), Tenant-scoped, encrypted with a single app-level secret env var (AES-GCM). Operator-pooled keys are a future-additive path. Keys are write-only after save (last 4 visible). No spend caps in MVP. No per-campaign overrides.

Opinionated 2-providers-per-Component matrix:

- LLM: Anthropic + Ollama
- STT: Deepgram + whisper.cpp
- TTS: ElevenLabs + Coqui XTTS
- Embeddings: OpenAI + Ollama
- S2S: deferred

Tenant admins paste a key, pick a model from the provider's list-models endpoint, and validate with a test-call button.

## Amendment: per-session spend caps (2026-07-04, #130 — reverses "No spend caps in MVP")

- **Unit: estimated currency.** Spend is estimated from metered usage (LLM tokens, TTS characters, STT audio seconds — streaming STT per ADR-0042 makes STT a duration cost that MUST be metered) via a static price map for the MVP provider matrix. Unknown models use a conservative documented default and log a warning; every surfaced number is labelled an *estimate*, never billing truth.
- **Scope: per Voice Session**, accumulated in-memory at the same capture points as the Prometheus usage counters (which stay session-unlabelled per ADR-0032 cardinality bounds). Per-tenant monthly budgets are a later, separate layer.
- **Two independently opt-in thresholds, configured per Tenant by the Operator:** a **soft cap** — crossing it stops new Agent turns (the in-flight Turn completes; human speech keeps being transcribed) and surfaces a spend-cap-reached state on the Session screen — and a **hard cap** — crossing it ends the Voice Session itself (clean stop), the guard for the unattended-weekend case where streaming STT would otherwise keep billing indefinitely behind a soft block. Either may be set alone; both set requires `hard ≥ soft`. Neither set = today's uncapped behavior (self-host default).

## Amendment: `image` Component via Gemini (2026-07-07, #311)

The `provider_component` enum gains **`image`** (enum migration). v1 matrix entry: **Gemini** is the sole image-generation adapter — the provider already exists in the matrix for LLM, so the operator manages no new vendor/key family. The Component gets its own BYOK Configuration slot + health check, and ADR-0046 price-map entries (per-image estimates). Scope is image-only; video generation is explicitly out of v1. ElevenLabs sound generation (deferred, #312) will ride the existing `tts` Provider Config with a distinct usage kind rather than a new Component.

## Amendment: whisper.cpp retired; quality bar for future local/drop-in providers (2026-07-16)

**whisper.cpp is out.** It was under consideration as the second STT provider
(local, key-free) since v1, and the build plumbing (Makefile `whisper-libs`,
Dockerfile static-lib stage, goreleaser flags) existed ahead of any Go binding —
nothing ever linked it. Evaluation concluded its performance is too poor for
Glyphoxa's live loop: transcription latency on self-host-class hardware is far
from the ~1.7 s STT-bound turn latency the hosted providers deliver, and it
would have re-added a heavy CGO dependency just as the codec and DAVE went pure
Go (ADR-0006/0033/0034 amendments). The vestigial build steps are removed with
this amendment.

**Quality bar for any future replacement:** a drop-in TTS or STT alternative
(local or hosted) may be evaluated later **only if its actual audio quality is
roughly comparable to the ElevenLabs API** — that is the reference bar for both
synthesis quality (TTS) and transcription fidelity on live Discord speech
(STT). Candidates below that bar are not worth the integration surface,
whatever their cost or locality advantages. If a local provider is adopted, it
must run out-of-process (sidecar/server mode) rather than as an in-process CGO
binding, to preserve the pure-Go binary (see the ONNX exit, #468).

The matrix's second-provider slots (STT: whisper.cpp, TTS: Coqui XTTS — also
never implemented) are accordingly **vacant by policy**, not TODO: Deepgram and
ElevenLabs remain the implemented reference providers, alongside the
OpenAI-compatible surfaces already in the codebase.

---

**Amendment (2026-07-22, #312 — sound generation rides the `tts` Provider
Config, deliberately NOT a Component):** Highlight sound enrichment (ElevenLabs
sound-effects "sting" and Music endpoints) resolves the Tenant's existing `tts`
Provider Config and uses its key **iff the configured provider is ElevenLabs**;
any other tts provider (or none) makes sound generation cleanly not-configured
(no-op, no retry, no spend — the `ErrImageNotConfigured` pattern from #311).
Unlike `image` (a new vendor, hence a new Component), sound generation is the
same vendor and the same key as tts — a `sound` Component would force Tenants
to paste one key twice for pure attribution's sake. Attribution is preserved in
the Usage Ledger instead: sound calls meter under their own model ids (SFX and
Music separately) with their own ADR-0046 price-map entries. Revisit the
Component question only if a non-ElevenLabs sound provider is ever adopted.

## Amendment: the LLM matrix as built is Groq + Anthropic (2026-11-22, #702)

The matrix at the top names "LLM: Anthropic + Ollama". What shipped first was
Groq alone (ADR-0036, ADR-0039), Ollama as a voice LLM was never built, and
Anthropic had an adapter but no place to save a key. As of #702 the
Tenant-facing LLM matrix is **Groq + Anthropic**: two write-only credential
slots, each its own `provider_config` row `(tenant, llm, provider)`.

- **A choice, not a pair.** A Tenant runs one LLM provider at a time: the one
  its most recently saved `llm` row names (the read every engine already did).
  Saving a key or a model into a slot therefore puts the Tenant on it, and the
  Configuration screen can switch back to the other slot without re-entering
  its key. With no row at all the Tenant runs the deployment default (ADR-0036,
  amendment 2026-11-22).
- **Platform Keys cover both slots.** A Tenant entitled to the Platform Keys
  (ADR-0054) may choose a slot it never saved a key for, when the deployment
  holds that provider's key: the choice is stored as a keyless row carrying the
  `env` placeholder. It grants nothing new — every key resolution still
  consults the entitlement at call time.
  On that key the Tenant may run only models the price table has a row for
  (ADR-0036, amendment 2026-11-22): an unpriced id is refused, because the flat
  fallback rate it would be metered at is wrong in both directions.
- **Gemini stays the `image` slot** on the Configuration screen. As an LLM it is
  reachable as a deployment default only, not as a third Tenant slot: nothing
  measured it for voice.
- **Models come from the provider.** Each slot lists its provider's live
  catalog (Groq `GET /models`, Anthropic `GET /v1/models`). With the Tenant's
  own key the whole catalog is listed and free text is accepted; on the
  Platform Key the list is cut down to priced models. A hardcoded list is only
  ever the fallback when the listing fails or there is no key to list with, and
  only of models the price table knows.

## Amendment: the planning chat is a choice over the LLM matrix, not a second key slot (2026-11-22)

ADR-0062 (amendment of the same date) is the record; what changes here:

- **The planning-chat entry is a choice (Groq or Anthropic), not a credential
  slot.** A Tenant has at most one `chat_llm` Provider Config, kept by the
  single writer; no row means the chat follows the voice LLM.
- **Keys are reused, not pasted twice.** A chat row that stores no key of its
  own (`credentials_last4 = "env"`) runs on the Tenant's key in that
  provider's LLM slot when there is one, else on the Platform Key within the
  Tenant's plan. A key for the chat alone stays possible and is stored on the
  chat row only; saving it never changes the voice LLM.
- **`DeleteProviderConfig`** (slot `chat` only, for now) is the "back to same
  as voice" action and removes a chat-only key with the row. It is the natural
  home of a key-delete for the other slots later. A chat row holding an
  unpriced model whose borrowed key disappears does not run on the Platform
  Key: the engine checks the rule again on every exchange.
- **Priced models only on the Platform Key** applies to the chat choice too,
  when the choice is saved and again when the chat runs.

## Amendment: removing a key, the way back to the platform default, and the usage summary (2026-11-26, #712 items 2 and 3)

Setup became an ordered checklist with a plan and usage card (the
`setup-checklist` package of the UX programme). What changes here:

- **A voice LLM key can be removed.** `DeleteProviderConfig` also accepts
  slot `groq` and slot `anthropic` (that slot's row, a saved key or a keyless
  platform choice alike) and slot `voice_llm` (both rows: "Back to platform
  default"). The Tenant then runs its newest remaining LLM row, else the
  deployment default; `effective_llm` follows on the next read. The removal
  busts the provider-health cache. ElevenLabs and Gemini keys keep Replace
  only: removing them would unbind every Agent's voice Provider Config (`ON
  DELETE SET NULL`), which is a decision of its own.
- **The planning chat is re-validated on removal.** A chat choice may name an
  unpriced model only because it ran on the Tenant's own key for that
  provider. When that key goes and the chat would fall to the Platform Key,
  the chat's model is reset to its provider's chat default (priced by test),
  and the response says so (`chat_model_reset`) so the screen can tell the GM.
  A chat-only key, or a priced model, is left alone.
- **Activating a platform slot is insert-only (#712 item 3).** The keyless
  "ride the Platform Key" row is written with `ON CONFLICT DO NOTHING`: a key
  saved between the handler's read and its write is never replaced by an empty
  credential. A key save always activates its slot, so the concurrent save
  already did what the activation asked for.
- **The linked server is stored by name.** The guild-admin proof already reads
  `GET /guilds/{id}`; the name it returns is stored with the link
  (`deployment_config.guild_name`, migration 00061), kept while the same
  server is re-linked, cleared when the link changes or is released, and
  returned as `guild_name` on `ListProviderConfigs`. Display data only: nothing
  routes on it.
- **`GetUsageSummary`** reports the plan (slug, display name), whether the
  Tenant rides the Platform Keys (the same entitlement every key resolution
  consults) and, where the allowance gate applies (open Admission Mode, a plan
  with `included_usage_usd`), the month-to-date estimate, the allowance and
  the reset instant (the next UTC calendar month, the window of
  `spend.PlanAllowance`). Allowlist mode wires no allowance, so none is
  reported: Setup never shows a meter that nothing enforces.

The explicit active-LLM marker (#712 item 1, saving a key no longer switching
the voice model) stays open: it needs a schema change and its own decision.
