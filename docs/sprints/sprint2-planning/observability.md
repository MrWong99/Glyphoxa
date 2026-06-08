# Sprint 2 — Observability Plan (logging, monitoring, benchmarking)

Scope: design only. Implements ADR-0032 (slog + thin Prometheus, tracing
deferred) and fits ADR-0033's CI tiers. No production code is changed by this
document.

The through-line: **the Sprint-1 live test was a fully working call, yet the
logs screamed.** Two log families fired continuously on benign frames and made
the operator think the bot was broken. The fix is not just "silence them" — it
is to *move the information from the log stream into metrics*, where a benign
trickle is invisible but a real spike trips an alert. Every reclassified log
site in §1 has a matching counter in §2 so nothing is actually lost.

Evidence: `~/claude_workspace/glyphoxa-s2-evidence-sprint1-livelog.log` (40
lines, two noise families during a healthy ~6-minute call).

---

## 1. LOGGING

### 1.1 The two noise families and why they're benign

**Family A — our code.** `pkg/voice/wire/wire.go:147`

```
level=WARN msg="skipping undecodable inbound frame" user=… err="codec: decode Opus for user …: opus: corrupted stream"
```

Fires from `Pipeline.Run` when `codec.DecodeInbound` returns a non-fatal error.
During a normal call this happens on transitional/partial Opus packets (notably
around the DAVE/MLS key handshake and on speaker SSRC changes) — the code
comment at `wire.go:144-146` already says as much. It is correctly *handled*
(skip + keep listening); it is just logged at the wrong level. Note the format
is our slog text handler (`time=… level=WARN`), so this one is ours to fix
directly.

**Family B — the disgo library.** Evidence lines 4-40:

```
2026/06/08 23:41:05 ERROR error while reading packet name=bot name=voice name=voice_conn err="failed to DAVE decrypt packet: failed to decrypt frame"
```

This is **not our code**. It originates in disgo:
`voice/audio_receiver.go:91` logs `s.logger.Error("error while reading packet", …)`
wrapping the decrypt failure raised at `voice/udp_conn.go:389`
(`failed to DAVE decrypt packet: %w`). The `name=bot name=voice name=voice_conn`
attribute chain is disgo tagging its own logger (`bot/config.go:63` adds
`name=bot`, the voice layer adds the rest).

Why it's benign here: DAVE/MLS is a ratcheting group cipher. When a participant
joins/leaves or an epoch rolls, the bot momentarily receives frames it cannot
yet decrypt (it doesn't have that sender's current epoch key). disgo logs each
such packet at ERROR. On a working call this is a steady background trickle, not
a fault.

### 1.2 Root cause of the disgo noise: nobody set the default logger

disgo's bot config defaults `Logger: slog.Default()` (`bot/config.go:19`), and
**`disgo.New` is called at `internal/wirenpc/wirenpc.go:169` without
`bot.WithLogger(...)`.** Confirmed: `grep -rn 'slog.SetDefault'` over the repo
returns **nothing** — the app never replaces the process default logger. So
disgo logs through Go's stdlib default text handler (which is exactly why the
evidence shows the `2026/06/08 23:41:05 ERROR …` stdlib-`log` prefix instead of
our `time=… level=…` slog format). The library's logging is entirely
uncontrolled by us today. That is the handle we pull in §1.5.

### 1.3 Leveling / structure convention (the rule the fixes follow)

Adopt one repo-wide convention, enforced in review:

| Level | Meaning | Examples |
|---|---|---|
| `Error` | A turn/session **failed** and the user is affected; needs a human eventually. | session open failed, provider returned 5xx after retries, panic recovered |
| `Warn`  | Degraded but self-healed; recurring would matter. | provider retry succeeded, frame buffer drop, DAVE *expected-but-unavailable build* (`manager.go:155`) |
| `Info`  | Lifecycle landmarks, one per turn/session at most. | session joined/left, NPC loaded, turn committed |
| `Debug` | Per-frame / per-packet detail; off in prod. | undecodable frame skip, per-frame feed error, VAD transitions |

