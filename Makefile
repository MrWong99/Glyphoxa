# Glyphoxa Makefile
# Requires: Go 1.27+ and the generated protobuf stubs (gen/ is gitignored —
# `make proto` produces it, ADR-0039).
#
# Two builds:
#   make build       — the default UNTAGGED build: pure Go, CGO off, statically
#                      linked and cross-compilable. It links the STUB Opus codec
#                      and the STUB DAVE wiring, so it serves the web console,
#                      migrations and the CLI subcommands but CANNOT carry a voice
#                      session (it logs a WARN at boot in voice/all Mode).
#   make build-live  — the voice-capable build with the production tags
#                      `opus dave nolibopusfile`, the same tags the container
#                      image uses. The outbound Opus encoder links system libopus
#                      via CGO (ADR-0034 amendment 2026-07-19), so it needs a C
#                      toolchain (gcc), pkg-config and libopus headers
#                      (libopus-dev / opus-devel / `brew install opus pkg-config`).
#                      The binary links libopus dynamically; the Dockerfile is the
#                      fully static variant.
# The test targets override the CGO export per-recipe: `go test -race` requires
# cgo (issue #580), so they need gcc too — while still linking no native libs.

export CGO_ENABLED := 0

# The version stamped into the binary (`glyphoxa version`, the boot log).
# Release images stamp the tag via the Dockerfile's VERSION build-arg.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)
LIVE_TAGS := opus dave nolibopusfile

.PHONY: build build-live gen-check test test-v test-cover lint vet fmt check clean refresh-silero-model install-lint proto proto-check proto-lint spa docker-build docker-smoke docker-smoke-test helm-lint helm-test helm-validate helm-validate-test

# Pure-Go stub build: web console, migrations and CLI only — NO voice (see the
# header). Use build-live for a binary that can join a voice channel.
build: gen-check
	go build -ldflags "$(LDFLAGS)" -o bin/glyphoxa ./cmd/glyphoxa

# Voice-capable build with the production tags (needs gcc + pkg-config + libopus).
build-live: gen-check
	CGO_ENABLED=1 go build -tags "$(LIVE_TAGS)" -ldflags "$(LDFLAGS)" -o bin/glyphoxa ./cmd/glyphoxa

# gen/ is gitignored; fail with the fix instead of a wall of missing-package errors.
gen-check:
	@test -d gen || { echo "gen/ is missing: run 'make proto' (buf generate) first" >&2; exit 1; }

# Run all tests with race detector. CGO_ENABLED=1 overrides the global pure-Go
# export: -race requires cgo (issue #580). Mirrors CI's test job, which runs
# `go test -race ./...` with CGO on.
test:
	CGO_ENABLED=1 go test -race -count=1 ./...

# Run tests with verbose output
test-v:
	CGO_ENABLED=1 go test -race -count=1 -v ./...

# Run tests with coverage
test-cover:
	CGO_ENABLED=1 go test -race -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1
	@echo "HTML report: go tool cover -html=coverage.out"

# Lint with golangci-lint v2 (config requires v2).
# Install: make install-lint   OR   https://golangci-lint.run/welcome/install/
lint:
	$(shell go env GOPATH)/bin/golangci-lint run ./...

# Install golangci-lint v2 into GOPATH/bin, at the version CI's lint job pins
# (.github/workflows/ci.yml) so local and CI lint agree. Build it with the
# project's Go toolchain: a binary built by an older Go cannot lint 1.27 code.
GOLANGCI_LINT_VERSION ?= v2.15.0
install-lint:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

# Go vet
vet:
	go vet ./...

# Format the tree in place (gofmt -w; lists the files it rewrote).
fmt:
	gofmt -l -w .

# Full pre-commit check
check: fmt vet test
	@echo "All checks passed ✓"

# Refresh the embedded Silero VAD ONNX model from upstream. Run when bumping
# silero versions; commit the updated file, the new SHA-256 in
# pkg/voice/vad/silero/data/README.md, AND regenerated goldens
# (scripts/gen-silero-golden.py) — the golden equivalence test is the
# acceptance gate for any model change (#468). The pure-Go forward pass
# consumes the op18 "ifless" export; the legacy opset-16 export (nested Ifs,
# LSTM op) is NOT loadable by it.
SILERO_MODEL_URL := https://github.com/snakers4/silero-vad/raw/master/src/silero_vad/data/silero_vad_op18_ifless.onnx
SILERO_MODEL_DST := pkg/voice/vad/silero/data/silero_vad_op18_ifless.onnx

