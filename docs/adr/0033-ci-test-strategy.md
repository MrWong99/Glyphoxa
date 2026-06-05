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
