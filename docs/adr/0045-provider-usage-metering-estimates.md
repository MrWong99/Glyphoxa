# Provider usage metering: event shape, labels, and estimate fallbacks

Implementing #127 (E6, under the ADR-0004 amendment and ADR-0042) required deciding how usage figures travel from provider adapters to counters, what the Prometheus label shape is, and what happens when a provider reports no usage. The operator delegated these decisions to the implementation run (2026-07-07); this ADR records them.

## What this decides

- **Usage rides a new additive `EventUsage` stream event** (`llm.StreamEvent` gains a `Usage{InputTokens, OutputTokens}` field), not a mutated `EventDone`. Old cassettes never contain the event, so they replay unchanged (ADR-0021); consumers already drain streams to close.
- **StageRecorder gains three usage methods** — `LLMTokens(provider, model, in, out)`, `TTSCharacters(provider, chars)`, `STTAudioSeconds(provider, d)`. The `model` parameter exists for the spend meter (ADR-0046 prices per model); **Prometheus drops it** — model is never a label. New series: `glyphoxa_voice_llm_tokens_total{provider, direction=input|output}` (direction is required: Groq prices directions differently), `glyphoxa_voice_tts_characters_total{provider}`, `glyphoxa_voice_stt_audio_seconds_total{provider}`. Bounded labels per ADR-0032.
- **Estimate fallbacks (documented, tested, never zero):** LLM without reported usage → `ceil(chars/4)` per direction over the sent/received text; TTS → `utf8.RuneCountInString(sentence)` on accepted request (ElevenLabs bills submitted characters; counted even if later barged); STT → summed frame durations — batch: submitted audio length; streaming: voiced+pre-roll duration accumulated per utterance, recorded at end-utterance regardless of commit outcome (audio sent is audio billed; a batch fallback after a failed commit truthfully double-records, since both calls billed).
- **`include_usage` is preset-gated in openaicompat**: on for Groq and OpenAI presets, off for Gemini until verified — a gateway that rejects `stream_options` would 400 every turn. The trailing usage chunk arrives with empty `choices`; capture happens before the empty-choices skip guard.
- **Capture is inline at the call sites** (atomic counter adds, nanoseconds); no deferral machinery. Usage capture never blocks or fails a turn. Error/barge paths record provider-reported usage if received, otherwise nothing.

## Considered and rejected

- **Usage on `EventDone`** — mutating an existing event's semantics risks ordering assumptions in consumers and cassette drift.
- **Model as a Prometheus label** — unbounded-ish cardinality for no dashboard need; the spend meter is the only consumer of model granularity.
- **Deferred/batched usage recording** — complexity without measurable hot-path win.

## Relationship to other ADRs

ADR-0004 (amendment 2026-07-04 defines the metering mandate), ADR-0042 (STT duration cost), ADR-0032 (label bounds), ADR-0021 (additive event, cassette stability), ADR-0046 (spend meter consumes these capture points).