Rules:
- **No per-frame or per-packet event is above `Debug`.** Audio runs at 50
  packets/s/speaker; anything routine at that rate at Warn/Error is noise by
  construction.
- **Structured fields, never interpolation.** Carry `tenant_id`,
  `campaign_id`, `voice_session_id`, `guild_id`, `turn_id` on a
  `*slog.Logger` in `context.Context` (ADR-0032), not as ad-hoc args. This is
  not yet done — the codebase threads bare `*slog.Logger` (e.g.
  `buildConversation(log, …)` `wirenpc.go:309`); Sprint 2 introduces a
  `ctxLogger(ctx)` helper and the request/turn-scoped `With(...)`.
- **Handler chosen by mode** (ADR-0032: JSON in prod, text in dev). Today
  `cmd/glyphoxa/main.go:20` hardcodes `NewTextHandler(os.Stderr, Level:Info)` —
  fix site (see §1.6).

### 1.4 Specific log sites to reclassify/fix

| # | Site | Now | Change |
|---|---|---|---|
| L1 | `wire.go:147` "skipping undecodable inbound frame" | `Warn`, every packet | → **`Debug`** + bump counter `glyphoxa_inbound_undecodable_frames_total` (§2). Keep `user` only at Debug. |
| L2 | `wire.go:152` "feed frame" | `Warn`, per frame | → **`Debug`** (per-frame). A *sustained* feed failure is a real bug — catch it via the same undecodable/drop counters + a "no STT in N s while frames flowing" alert, not per-line Warn. |
| L3 | `wire.go:121` "flush on shutdown" | `Warn` | Keep `Warn` (once per session, genuinely worth seeing). |
| L4 | `manager.go:155` DAVE-expected-but-unavailable | `Warn` | Keep — fires **once** at startup, is a real misconfig signal (ADR-0006: close code 4017). |
| L5 | disgo `voice_conn` "error while reading packet" / DAVE decrypt | lib `Error`, every undecryptable packet | **Route + downgrade + count** — see §1.5. |
| L6 | `wirenpc.go:357` "agent reply failed", `:369` "reply dispatch failed" | `Warn` | Promote to **`Error`** when it means the user got no reply (a failed turn is user-visible); keep `Warn` only if a fallback reply was delivered. Tie to `glyphoxa_provider_errors_total`. |

### 1.5 Taming the disgo library noise (L5)

Two layers, do both:

1. **Take ownership of disgo's logger.** Pass `bot.WithLogger(libLogger)` at
   `disgo.New` (`wirenpc.go:169`) and call `slog.SetDefault(libLogger)` once in
   `main` so *any* library on the default logger is covered, not just disgo's
   bot logger. (disgo otherwise silently keeps `slog.Default()`.)

2. **Wrap with a filtering `slog.Handler`** that the app owns. A small
   `internal/observe` (or `pkg/voice/loghandler`) `slog.Handler` decorator that,
   before delegating to the JSON/text handler, inspects the record:
   - If `msg == "error while reading packet"` **and** the record carries the
     `name=voice_conn` group/attr **and** the wrapped err contains
     `"DAVE decrypt"` → **downgrade to Debug** and **rate-limit** (e.g. log at
     most 1 line / 10 s per guild, with a `suppressed=N` field), and increment
     `glyphoxa_dave_decrypt_errors_total{guild}`.
   - Everything else from disgo passes through unchanged at its original level.

   This is deliberately a *content* filter, not a blanket level floor on
   `name=voice` — a genuine voice-gateway error (4006/4014/close codes, UDP
   failures) must still surface at Error. We only quiet the one known-benign,
   high-frequency message.

   Matching on message text is brittle if disgo renames the string; the
   rate-limit + counter is the durable safety net, and the live cassette/smoke
   run (ADR-0033) will re-confirm the filter still bites after a disgo bump.

**Net effect:** the operator's console during a healthy call goes from ~40 noise
lines to a handful of lifecycle Info lines, while the DAVE-decrypt and
undecodable-frame *information* is preserved as counters with rate alerts (§2) —
so a real DAVE handshake breakdown (sustained, not transient) still pages.

