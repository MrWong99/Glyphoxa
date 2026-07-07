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
