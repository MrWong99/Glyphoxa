# Provider retry policy and provider-call metric placement

Implementing #124/#125 (E6) required deciding where retries live, what is retryable, how retries interact with the per-turn deadline and barge-in, and how retry attempts appear in metrics. The operator delegated these decisions to the implementation run (2026-07-07); this ADR records them.

## What this decides

- **Retry lives in the orchestrator stages, not the provider adapters.** A small generic helper `pkg/voice/retry` (`Do[T]` + `Retryable`) wraps the three call sites: `orchestrator.STT.Transcribe`, `orchestrator.TTS.Dispatch`, and the LLM start in `agenttool`/`agent.providerEngine`. Adapters stay dumb and only gain *typed* errors: `pkg/voice/providererr.HTTPError{Op, StatusCode, Status, Body}` with error text byte-identical to today's, so classification is `errors.As`, never string matching.
- **Policy defaults:** 3 attempts total, full-jitter exponential backoff 250ms→1s, injected `Sleep`/`Rand` (cassette determinism, ADR-0021 — tests never sleep wall-clock). Retryable: HTTP 429 and ≥500, `net.Error`. Non-retryable: other 4xx, context errors, untyped prose errors (under-retry is the safe default).
- **The deadline is never extended.** Before each backoff the helper gives up if the remaining context deadline is smaller than the pause; a context cancellation (barge-in, ADR-0027) aborts immediately — including mid-backoff — and returns the context error, not a provider failure.
- **Start-errors only.** Mid-stream failures (LLM deltas, TTS audio chunks) are not retried: output may already have been dispatched and a retry would re-speak. All three adapters surface 429/5xx as start errors anyway.
- **The existing STT 15s request timeout becomes the *total* retry budget**, wrapping the whole retry loop — never per-attempt. A per-attempt budget (3×15s) would recreate the serial-transcription-worker wedge (#91-class NPC-goes-silent).
- **Metrics record final outcomes only.** `ProviderCall`/`ProviderError` (wired by #125 with a shared `observe.CallOutcome(ctx, err)` classifier: ok / timeout / error) fire once per logical call after retries resolve; per-attempt detail is slog only. Stage histograms span the whole logical call including backoffs. A barge mid-TTS-stream is not a provider error. `TTSInvoked` publishes once per Dispatch, never per attempt.
- **#125 scope note:** `stt_request` was already wired; its RESERVED help text was stale. The slice wires the remaining five reserved histograms (tts_total, codec_decode, codec_encode, vad_hangover, llm_turn) and drops all RESERVED markers. Codec encode timing covers only the encode section, never the synthesis-channel wait.

## Considered and rejected

- **Retry inside each provider adapter** — duplicates policy three times and hides attempts from the orchestrator's deadline logic.
- **Per-attempt Prometheus series** — cardinality noise for a debugging concern; slog covers it (ADR-0032).
- **Retrying mid-stream failures** — re-speak risk; rejected outright for MVP.

## Relationship to other ADRs

ADR-0019/0026 (stages own resilience, adapters stay thin), ADR-0021 (injected time), ADR-0027 (barge-in cancellation contract), ADR-0032 (bounded labels, final-outcome-only series), ADR-0004 (provider interfaces unchanged).
