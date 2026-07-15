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
