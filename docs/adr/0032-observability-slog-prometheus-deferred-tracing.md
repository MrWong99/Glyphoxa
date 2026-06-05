# Observability: structured slog + thin Prometheus, tracing deferred behind a flag

Logging is structured `log/slog` (JSON in production, text in dev) from day one. Metrics are a small, hand-curated Prometheus surface exposed on the existing `web`/`all` HTTP server at `/metrics`. Distributed tracing (OpenTelemetry) is **deferred** and, when it lands, is flag-gated and off by default — not plumbed through the codebase up front.

## What this decides

- **Logs:** stdlib `log/slog`. One process-wide handler chosen by `mode`/env: JSON for prod, text for local. Request/turn-scoped fields (`tenant_id`, `campaign_id`, `voice_session_id`, `guild_id`, `turn_id`) are carried on a `*slog.Logger` in `context.Context`, not threaded as bare args. No third-party logging library.
- **Metrics:** `prometheus/client_golang`, a deliberately small set of instruments — not auto-instrumentation. v1.0 surface: turn latency histogram (utterance→first-audio), STT/LLM/TTS provider call duration + error counters labelled by `component`+`provider` (bounded cardinality — never by `tenant_id`), active `voice_sessions` gauge, embedding-backlog gauge (chunks with `embedding IS NULL`, per ADR-0011). Scraped at `/metrics`; `voice` mode opens a minimal metrics-only listener.
- **Tracing:** none in v1.0. A single `internal/observe` seam may expose a no-op tracer so call sites can opt in later, but no exporter, no `TracerProvider`, no span plumbing ships until a concrete need (cross-process `gateway`/`voice` split, ADR-0005's SaaS path) justifies it. When added: OTel, gated behind an env flag, default off.

## Why — the v1 failure mode, named

v1 is not rejected for "having observability." It is rejected for a *specific* over-build (the same discipline as ADR-0005: gRPC-for-audio failed, gRPC-for-control is fine). v1's `internal/observe` stood up a full OTel apparatus for a single-binary app: `sdktrace.TracerProvider` with batchers + a swappable `SpanExporter`, an `sdkmetric.MeterProvider` with a Prometheus exporter, a package-level `Tracer()`, and manual `StartSpan` calls spread across ~9 files — all to produce traces that never crossed a process boundary, because v1 (like v2's `all` mode) ran in one process. The spans added ceremony and context-propagation bugs without the payoff distributed tracing exists for. Meanwhile the part that *worked* was plain `slog` (741 call sites) — readable, greppable, zero ceremony.

So v2 keeps what worked (slog), keeps the cheap high-value half of the metrics stack (a small Prometheus surface — no MeterProvider/exporter indirection, just `client_golang`), and **defers the half that hurt** (tracing) until the architecture actually spans processes. This matches the methodology's "distinguish what *failed* from what merely *lived*."

## Considered options

- **Full OTel from day one (logs+metrics+traces via the OTel SDK)** — rejected. This is precisely v1's sprawl; pays distributed-tracing cost in a single-binary deployment.
- **slog only, no metrics** — rejected. Turn latency and provider error rates are operational must-haves for a real-time voice product; a `/metrics` surface is cheap and the self-host target can point any Prometheus at it.
- **OTel logs bridge / `slog` → OTel handler** — deferred, not adopted. Reasonable once tracing exists (to correlate logs↔spans), but premature before there are spans to correlate.
