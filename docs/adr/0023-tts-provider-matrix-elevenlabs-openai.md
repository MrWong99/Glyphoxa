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
| `DialogueSynthesizer`  | ✓ (v3/v4 models; `eleven_v4_turbo` default since the 2026-11-22 amendment) | ✗ (no dialogue API) |

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
  runs two syntheses at once, and a warm can overlap them — three, where the
  stdlib default keeps two) and the live wiring pre-warms with a HEAD request
  (`Client.Warm`) on connect and after each barge, and on a speech start only
  when the pool has been quiet for 20 s (no warm and no sentence streamed in
  that window). The HEAD round trip (~130–145 ms on a reused connection) is
  longer than the dial it saves (~55–80 ms), so an unthrottled warm per
  utterance start could hold the idle connection the reply's first sentence
  needs, and would send ~2000 anonymous HEADs per 3 h session. ~150 ms first
  audio after a barge + warm. A 45 s idle connection was still reused.

**Data.** Every persisted voice recorded `"model_id":"eleven_v3"` explicitly (no
UI ever picked a model), so migration `00059` moves ElevenLabs agent voices on
`eleven_v3` to `eleven_v4_turbo` (Down reverses it), and Campaign Bundle import
applies the same move to voices of bundles exported before the switch. A voice
explicitly on any other model is left alone.

**Later, optional:** a pooled Text-to-Dialogue WebSocket per live NPC voice
(~35 ms more, at the cost of a dialogue-concurrency slot per open socket, a 20 s
keep-alive and a redial per barge — since built as an opt-in, see the next
amendment); HTTP/2 (measured equal warm first audio and
keeps the connection across a barge).

## Amendment: optional WebSocket transport for the v3/v4 families; HTTP stays the default (2026-11-22, #702)

The "later, optional" pooled Text-to-Dialogue WebSocket is built, as an
**opt-in** so it can be compared in the live pipeline against the HTTP path:
`GLYPHOXA_TTS_TRANSPORT=ws` (default `http`, whose requests are byte-identical to
before). The interface consequences are in the ADR-0022 amendment of the same
date.

**Scope rules.**

- Only voices on the `eleven_v3*` / `eleven_v4*` model families use the socket.
  The dialogue socket refuses every other model (handshake 403) and the plain
  TTS socket refuses v3/v4 (handshake 400), so a voice on flash / multilingual
  v2 / turbo v2.5 silently stays on HTTP.
- A socket is bound at its handshake and first message to one voice, model,
  output format, language, seed and voice settings; the client pools sockets by
  exactly that. At most **2 sockets per voice** (the look-ahead pair: sentence
  k+1 synthesizes while k is still being read, and a socket is strictly
  sequential) and **8 per client** (one client per connect cycle of a Voice
  Session). Past the per-voice cap, or at the total cap with no idle socket to
  close, the sentence goes over HTTP rather than waiting.
- An open socket holds one **dialogue-session slot** of the plan behind the key
  (Creator 35, Pro 70) for its whole life, so sockets are short-lived: closed
  after 60 s idle, kept alive with `keep_alive` every 10 s (the server's limit is
  a fixed 20 s; sent during a sentence too, since playback back-pressure can
  stretch one read past it), and all closed when the cycle ends.
- There is no cancel message. A barge closes the cut sentence's socket (its
  unread audio must never reach the next sentence) and a replacement is dialled
  in the background.
- `Warm` cannot pre-dial for a voice that has not spoken yet (the socket is
  per voice), so a session's first sentence dials. Afterwards a warm lines up, or
  keeps alive, an idle socket for the two most recently used voices.
- Metering is unchanged: submitted characters are counted client-side on an
  accepted start, priced by model. The socket reports no character cost at all
  (HTTP returns a `character-cost` header), so there is nothing to cross-check
  against.

**Measured** (2026-11-22, real API from the dev workstation, `eleven_v4_turbo`,
`pcm_48000`, ten German NPC lines of 70–80 characters, through the real client;
first audio chunk in ms):

| | n | median | min | p90 | max |
|---|---|---|---|---|---|
| HTTP, warm connection | 10 | 143 | 123 | 147 | 161 |
| WS, warm socket | 10 | 99 | 94 | 102 | 103 |
| WS, fresh dial per sentence | 10 | 203 | 174 | 211 | 245 |
| look-ahead pair over HTTP: sentence k | 5 | 134 | 126 | 219 | 219 |
| look-ahead pair over HTTP: sentence k+1 | 5 | 134 | 128 | 162 | 162 |
| look-ahead pair over WS: sentence k | 5 | 99 | 98 | 108 | 108 |
| look-ahead pair over WS: sentence k+1 | 5 | 104 | 98 | 108 | 108 |
| HTTP: first sentence 600 ms after a barge | 5 | 160 | 134 | 211 | 211 |
| WS: first sentence 600 ms after a barge | 5 | 106 | 91 | 313 | 313 |

A 24 s sentence read at playback pace over the socket completed without error.

**Decision: HTTP stays the default.** A warm socket is ~45 ms earlier per
sentence and tighter (p90 102 vs 147 ms), and that holds for the look-ahead pair
and after a barge. It is not enough to pay for what it brings:

- 45 ms is under a twentieth of a turn's response latency (STT final + LLM first
  sentence + TTS), below what a table hears.
- A socket that has to dial is 60 ms *slower* than HTTP, and the first sentence
  of every session, every voice idle for a minute and every voice past the pool
  caps dials or falls back.
- It holds plan-capped dialogue-session slots: two per speaking voice for up to
  a minute. On the shared platform key that is a fleet-wide ceiling HTTP does
  not have (HTTP counts only requests in flight).
- It is a stateful pool with its own failure modes (dead pooled sockets, the
  20 s server timeout, redial per barge) where HTTP is one stateless request.

The switch is worth revisiting only if the in-pipeline numbers
(`glyphoxa_voice_tts_ttfb_seconds`, compared across deploy windows — it has no
transport label) show the gain surviving on the production network path **and**
the dialogue-session cap of the platform key's plan fits the fleet's speaking
voices, or if a WS-only model (`eleven_v3_conversational`) becomes the wanted
voice. Until then `ws` is a comparison arm, not a recommendation.