### 1.6 Other logging fixes (not noise, but ADR-0032 debt)

- `cmd/glyphoxa/main.go:20`: replace the hardcoded `TextHandler/Info` with a
  mode-selected handler (JSON for prod / text for dev) and wire the
  context-carried turn/session fields.
- Replace the three ad-hoc discard loggers
  (`slog.New(slog.NewTextHandler(discard{}, nil))` at `wirenpc.go:115,153`,
  `agentspec.go:60`) with the existing `discardLogger()` helper from
  `pkg/voice/observability.go:29` for one canonical no-op.

---

## 2. MONITORING

Per ADR-0032: `prometheus/client_golang`, a **deliberately small, hand-curated**
surface on the existing `web`/`all` HTTP server at `/metrics`; `voice` mode
opens a metrics-only listener. No `MeterProvider`/OTel exporter indirection.
Build on the seam that already exists rather than forking a parallel path:
`pkg/voice/observability.go` already defines `MetricsRecorder`
(`InboundFramesDropped`, `PlaybackStarted`, `PlaybackFinished`) with a no-op
default. Sprint 2 **extends that interface** and ships one Prometheus adapter
implementing it; the orchestrator gets a sibling recorder for the stage/turn
timings (driven off the `voiceevent` bus so instrumentation stays out of the hot
path).

### 2.1 Cardinality discipline (ADR-0032 is emphatic)

- **Never** label by `tenant_id`, `campaign_id`, `turn_id`, `guild`, or
  `agent_id`. `turn_id`/`guild`/`agent_id` are carried as **log fields and
  (optional) exemplar attributes**, not series labels — they correlate a single
  turn's spans without exploding cardinality.
- Bounded enum labels are fine: `provider` (elevenlabs|openai|gemini|anthropic),
  `agent_role` (butler|character — 2 values), `round_index`, `had_tool_call`,
  `outcome`.
- **`guild` and `agent_id` are the same unbounded class as `tenant_id`** on the
  SaaS path (ADR-0005). The existing `MetricsRecorder` *takes* `guild` as a
  param, but the Prometheus adapter **aggregates it away**. Want a per-agent cut?
  Use `agent_role`, not `agent_id`.

### 2.2 Metric surface (v1.0)

