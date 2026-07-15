# CI / test strategy: keyless-by-default suite, build-tag-isolated heavy tests, tiered live

The default `go test -race ./...` stays keyless, deterministic, and fast on every PR — no Docker, no API keys, no audio libs required. Everything that needs an external dependency (Postgres, libopus/libdave, live vendor APIs) is isolated behind a build tag and run as a *separate* CI job or on a cron, never gating an unrelated PR. This is the gate that keeps small commits + tight reviews cheap.

## What this decides

- **Default PR gate (`ci.yml`, runs on every PR):** `go build ./...`, `go vet ./...` and `go vet -tags=record ./...`, `go test -race ./...`, and `golangci-lint` v2 (installed from source under the project toolchain — the official action pins v1, which can't parse the v2 config on Go 1.26). Must stay sub-minute-ish and require **no secrets and no Docker** — cassettes (ADR-0021) make LLM/STT deterministic, and provider adapters are unit-tested keylessly with `httptest`.
- **DB integration tests — the discriminating call:** the persistence layer's testcontainers-Postgres tests do **not** run in the default `go test ./...` path (that would break ADR-0021's "sub-second, free, every PR" contract). They are gated by a `//go:build integration` tag and run as a **dedicated CI job** with Docker available (`go test -race -tags=integration ./internal/storage/...`). Locally they also skip gracefully when no Docker daemon is reachable. Migrations are exercised here (goose up/down round-trip, ADR-0031).
- **CGO/audio builds (`-tags opus`, `-tags dave`, `nolibopusfile`):** a separate job installs libopus/libdave (`make dave-libs`) and runs `go build`/`go vet`/lint across the audio tag combinations so the live binary can't silently break. The codec is stubbed (`ErrCodecUnavailable`) in the default build, so the orchestrator suite stays CGO-light.
- **Live vendor tests (`-tags=live`, `-tags=record`):** unchanged from ADR-0021 — `record` regenerates cassettes deliberately on prompt/model changes; `live` runs ~5–10 canonical cases against real APIs on a nightly cron (`record-live.yml`) + pre-release, and its failures open a "vendor regression" notification rather than blocking PRs.

## Why

The sprint validated the pattern empirically: every component (LLM, Gemini, tool loop, codec, TTS tee) shipped with keyless, race-clean, deterministic tests that ran on every commit, and the expensive bits (real Discord+ElevenLabs+Gemini audio smoke) were a single coordinated manual run, not a gate. That is exactly the shape that makes "small reviewable diffs" affordable: a reviewer trusts a green default suite without provisioning Docker, keys, or audio libs. The only genuinely new gating question after the sprint was the persistence layer's testcontainers tests — and the answer is "tag-isolate + separate job," because folding a Postgres container into the default suite would silently violate ADR-0021's contract and slow every unrelated PR.

## Considered options

- **One suite, everything in `go test ./...`** — rejected. Testcontainers + audio CGO + (worst) live keys in the default path makes the every-PR gate slow, flaky, and secret-dependent; it recreates v1's "green tests, nothing reasons" rot and breaks ADR-0021.
- **Docker-detection skip instead of a build tag for DB tests** — partially adopted (used for *local* graceful skip) but not as the *CI* mechanism: an explicit `-tags=integration` job makes "did the DB tests actually run?" auditable, whereas silent skip-on-no-Docker can hide a job that quietly tested nothing.
- **Live APIs gate PRs** — rejected (already settled by ADR-0021): cost, latency, and vendor flakiness would mute the suite.

## Addendum (Sprint 2): the latency benchmark's two tiers and where they run

Sprint 2's benchmark harness (`pkg/voice/voicebench`, Epic C) adds a `//go:build bench` tag and clarifies a latent inconsistency in the Sprint-2 plan: §3.1 called the cassette bench a "default tier, no audio libs" while §5 said it measures real VAD inference + codec. Both cannot hold. Resolution, consistent with this ADR's keyless/CGO split:

- **`bench` tag is orthogonal to `opus`.** `//go:build bench` marks "this file is benchmark code"; `opus`/`dave` mark "audio CGO deps present." Keeping them separate lets the same bench files run with or without audio libs.
- **The real cassette bench is keyless-but-CGO.** It drives **real silero VAD + real codec** with **cassettes for the network providers** (STT/TTS/LLM — no keys, ADR-0021). So it is keyless (no secrets, fits the no-keys contract) but needs CGO, so it runs in the **existing audio/CGO CI job** invoked as `go test -tags "bench opus …"`, NOT in the default no-CGO PR gate. The default `go test -race ./...` never compiles the `bench` files, so the fast keyless default gate is untouched.
- **Two assertion modes, by tier (one source of truth with the A2 alerts):** the **cassette tier is a REGRESSION-DIFF** — replay is instant, so its spans are orchestration-only (~0) and an absolute SLO would pass trivially and mask a 10x regression; it compares each stage's p95 to a committed `baseline.json` and fails on a relative delta (`Report.CheckRegression`). The **live tier (`-tags=live`, nightly cron) owns the absolute EngineeringSLO** (≤1.2 s p50 / ≤2.5 s p95 on `response_latency`, via `Report.CheckSLO`). A cassette "baseline p50/p95" is therefore a plumbing/orchestration number, NOT the user-facing latency the Sprint-2 B-fixes are judged against — that judgment is a live-tier run.

---

**Amendment (2026-07-16, pion/opus + dave-go migration):** the `opus` and
`dave` tags are pure Go now (github.com/pion/opus, github.com/thomas-vilte/dave-go);
the audio CI job no longer installs libopus or libdave (`make dave-libs` is
gone) and the `nolibopusfile` companion tag is retired. The job itself remains —
it still owns the tagged builds, the opus-tagged tests, and the keyless-but-CGO
cassette benchmark (the Silero VAD's ONNX binding is still cgo) — and the `dave`
tag is now compiled there too, since that costs nothing. References to
"installs libopus/libdave" above are historical.
