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

## Amendment: ElevenLabs default model is `eleven_v4_turbo`, over HTTP (2026-11-22, #702)

**Decision.** The ElevenLabs adapter's default model moves from `eleven_v3` to
**`eleven_v4_turbo`**, on the existing HTTP
`POST /v1/text-to-speech/{voice}/stream` path. A WebSocket transport is **not**
required for the win; a warm Text-to-Dialogue WebSocket is an optional later
step. Model selection stays in one place (`elevenlabs.DefaultModel` /
`elevenlabs.ModelOf`), so a transport switch keys off the same seam.

**Measurements** (live API from the dev box, voice "George", German NPC lines,
2026-11-22; first audio = request sent → first PCM chunk):

| Path | Model | First audio |
|---|---|---|
| HTTP `/stream`, warm connection | `eleven_v4_turbo` | median 138 ms (127–141, n=5); probe 133–142 ms (n=16) |
| HTTP `/stream`, cold (TCP+TLS included) | `eleven_v4_turbo` | 184–230 ms |
| HTTP `/stream`, warm connection | `eleven_v3` | median 553 ms (490–797, n=5); probe 575–677 ms |
| HTTP `/stream` | `eleven_v4` (non-turbo) | ~640 ms — unsuitable for live voice |
| Text-to-Dialogue WS, warm socket | `eleven_v4_turbo` | ~102–104 ms |
| Text-to-Dialogue WS, dialled per turn | `eleven_v4_turbo` | ~210 ms — slower than warm HTTP |

- **Cost:** the `character-cost` header shows ~0.22 credits/char for
  `eleven_v4_turbo` (and flash) against ~0.44 for `eleven_v3`, `eleven_v4` and
  `eleven_multilingual_v2`. The ADR-0046 price map prices TTS per
  (provider, model) since this amendment, and the Usage Ledger records the model.
- **Markup:** v4 Turbo renders the v3 bracketed audio-tag vocabulary
  (`[laughs]`, `[whispers]`, `[pause]` verified), but silently strips SSML
  `<break/>` (even with `enable_ssml_parsing`). The tag prompt is therefore
  gated by model family (v3/v4 get tags; flash / multilingual v2 / turbo v2.5
  get "plain prose only") and teaches pauses as `[pause]` tags, never SSML.
- **Incompatibilities:** `optimize_streaming_latency` is rejected (HTTP 400) on
  v4 Turbo; v3/v4 models are rejected on the plain TTS WebSocket
  (`/stream-input`), only Text-to-Dialogue WS accepts them.
- **Connection reuse:** a barge closes the response mid-stream and with it the
  HTTP/1.1 connection, so the next reply's first sentence paid ~180 ms instead
  of ~140 ms. The adapter's pool keeps 4 idle connections per host (look-ahead
  runs two syntheses at once) and the live wiring pre-warms with a HEAD request
  (`Client.Warm`) on connect, on each speech start and after each barge
  (~150 ms first audio after a barge + warm). A 45 s idle connection was still
  reused.

**Data.** Every persisted voice recorded `"model_id":"eleven_v3"` explicitly (no
UI ever picked a model), so migration `00059` moves ElevenLabs agent voices on
`eleven_v3` to `eleven_v4_turbo` (Down reverses it), and Campaign Bundle import
applies the same move to voices of bundles exported before the switch. A voice
explicitly on any other model is left alone.

**Later, optional:** a pooled Text-to-Dialogue WebSocket per live NPC voice
(~35 ms more, at the cost of a dialogue-concurrency slot per open socket, a 20 s
keep-alive and a redial per barge); HTTP/2 (measured equal warm first audio and
keeps the connection across a barge).
