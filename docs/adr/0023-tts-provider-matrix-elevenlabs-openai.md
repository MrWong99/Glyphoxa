# TTS provider matrix v1.0: ElevenLabs + OpenAI (amends ADR-0004)

The v1.0 TTS row of the BYOK provider matrix is **ElevenLabs + OpenAI**,
amending ADR-0004 which originally paired ElevenLabs with Coqui XTTS.

**Why Coqui is dropped:**

Coqui AI shut down in December 2025. The model and codebase live on as the
community-maintained `coqui-tts` Idiap fork on PyPI, but:

- No commercial backing; community release cadence is quarterly with limited
  major-model improvements.
- XTTS-v2 sits mid-tier in 2026 (~910 ELO on Artificial Analysis vs 1,160+
  for modern managed APIs); modern emotional/prosody nuance is dated.
- No inline emotion markup at all — emotion comes only from a `speaker_wav`
  reference clip, which forces an awkward UX (per-emotion clip libraries
  per NPC) and produces flatter results than current managed alternatives.
- PyTorch 2.6 compatibility is fragile (custom `XttsConfig` blocked by
  `weights_only=True`); ongoing maintenance burden.

**Why OpenAI gpt-4o-mini-tts:**

- Steerable per-call delivery via the natural-language `instructions`
  parameter — fits Glyphoxa's per-NPC Persona model cleanly (instructions
  go in `Voice.Settings`, optionally per-call via `OverrideSettings`).
- 50+ languages, 13 preset voices, streaming output, sub-second latency.
- HTTP-only API; no extra runtime dependency (unlike Coqui's PyTorch + ONNX
  + Python interop).
- BYOK fits — Tenant supplies its own OpenAI key.

**Capability matrix in MVP:**

| Capability             | ElevenLabs | OpenAI |
|------------------------|------------|--------|
| `Synthesizer`          | ✓          | ✓      |
| `VoiceLister`          | ✓          | ✓ (preset list) |
| `VoiceCloner`          | ✓          | ✗ (no public clone API) |
| `VoiceDesigner`        | ✓          | ✗ (no design API) |
| `DialogueSynthesizer`  | ✓ (`eleven_v3`) | ✗ (no dialogue API) |

OpenAI satisfying only the core `Synthesizer` + `VoiceLister` is fine — that's
exactly the capability-interface pattern from ADR-0022 working as intended.
The web UI gates clone/design/dialogue features on the chosen Voice's
provider supporting them.

**Out of scope (deferred):**

- Self-hosted local TTS option — Coqui filled this slot in ADR-0004; no
  replacement chosen for v1.0. Candidates if/when revisited: Piper, Kokoro,
  community Cartesia self-host.
- A third managed provider (Cartesia, Google Chirp 3 HD, Azure HD) —
  capability interfaces make adding one straightforward when product demand
  emerges.

**Considered options:**

- **Keep Coqui as-is** — rejected. Mid-tier quality, no inline emotion, dead
  upstream company; the v1.0 self-hosted slot is better empty than filled
  with a degrading dependency.
- **Replace Coqui with Cartesia** — Cartesia is technically excellent
  (sub-90 ms TTFA, 60+ emotional tones, mixed SSML+brackets) but is also
  managed/BYOK, duplicating ElevenLabs's posture. OpenAI brings a
  differently-shaped markup model (out-of-band `instructions`) that
  exercises ADR-0022's `OverrideSettings` design more meaningfully.
- **Replace Coqui with self-hosted Piper** — Piper is light and local but
  CPU-only quality is a regression vs Coqui XTTS. Defer until self-hosted
  is a product priority.
