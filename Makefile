# Glyphoxa Makefile
# Requires: Go 1.26+, CGO_ENABLED=1

export CGO_ENABLED := 1

.PHONY: build test lint vet fmt check clean whisper-libs dave-libs refresh-silero-model install-lint proto proto-check proto-lint docker-build docker-smoke helm-lint helm-test helm-validate

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

# Install libdave shared library for DAVE (Discord Audio Video Encryption).
# Required by github.com/disgoorg/godave/golibdave (CGO bindings).
# The install script downloads a pre-built binary or builds from source.
DAVE_VERSION := v1.1.0
DAVE_SRC     := /tmp/godave-src

dave-libs:
	@if pkg-config --exists dave 2>/dev/null; then \
		echo "libdave already installed (found via pkg-config)"; \
	else \
		echo "Installing libdave $(DAVE_VERSION)..."; \
		rm -rf $(DAVE_SRC); \
		git clone --depth 1 https://github.com/disgoorg/godave.git $(DAVE_SRC); \
		cd $(DAVE_SRC) && bash scripts/libdave_install.sh $(DAVE_VERSION); \
		echo ""; \
		echo "libdave installed. Add to your shell profile:"; \
		echo "  export PKG_CONFIG_PATH=\"$$HOME/.local/lib/pkgconfig:\$$PKG_CONFIG_PATH\""; \
		echo "  export LD_LIBRARY_PATH=\"$$HOME/.local/lib:\$$LD_LIBRARY_PATH\""; \
		echo "  export CGO_ENABLED=1"; \
	fi

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
# The native build (whisper.cpp + libopus + libdave + CGO) is SLOW on a cold
# layer cache — expect several minutes on first build.
DOCKER_IMAGE ?= glyphoxa:smoke

# Depends on `proto`: gen/ is gitignored and NOT generated inside the image
# (buf/node stay out of the builder), so it must exist in the build context on
# the host before `docker build` ships it. `make proto` runs `buf generate` first.
docker-build: proto
	docker build -t $(DOCKER_IMAGE) .

# Run the container smoke test (issue #31) against $(DOCKER_IMAGE). Asserts the
# CLI works, ldd resolves every native dep, the bundled ONNX runtime exists, and
# the process is non-root. Build the image first (make docker-build).
docker-smoke:
	./scripts/container-smoke.sh $(DOCKER_IMAGE)

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

# Render both value paths (in-cluster Postgres and external DB) and schema-check
# each against the upstream Kubernetes schemas.
helm-validate:
	helm template glyphoxa $(HELM_CHART) | kubeconform -strict -summary -kubernetes-version 1.30.0
	helm template glyphoxa $(HELM_CHART) --set postgres.enabled=false \
		--set database.url='postgres://u:p@external.example.com:5432/glyphoxa?sslmode=require' \
		| kubeconform -strict -summary -kubernetes-version 1.30.0
