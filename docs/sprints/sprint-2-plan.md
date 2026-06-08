# Glyphoxa v2 — Sprint 2 Plan

**Status:** goals locked, ready to execute. **Date:** 2026-06-08.
**Inputs:** the Sprint 1 retro + two planning analyses in
[`sprint2-planning/`](./sprint2-planning/) (`retro.md`, `latency.md`,
`observability.md`), produced by the `glyphoxa-s2-plan` agent team and
cross-aligned on metric vocabulary and SLOs.

## Sprint goal

**Make Bart fast and make the system legible.** Turn the latency complaint from
anecdote into a measured, regression-guarded number and bring it under SLO; and
replace the misleading log stream with disciplined logs + a small Prometheus
surface, so an operator can read a run's health at a glance.

One-line success test: a second live run where (a) Bart's speech-end→first-audio
is visibly tighter, and (b) the console shows lifecycle Info lines, not a wall of
benign DAVE/codec errors — with a `/metrics` endpoint and a benchmark backing
both claims.

## Why (from Sprint 1)

Sprint 1's MVP works (live two-way voice, barge-in, dice — merge `6aa3649`/PR #14).
The two flagged rough edges share **one root**: ADR-0032 observability is specced
but unbuilt. With no turn-latency metric we can't quantify "sometimes late," and
with no severity discipline the logs scream on a healthy call (this caused a real
mid-test misdiagnosis). Sprint 2 builds that foundation, then uses it to fix the
latency.

---

## Epics & backlog

Ordered by execution sequence. Each item is one reviewable unit of work →
candidate GitHub issue. **AC = acceptance criteria.**

### Epic A — Observability foundation *(build first: you can't fix what you can't see)*

- **A1 · Logging cleanup + tame the disgo noise.** Re-level the two benign noise
  families and take ownership of the library logger.
  - `wire.go:147` "skipping undecodable inbound frame" and `:152` "feed frame":
    `Warn` → **`Debug`** (per-frame events), each bumping a counter (A2).
  - Own disgo's logger: `bot.WithLogger(...)` at `wirenpc.go:169` **and**
    `slog.SetDefault(...)` in `main` (today neither is set — confirmed, so disgo
    logs uncontrolled via the stdlib default).
  - Add an app-owned filtering `slog.Handler` (`internal/observe`) that
    **downgrades to Debug + rate-limits** only the known-benign disgo
    `error while reading packet` / `DAVE decrypt` message (content match, not a
    blanket floor on `name=voice` — real gateway errors must still surface at
    Error), incrementing `glyphoxa_dave_decrypt_errors_total`.
  - `wirenpc.go:357/369` reply-failure: promote to **`Error`** when the user got
    no reply.
  - **AC:** a healthy live call prints lifecycle Info + a handful of lines, zero
    per-packet Warn/Error; a forced gateway error still logs at Error.

