# Contributing to Glyphoxa

Thanks for your interest in contributing to Glyphoxa! This guide covers everything
you need to get started.

## Getting Started

### Prerequisites

- **Go 1.26+** with CGo enabled (`CGO_ENABLED=1`)
- **Node.js 20+ and npm** — the operator console is a Vite/React bundle the Go binary embeds (see the SPA step below)
- **[buf](https://buf.build/docs/installation)** — the Connect/protobuf stubs under `gen/` are generated, not committed
- **libopus** — `apt install libopus-dev` (Debian/Ubuntu) · `pacman -S opus` (Arch) · `brew install opus` (macOS)
- **ONNX Runtime** — shared library from [onnxruntime releases](https://github.com/microsoft/onnxruntime/releases) (for Silero VAD)

The full self-host setup (Postgres/pgvector, the `.env` template, Discord OAuth)
lives in [docs/configuration.md](docs/configuration.md); the build steps below
mirror its §3 build order.

### Build & Test

```bash
git clone https://github.com/MrWong99/Glyphoxa.git
cd Glyphoxa

# 1. Generate the protobuf stubs → gen/ (Go + TS). gen/ is gitignored, so this
#    must run first: the Go build and the SPA both import the generated code.
make proto

# 2. Build the SPA bundle → internal/spa/dist (go:embed). Must run before
#    `make build` embeds the console; skipping it embeds a blank placeholder.
make spa

# 3. Build the binary → bin/glyphoxa
make build

# Run all tests with race detector
make test

# Lint
make lint

# Full check (fmt + vet + test) — the pre-push gate
make check
```

Order matters: `make proto` → `make spa` → `make build`, exactly as
[docs/configuration.md](docs/configuration.md) §3 mandates.

#### Audible local runs (voice mode)

The audio codec (pion/opus) and DAVE/MLS encryption (dave-go) are pure Go,
selected by build tags — no native libraries to install. A default build links
stubs — the pipeline wires up but the audio loop exits with `wire: audio codec
unavailable` on the first frame. For an **audible** (and encrypted) live run,
build with the audio tags:

```bash
CGO_ENABLED=1 go build -tags "opus dave" -o glyphoxa ./cmd/glyphoxa
```

- `opus` — real Opus↔PCM codec (else the stub: no audio).
- `dave` — real DAVE/MLS encryption (mandatory on production Discord; else unencrypted).

(CGO_ENABLED=1 is still required — the Silero VAD's ONNX binding is cgo.)

See [docs/agents/live-npc-run.md](docs/agents/live-npc-run.md) for the full
`voice`-mode runbook.

## Development Workflow

1. **Fork the repo** and create a feature branch from `main`
2. **Write code** following the style guidelines below
3. **Add tests** — every new package needs tests; aim for table-driven, parallel tests
4. **Run `make check`** before pushing
5. **Open a PR** against `main` — fill out the PR template

### Branch Naming

- `feat/short-description` — new features
- `fix/short-description` — bug fixes
- `docs/short-description` — documentation only
- `refactor/short-description` — code cleanup

## Code Style

Glyphoxa follows standard Go conventions with a few project-specific rules:

### Go Conventions

- **`gofmt`** — all code must be formatted with `gofmt`
- **`go vet`** — must pass cleanly
- **Godoc** — all exported symbols must have complete doc comments
- **Error wrapping** — use `%w` with consistent package prefixes (e.g., `"agent: ..."`)
- **No naked returns** — always name what you're returning explicitly
- **No stale loop-var captures** — Go 1.22+ semantics apply

### Testing

- **`t.Parallel()`** on all tests and subtests
- **Table-driven tests** where appropriate
- **`go test -race`** must pass — all public methods must be safe for concurrent use
- **Compile-time interface assertions** — `var _ Interface = (*Impl)(nil)`
- **Mocks** live in `<package>/mock/` subdirectories

For detailed testing patterns, mock conventions, and examples, see the [Voice Tests guide](docs/devs/voice-tests.md).

### Concurrency

- Thread-safety is non-negotiable — every public method must be safe for concurrent use
- Prefer `sync.Mutex` over channels for protecting shared state
- Never hold a lock during blocking I/O (network calls, channel operations)
- Use `container/heap` for priority queues (not sorted slices)
- Use `slices.SortFunc` over `sort.Slice` (Go 1.21+)

### Packages

- **`pkg/`** — public API; external code may import these packages
- **`internal/`** — application-private; not importable by external code
- **`cmd/`** — entry points

## Architecture

Before contributing a major feature, read the architecture overview and the ADRs:

- [Architecture overview](docs/architecture.md) — the current-system overview; every subsystem names the ADR(s) that govern it
- [Architecture Decision Records](docs/adr/) — the decisions ledger (system layers, provider seams, data flow)

The core principle: **every external dependency sits behind an interface**. Swapping
providers is a config change, not a rewrite.

## Reporting Issues

- **Bugs** — use the [Bug Report template](.github/ISSUE_TEMPLATE/bug_report.yml)
- **Features** — use the [Feature Request template](.github/ISSUE_TEMPLATE/feature_request.yml)
- **Security** — see [SECURITY.md](SECURITY.md)

## License

By contributing, you agree that your contributions will be licensed under the
[GPL v3](LICENSE).