refresh-silero-model:
	curl -fsSL "$(SILERO_MODEL_URL)" -o "$(SILERO_MODEL_DST)"
	@echo "Updated $(SILERO_MODEL_DST)"
	@echo "New SHA-256:"
	@sha256sum "$(SILERO_MODEL_DST)"
	@echo "Update the SHA-256 in pkg/voice/vad/silero/data/README.md and regenerate"
	@echo "the goldens (scripts/gen-silero-golden.py) before committing."

# Protobuf code generation via buf.
# Install: go install github.com/bufbuild/buf/cmd/buf@latest
proto:
	buf generate

# Verify the proto stubs generate AND the result compiles. gen/ is gitignored
# (ADR-0039) — produced in CI and the Docker build, not committed — so a stale-
# diff check is meaningless. Instead regenerate and compile: a broken or stale
# proto now fails by not building.
proto-check:
	buf generate
	go build ./...

# Lint protobuf definitions.
proto-lint:
	buf lint

# Clean build artifacts
clean:
	rm -rf bin/ coverage.out

# --- Container image (ADR-0034) --------------------------------------------
# One multi-stage image for the single binary; `mode`/config are runtime args.
# The image builds the live tags with libopus linked STATICALLY (CGO on, see the
# Dockerfile), so the runtime stage is FROM scratch carrying the static binary +
# CA certs. VERSION is stamped through the Dockerfile's VERSION build-arg.
DOCKER_IMAGE ?= glyphoxa:smoke

# Depends on `proto`: gen/ is gitignored and NOT generated inside the image
# (buf/node stay out of the builder), so it must exist in the build context on
# the host before `docker build` ships it. `make proto` runs `buf generate` first.
#
# The SPA bundle is context-fed too (ADR-0034 amendment, #114): a node-free
# `make docker-build` embeds the committed placeholder index.html, so run
# `make spa` first to build the real console into internal/spa/dist and ship it
# (CI feeds this via the `web` job's `spa-dist` artifact instead).
docker-build: proto
	docker build --build-arg VERSION=$(VERSION) -t $(DOCKER_IMAGE) .

# Build the real Vite + React console into internal/spa/dist (vite.config.ts
# build.outDir) so a following `make docker-build` embeds the console rather than
# the placeholder fallback (#114). Requires Node; the Go builds do not. It
# overwrites the tracked placeholder internal/spa/dist/index.html — run
# `git checkout -- internal/spa/dist/` before committing.
spa:
	cd web && npm ci && npm run build

# Run the container smoke test (issue #31) against $(DOCKER_IMAGE). Asserts the
# CLI works, the binary is fully static (#468), the image is scratch-minimal
# with CA certs, the process is non-root, and the embedded web root is the
# real console (#114). Build the image first (make docker-build).
docker-smoke:
	./scripts/container-smoke.sh $(DOCKER_IMAGE)

# Self-test for the embedded-console gate above: proves container-smoke.sh FAILS
# on a placeholder-only image and PASSES only on a real build, so the gate can't
# silently no-op (the #140 discipline, mirrored for #114). Needs only Docker.
docker-smoke-test:
	./scripts/container-smoke-test.sh

# --- Helm chart (ADR-0034, issue #34) --------------------------------------
# The deploy chart stands up a pgvector Postgres and applies the schema via a
# migrate pre-install hook Job. These targets mirror the fast `helm` CI job:
# template-time correctness only, no cluster (the cluster e2e is issue #37).
# Requires: helm, the helm-unittest plugin
# (helm plugin install https://github.com/helm-unittest/helm-unittest), and
# kubeconform (https://github.com/yannh/kubeconform).
HELM_CHART ?= deploy/charts/glyphoxa

helm-lint:
	helm lint $(HELM_CHART)

helm-test:
	helm unittest $(HELM_CHART)

# Render both value paths (in-cluster Postgres and external DB) with the CI
# dummy values and schema-check each against the upstream Kubernetes schemas.
# Delegates to scripts/helm-validate.sh, which fails on a render error and on
# an empty render — the two modes the former inline pipes silently passed (#140).
helm-validate:
	HELM_VALIDATE_CHART=$(HELM_CHART) scripts/helm-validate.sh

# Self-test for the gate above: asserts helm-validate actually fails when the
# render fails or is empty.
helm-validate-test:
	scripts/helm-validate-test.sh
