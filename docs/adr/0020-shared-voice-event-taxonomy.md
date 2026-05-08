# Shared event taxonomy across tests and SSE

Voice tests and prod SSE consume the same in-process event channel. **One vocabulary, two transports.** The harness observes the channel directly; the SSE relay (per ADR-0014) forwards the same events to browsers.

Test assertions live in `pkg/voice/voicetest/` as imperative Go test functions, with primitives `AssertEventOccurred`, `AssertNoEvent`, `AssertEventCount`, `AssertTimingBetween`, `AssertOrder`, and `MustOne`. Per-clip `meta.yaml` is pure documentation (spoken script, intent, expected outcome in prose) — assertions live in Go, not YAML.

**Considered options:**

- **YAML DSL for assertions** — rejected. Reinvents test predicates Go already has, and gives up `go test` integration (race, coverage, CI).
- **Golden event-log files** — rejected. Drift on every model update produces churn that hides real regressions.

**Why one taxonomy:** keeping test events and prod events identical means the SSE wire is exercised by every test run, and live debugging uses the same vocabulary as test failure reports.