Namespace `glyphoxa_voice_*` (process namespace + `voice` subsystem). The
per-stage histograms below are the vocabulary **agreed with teammate `latency`
(task #2)** so a benchmark number maps 1:1 to a Prometheus series — latency.md
mirrors these exact names. Two conventions are applied vs. `latency`'s first
draft, both grounded (not preference), and confirmed in the coordination thread:
**(a) units are base-unit `_seconds`** (Prometheus convention; the bench may
*print* ms but the exported series is seconds), and **(b) `guild`/`agent_id` are
not labels** (§2.1) — `agent_role` is the bounded substitute.

**Latency / per-stage (the SLO core — shared with task #2 benchmarks):**

| Metric (histogram) | Span / meaning | Labels |
|---|---|---|
| `glyphoxa_voice_response_latency_seconds` | **headline SLO**: VAD speech-end → first `tts.AudioChunk` handed to PlaybackPump | `agent_role` |
| `glyphoxa_voice_vad_hangover_seconds` | speech-end detection lag (`minSilenceFrames*frameMs`, currently 15*32=480ms) | — |
| `glyphoxa_voice_stt_request_seconds` | STT provider POST round-trip (ElevenLabs scribe) | `provider` |
| `glyphoxa_voice_address_detect_seconds` | address-detection stage | — |
| `glyphoxa_voice_llm_round_seconds` | one LLM `Complete` round | `provider`,`round_index`,`had_tool_call` |
| `glyphoxa_voice_llm_turn_seconds` | full agenttool loop (all rounds + tool exec) | `provider` |
| `glyphoxa_voice_tts_ttfb_seconds` | `Synthesize` call → first `AudioChunk` | `provider` |
| `glyphoxa_voice_tts_total_seconds` | full TTS synthesis | `provider` |
| `glyphoxa_voice_codec_decode_seconds` / `_codec_encode_seconds` | Opus↔PCM per direction | — |

`turn_id` (stamped at `STTFinal`, propagated `AddressRouted`→reply→TTS) joins one
turn's spans across logs/exemplars. Histogram buckets sized to the SLOs (dense
around the p95 target). The four coarse spans I originally proposed
(stt/llm/tts/turn) are subsumed by these finer ones.

**Provider health:**

| Metric | Type | Labels |
|---|---|---|
| `glyphoxa_voice_provider_calls_total` | counter | `stage`,`provider`,`outcome` (ok/error/timeout) |
| `glyphoxa_voice_provider_errors_total` | counter | `stage`,`provider` |

**Voice / audio plumbing (extends `MetricsRecorder`):**

| Metric | Type | Labels | Source |
|---|---|---|---|
| `glyphoxa_voice_sessions` | gauge | — | Manager open/close |
| `glyphoxa_voice_inbound_frames_dropped_total` | counter | (guild aggregated) | existing `InboundFramesDropped` |
| `glyphoxa_voice_inbound_undecodable_frames_total` | counter | — | **new — pairs with log L1** |
| `glyphoxa_voice_dave_decrypt_errors_total` | counter | — | **new — pairs with log L5** |
| `glyphoxa_voice_playback_total` | counter | `interrupted` (true/false) | existing `PlaybackStarted/Finished` |
| `glyphoxa_voice_barge_cancels_total` | counter | — | `barge.detected` → turn cancel (ADR-0027) |

**Backlog (ADR-0032 mandates this one explicitly):**

| Metric | Type | Notes |
|---|---|---|
| `glyphoxa_embedding_backlog` | gauge | transcript chunks with `embedding IS NULL` (ADR-0011). Not a `voice` subsystem metric (process-level). The persistence/embedding layer isn't coded yet, so this lands when that layer does — listed now so the `/metrics` surface is spec-complete against ADR-0032 and a reviewer diffing the two doesn't see a gap. |

### 2.3 `/metrics` wiring

- `web`/`all` mode: register the default `prometheus.Registry` and mount
  `promhttp.Handler()` at `/metrics` on the existing HTTP server.
- `voice` mode: a minimal metrics-only `http.Server` (ADR-0032) — no SPA/SSE,
  just `/metrics` so a Prometheus can scrape a headless voice node.
- The Prometheus adapter is the single `MetricsRecorder` implementation injected
  via `gxvoice.WithLogger`'s metrics sibling (the wiring already passes a no-op
  recorder today — `manager.go:145`).

### 2.4 Dashboards & alerts that matter

Dashboards (Grafana, self-host points any Prometheus at `/metrics`):
- **Response latency** p50/p95/p99 of `glyphoxa_voice_response_latency_seconds`,
  overlaid with the SLO line.
- **Stage breakdown** stacked p95 of the per-stage histograms
  (`..._stt_request_seconds`, `..._llm_turn_seconds`, `..._tts_ttfb_seconds`, …)
  — shows *which* stage blew the response budget.
- **Provider health** error rate = `rate(..._provider_errors_total)` /
  `rate(..._provider_calls_total)` by provider.
- **Voice plumbing** `glyphoxa_voice_sessions`, frame-drop rate, and the two new
  noise counters.

Alerts (these replace the per-line log noise with a *rate* signal):
- `rate(glyphoxa_voice_dave_decrypt_errors_total[5m])` **sustained** above a
  baseline for >2m → "DAVE handshake degraded" (the benign trickle won't trip it;
  a real break will). Pairs with log L5.
- `rate(glyphoxa_voice_inbound_undecodable_frames_total[5m])` sustained →
  codec/feed problem. Pairs with log L1/L2.
- `histogram_quantile(0.95, glyphoxa_voice_response_latency_seconds)` over SLO
  for >5m → latency regression.
- `provider error ratio > X%` over 5m → vendor regression (ties to ADR-0033's
  "vendor regression" notification rather than a PR gate).
- `glyphoxa_voice_sessions == 0` while expected, or frame-drop rate spike →
  plumbing.

---

