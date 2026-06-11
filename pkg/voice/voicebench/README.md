# voicebench — voice-pipeline latency benchmark (Sprint 2, Epic C)

> **The cassette-tier number is the orchestration/plumbing regression floor —
> NOT the latency win.** Cassette replay is instant, so cassette-tier spans are
> ~0 ms; they exist to catch *our-code* regressions relatively, against a
> committed baseline. The real before/after for the Sprint-2 latency fixes
> (B1 sentence-streaming, B2 thinking-cap) is a **LIVE-tier measurement**,
> captured in the second live run with real vendors. Never read a cassette
> p50/p95 as "the latency we ship."

A thin consumer of [`voicetest.Harness`](../voicetest): it drives clips through
the real `orchestrator.Conversation`, reads the timestamped event log the
Harness already collects plus the A3 recorder taps, and reduces per-turn stage
spans to a distribution. It does **not** re-implement event observation
(ADR-0020).

## Two tiers (ADR-0033 addendum)

| Tier | Providers | Build | Runs | Asserts |
|---|---|---|---|---|
| **cassette** | real silero VAD + codec; STT/TTS/LLM from cassettes (no keys) | `-tags "bench opus"` (keyless-but-CGO) | the audio/CGO CI job | **regression-diff** vs `baseline.json` (`Report.CheckRegression`, default +25% p95) |
| **live** | real ElevenLabs + Gemini | `-tags "bench opus live"` | nightly cron + pre-second-live-run | **absolute** `EngineeringSLO` (≤1.2 s p50 / ≤2.5 s p95, `Report.CheckSLO`) |

The default no-CGO PR gate never compiles the `//go:build bench` files, so the
fast keyless gate is untouched.

## Stage vocabulary

Each `Stage` maps 1:1 onto a prod `glyphoxa_voice_<stage>_seconds` histogram (a
bench number == a Prometheus series). Boundaries are reconciled with
`internal/observe`'s A3 subscriber:

- `response_latency` (headline) = first `FirstOpus` per `TurnID` − `STTFinal.SpeechEndAt` — from the bus. Audible-on-wire boundary (task #7); the rig's drain sink publishes `FirstOpus` on each sentence's first chunk since the bench has no codec/sender. `FirstAudio` still feeds `tts_ttfb` and the lifecycle success outcome.
- `address_detect`, `llm_turn` — from bus event `At:` deltas.
- `llm_round`, `vad_hangover`, `stt_request`, `tts_ttfb/total`, `codec_*` —
  from the `recorderTap` (these are `observe.StageRecorder` calls, not bus
  events), so the bench taps the **real prod emit path** rather than
  re-deriving.

## Corpus

Reuses `tests/voice-clips/` audio (drives real VAD+codec) + `tests/voice-cassettes/`
for the network providers. `hello-test` is the only dice clip with a full
cassette set (`stt-hello-test` + `tts-hello-test` + `llm-tool-dice`) and is the
de-risk clip — the LLM cassette is prompt-hash keyed, so the reply assembly must
reproduce the recorded prompt exactly.
