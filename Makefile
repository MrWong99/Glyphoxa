# Glyphoxa Makefile
# Requires: Go 1.26+, CGO_ENABLED=1

export CGO_ENABLED := 1

.PHONY: build test lint vet fmt check clean whisper-libs refresh-silero-model install-lint proto proto-check proto-lint spa docker-build docker-smoke docker-smoke-test helm-lint helm-test helm-validate helm-validate-test

# Build voice engine
build:
	go build -o bin/glyphoxa ./cmd/glyphoxa

# Run all tests with race detector
test:
	go test -race -count=1 ./...

# Run tests with verbose output
test-v:
	go test -race -count=1 -v ./...

# Run tests with coverage
test-cover:
	go test -race -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1
	@echo "HTML report: go tool cover -html=coverage.out"

# Lint with golangci-lint v2 (config requires v2).
# Install: make install-lint   OR   https://golangci-lint.run/welcome/install/
lint:
	$(shell go env GOPATH)/bin/golangci-lint run ./...

# Install golangci-lint v2 into GOPATH/bin.
install-lint:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

# Go vet
vet:
	go vet ./...

# Format check
fmt:
	gofmt -l -w .

# Full pre-commit check
check: fmt vet test
	@echo "All checks passed ✓"

# Build whisper.cpp static library for local CGO compilation.
# After running this, set the environment before other targets:
#   export C_INCLUDE_PATH=/tmp/whisper-install/include
#   export LIBRARY_PATH=/tmp/whisper-install/lib
#   export CGO_ENABLED=1
WHISPER_SRC  := /tmp/whisper-src
WHISPER_DEST := /tmp/whisper-install

whisper-libs:
	@if [ -f "$(WHISPER_DEST)/include/whisper.h" ]; then \
		echo "whisper.cpp already built at $(WHISPER_DEST)"; \
	else \
		echo "Cloning whisper.cpp..."; \
		git clone --depth 1 https://github.com/ggml-org/whisper.cpp.git $(WHISPER_SRC); \
		echo "Building whisper.cpp..."; \
		cmake -B $(WHISPER_SRC)/build -S $(WHISPER_SRC) \
			-DCMAKE_BUILD_TYPE=Release \
			-DBUILD_SHARED_LIBS=OFF \
			-DGGML_NATIVE=ON \
			-DWHISPER_BUILD_TESTS=OFF \
			-DWHISPER_BUILD_SERVER=OFF; \
		cmake --build $(WHISPER_SRC)/build --config Release -j$$(nproc); \
		mkdir -p $(WHISPER_DEST)/include $(WHISPER_DEST)/lib; \
		cp $(WHISPER_SRC)/include/whisper.h $(WHISPER_DEST)/include/; \
		cp -r $(WHISPER_SRC)/ggml/include/* $(WHISPER_DEST)/include/ 2>/dev/null || true; \
		find $(WHISPER_SRC)/build -name '*.a' -exec cp {} $(WHISPER_DEST)/lib/ \;; \
		echo "whisper.cpp installed to $(WHISPER_DEST)"; \
	fi
	@echo ""
	@echo "Run the following to enable whisper CGO builds:"
	@echo "  export C_INCLUDE_PATH=$(WHISPER_DEST)/include"
	@echo "  export LIBRARY_PATH=$(WHISPER_DEST)/lib"
	@echo "  export CGO_ENABLED=1"

# Refresh the embedded Silero VAD ONNX model from upstream. Run when bumping
# silero versions; commit the updated file and the new SHA-256 in
# pkg/voice/vad/silero/data/README.md.
SILERO_MODEL_URL := https://github.com/snakers4/silero-vad/raw/master/src/silero_vad/data/silero_vad.onnx
SILERO_MODEL_DST := pkg/voice/vad/silero/data/silero_vad.onnx

refresh-silero-model:
	curl -fsSL "$(SILERO_MODEL_URL)" -o "$(SILERO_MODEL_DST)"
	@echo "Updated $(SILERO_MODEL_DST)"
	@echo "New SHA-256:"
	@sha256sum "$(SILERO_MODEL_DST)"
	@echo "Update the SHA-256 in pkg/voice/vad/silero/data/README.md before committing."

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
# The native build (whisper.cpp + CGO) is SLOW on a cold layer cache — expect
# several minutes on first build. (libopus and libdave are gone: the codec and
# DAVE are pure Go since the pion/opus + dave-go migration.)
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
	docker build -t $(DOCKER_IMAGE) .

# Build the real Vite + React console into internal/spa/dist (vite.config.ts
# build.outDir) so a following `make docker-build` embeds the console rather than
# the placeholder fallback (#114). Requires Node; the pure-Go build path does not.
spa:
	cd web && npm ci && npm run build

# Run the container smoke test (issue #31) against $(DOCKER_IMAGE). Asserts the
# CLI works, ldd resolves every native dep, the bundled ONNX runtime exists, the
# process is non-root, and the embedded web root is the real console (#114).
# Build the image first (make docker-build).
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