## 3. BENCHMARKING

Goal: a harness that produces the **same numbers the histograms emit**, runnable
keylessly in CI by default (ADR-0033) and against live vendors on a tagged cron.
A bench result must map 1:1 to a Prometheus series so "the dashboard says p95
turn = X" and "the bench says p95 turn = X" are the same measurement.

### 3.1 Fit to ADR-0033's CI tiers

| Tier | Build tag | Runs | Deps |
|---|---|---|---|
| **Default (every PR)** | none | `go test -bench` on the keyless harness | **none** — cassettes (ADR-0021) + `httptest` fakes; no Docker, no keys, no audio libs |
| **Live (`-tags=live`)** | `live` | nightly cron + pre-release | real STT/LLM/TTS keys |
| **Audio (`-tags opus,dave`)** | opus/dave | the CGO job | libopus/libdave |

The default tier is the one that must stay sub-second and secret-free
(ADR-0033's core contract). It measures **orchestration/pipeline** latency with
deterministic cassette providers — it is NOT a vendor-speed benchmark; it
catches *our* regressions (an extra copy, a serialized stage that should
pipeline, a lock). Live tier measures end-to-end with real vendors and feeds the
provider-latency SLOs.

### 3.2 What it measures (and where it taps)

Drive the harness off the existing `voicetest.Harness` (ADR-0019) which already
observes the `voiceevent` bus — so the bench reads the **same event timestamps**
the metrics adapter reads. For a fixed input clip / cassette set it records:

- **Response latency**: `vad.speech_end` → first playback frame (cassette path
  uses the TTS-tee `tts.AudioChunk` boundary as the "first audio" proxy when no
  real Opus encode is in the build) — directly comparable to
  `glyphoxa_voice_response_latency_seconds`.
- **Per-stage durations**: the per-stage spans in §2.2 (vad_hangover,
  stt_request, address_detect, llm_round/llm_turn, tts_ttfb/tts_total,
  codec_decode/encode), identical boundaries — so each bench number is directly
  comparable to its `glyphoxa_voice_*_seconds` histogram.
- **Throughput/overhead**: allocations & ns/op via `testing.B` for the hot frame
  path (`Pipeline.Run`, codec decode, VAD) — guards the per-frame budget at
  50 pkt/s.
- Reports p50/p95 across N replays of the canonical utterance set.

### 3.3 Tie-in to the latency SLOs (coordinate with task #2)

- The harness emits a small JSON artifact (stage → p50/p95) that a CI step
  asserts against **budgets owned by task #2**. A regression beyond the budget
  fails the (live-tier) job or, in default tier, flags an orchestration
  regression. Thresholds are **pending task #2's SLO numbers** — message sent to
  teammate `latency` proposing the four shared spans/labels above and asking for
  p50/p95 turn target + per-stage budgets; once they reply, the budget table and
  the §2.4 alert thresholds get filled with the same numbers (they must match so
  bench-fail and alert-fire trigger on one source of truth).
- Extra histograms to confirm with `latency`: barge-cancel latency
  (`barge.detected` → playback stop) and any floor/queue wait — added to both
  the metric surface (§2.2) and the bench if they want them.

### 3.4 Anti-goals (ADR-0033 discipline)

- Benchmarks do **not** gate unrelated PRs — they live in the bench/live job,
  not the default `go test -race ./...` correctness gate.
- No live keys in the default tier; cassette determinism keeps the every-PR
  number stable so a real regression isn't lost in vendor jitter.

---

## Cross-references

- ADR-0032 (observability: slog + thin Prometheus, tracing deferred) — the
  surface here is its v1.0 instrument list, plus the mandated embedding-backlog
  gauge.
- ADR-0033 (CI tiers) — the benchmark harness's keyless-default / tagged-live
  split.
- ADR-0006 (DAVE mandatory) — context for why the decrypt-error trickle exists.
- ADR-0020 (`voiceevent` taxonomy) — the bus events the metrics and benchmarks
  both key off.
- Coordinated with task #2 (`latency`): shared metric/event vocabulary, stage
  boundaries, and pending SLO thresholds.