- **A2 · Prometheus surface (ADR-0032).** Extend the existing
  `pkg/voice/observability.go` `MetricsRecorder` (don't fork) + one Prometheus
  adapter; orchestrator sibling recorder driven off the `voiceevent` bus (out of
  the hot path). Namespace `glyphoxa_`.
  - Core SLO metric: `glyphoxa_voice_response_latency_seconds` (histogram,
    `vad.speech_end` → first `AudioChunk` to `PlaybackPump`) + discrete per-stage
    histograms `glyphoxa_voice_{vad_hangover,stt_request,address_detect,llm_round,
    llm_turn,tts_ttfb,tts_total,codec_decode,codec_encode}_seconds` (reconciled
    `latency`↔`observability` vocabulary — prod metric == bench number). Base unit
    `_seconds` (Prometheus convention); benchmarks may print ms.
  - Provider health: `glyphoxa_provider_calls_total{stage,provider,outcome}`,
    `glyphoxa_provider_errors_total{stage,provider}`.
  - Voice plumbing: `glyphoxa_voice_sessions` gauge,
    `glyphoxa_inbound_frames_dropped_total`,
    `glyphoxa_inbound_undecodable_frames_total` (pairs A1/L1),
    `glyphoxa_dave_decrypt_errors_total` (pairs A1/L5),
    `glyphoxa_playback_total{interrupted}`, `glyphoxa_barge_cancels_total`.
  - Spec-complete stub: `glyphoxa_embedding_backlog` gauge (lands with the
    embedding layer; listed so the surface matches ADR-0032).
  - **Cardinality (ADR-0032):** never label `tenant_id`/`campaign_id`/`guild`/
    `turn_id`; use the bounded `agent_role` (butler|character) as the substitute;
    `turn_id` is a log/exemplar correlation id only. `guild` opt-in label on
    self-host only.
  - **AC:** `/metrics` scrapes clean in `voice` mode (metrics-only listener) and
    `web`/`all` mode (mounted on the existing server); buckets sized to the SLOs.

- **A3 · Turn instrumentation + correlation.** Stamp a `turn_id` at `stt.final`
  and propagate it through `address.routed → reply → tts.invoked → first-audio`.
  Most stage deltas are derivable today from existing bus `At:` timestamps; add
  the **two** missing hooks:
  1. **first-audio-out** — stamp when the first `AudioChunk` hits
     `PlaybackPump.HandleSentence` (the `TeeSynthesizer`→pump boundary). Closes
     the headline SLO.
  2. **per-LLM-round span** — inside `agenttool.providerAdapter.Generate`, one
     span per `Provider.Complete` with `round_index`, `had_tool_call` (separates
     "thinking time" H1 from "extra rounds" H2). Optional finer `first_token_ms`.
  - **AC:** every turn produces a correlated set of stage spans feeding A2's
    histograms.

- **A4 · slog hygiene (ADR-0032 debt).** Mode-selected handler in
  `cmd/glyphoxa/main.go:20` (JSON prod / text dev) replacing the hardcoded
  `TextHandler/Info`; introduce a `ctxLogger(ctx)` helper carrying
  turn/session-scoped fields; collapse the three ad-hoc discard loggers onto the
  existing `discardLogger()` helper.

### Epic B — Latency fixes *(the user-visible win; measured against A's baseline)*

- **B1 · Sentence-stream the reply to TTS. ⭐ highest leverage.** Today the
  streamed Gemini reply is drained into one `AssistantMessage` and dispatched as a
  **single** `Reply`, so first audio waits for the *entire* completion — even
  though `PlaybackPump` is already built for per-sentence incremental speech.
  Segment the streamed text into sentences and dispatch each as it completes.
  - **AC:** first audio begins after the *first* sentence, not the full
    completion; `voice_response_latency_ms` p50 drops materially in the benchmark.

- **B2 · Cap Gemini "thinking" wall-time. ⭐ primary variance fix.**
  `gemini/complete.go` sends only `max_tokens`; 2.5-flash thinking is dynamic and
  **time-unbounded** by default → input-dependent multi-second stalls (best match
  for "manchmal"). Send `reasoning_effort`/`thinkingBudget`; A/B the *distribution*
  (not the mean) low vs. default.
  - **AC:** `voice_llm_round_ms` p95 tightens with no answer-quality regression on
    the dice/RP corpus; the chosen budget is recorded in the Gemini adapter + an
    ADR note.

- **B3 · Tune VAD end-of-speech hangover.** `minSilenceFrames` 15→8 (480→256 ms),
  a fixed cost on every turn. Validate against false speech-end / clipped tails on
  the clip corpus. Cheap one-liner win.

- **B4 · (stretch) HTTP transport reuse (H3).** Shared tuned
  `http.Client`/`Transport` with keep-alive for Gemini + ElevenLabs so the
  first turn after silence doesn't pay a fresh TLS handshake. Do only if A3+C
  show the cold-connection penalty is real.

> H2 (tool-loop extra rounds) and H4 (provider jitter) are **measure-then-decide**
> — the benchmark quantifies them before we spend code on them.

### Epic C — Benchmark harness *(makes latency a guarded number, not anecdote)*

- **C1 · Harness on `voicetest.Harness` + `voicecassette`.** Reads the **same**
  bus timestamps the A2 metrics use (a bench number == a Prometheus series). Two
  tiers per ADR-0033: **cassette/keyless** (every PR, sub-second, catches *our*
  orchestration regressions) and **live `-tags=live`** (nightly cron, real
  vendors, measures H1/H2/H3). Corpus under `tests/voice-clips/`: trivial / dice /
  reasoning-bait. Report **p50/p95/p99** over N≥30 replays (the tail is the point).
- **C2 · CI wiring + SLO assertion.** Emit a JSON artifact (stage → p50/p95)
  asserted against the SLO budgets below; default tier flags orchestration
  regressions, live tier feeds vendor SLOs. Benchmarks never gate unrelated PRs.

### SLO targets (shared by A2 alerts and C2 assertions — one source of truth)

| SLO | Boundary | p50 | p95 |
|---|---|---|---|
| **Engineering** (prod dashboards/alerts) | `vad.speech_end` → first audio to pump | ≤ 1.2 s | ≤ 2.5 s |
| **User-perceived** (clip benchmark) | true end-of-speech → audible in Discord | ≤ 1.7 s | ≤ 3.0 s |

(User-perceived = engineering + ~480 ms VAD hangover + ~40–150 ms Discord tail.
Targets assume B1+B2 landed; A/C first establish the *current* baseline.)

### Epic D — Process *(fixes the Sprint 1 friction)*

- **D1 · "Done = committed + green."** Lead verifies a teammate's branch actually
  has the commits (and CI is green) before integrating; a self-reported "done"
  over an untracked tree is **not done**. (Recurred 4+ times in Sprint 1.)
- **D2 · Single-owner shared surfaces.** Declare sole ownership of
  `internal/wirenpc` and `pkg/voice/wire` up front (they collided 3× last sprint).
- **D3 · Close residual Gemini live-unknowns** during the next live run: confirm
  streamed tool-call-id correlation and pin the working thinking budget (B2); fold
  findings into the adapter + ADRs.

---

## Recommended execution order

1. **A1** (immediate operator relief, independent) +
2. **A2/A3** (stand up metrics + hooks → baseline measurement) →
3. **C1** (benchmark harness on the same taps → capture *before* p50/p95) →
4. **B1 → B2 → B3** (the fixes, each re-measured against baseline) →
5. **C2 + A4** (CI assertion, slog hygiene) → **second live run** (D3) to confirm.

A4 and D run throughout. B4/H2 are stretch, gated on what C measures.

## Out of scope / carry-overs

Web/control-plane (#6); Gemini key rotation (deferred by Luk); transcript-residual
scrub; OTel tracing (ADR-0032 defers). Not Sprint 2.

## Next step (per Luk's flow)

Goals are now written. **Restart the agent team fresh** (clear context) and
execute Sprint 2 against this plan — A first, then B measured against C's baseline.
