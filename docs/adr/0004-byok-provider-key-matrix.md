# BYOK provider keys with two-providers-per-component matrix

Provider keys are BYOK (Bring-Your-Own-Keys), Tenant-scoped, encrypted with a single app-level secret env var (AES-GCM). Operator-pooled keys are a future-additive path. Keys are write-only after save (last 4 visible). No spend caps in MVP. No per-campaign overrides.

Opinionated 2-providers-per-Component matrix:

- LLM: Anthropic + Ollama
- STT: Deepgram + whisper.cpp
- TTS: ElevenLabs + Coqui XTTS
- Embeddings: OpenAI + Ollama
- S2S: deferred

Tenant admins paste a key, pick a model from the provider's list-models endpoint, and validate with a test-call button.
