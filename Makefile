# Glyphoxa Makefile
# Requires: Go 1.26+, CGO_ENABLED=1

export CGO_ENABLED := 1

.PHONY: build build-web test lint vet fmt check clean whisper-libs dave-libs onnx-libs install-lint proto proto-check proto-lint web-install web-dev web-build web-lint

# Build voice engine
build:
	go build -o bin/glyphoxa ./cmd/glyphoxa

# Build web management service (no CGO required)
build-web:
	CGO_ENABLED=0 go build -o bin/glyphoxa-web ./cmd/glyphoxa-web

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

# Download ONNX Runtime shared library for Silero VAD.
# After running this, set the environment before other targets:
#   export LD_LIBRARY_PATH=/tmp/onnx-install/lib:$LD_LIBRARY_PATH
ONNX_VERSION := 1.24.1
ONNX_DEST    := /tmp/onnx-install

onnx-libs:
	@if [ -f "$(ONNX_DEST)/lib/libonnxruntime.so" ]; then \
		echo "ONNX Runtime already installed at $(ONNX_DEST)"; \
	else \
		ONNX_ARCH="x64"; \
		if [ "$$(uname -m)" = "aarch64" ]; then ONNX_ARCH="aarch64"; fi; \
		echo "Downloading ONNX Runtime $(ONNX_VERSION) for $${ONNX_ARCH}..."; \
		curl -fsL "https://github.com/microsoft/onnxruntime/releases/download/v$(ONNX_VERSION)/onnxruntime-linux-$${ONNX_ARCH}-$(ONNX_VERSION).tgz" \
			-o /tmp/onnxruntime.tgz; \
		mkdir -p $(ONNX_DEST); \
		tar xzf /tmp/onnxruntime.tgz -C $(ONNX_DEST) --strip-components=1; \
		rm -f /tmp/onnxruntime.tgz; \
		echo "ONNX Runtime installed to $(ONNX_DEST)"; \
	fi
	@echo ""
	@echo "ONNX Runtime is loaded at runtime via dlopen (no compile-time flags needed)."
	@echo "Set LD_LIBRARY_PATH for runtime, or use options.onnx_lib_path in config:"
	@echo "  export LD_LIBRARY_PATH=$(ONNX_DEST)/lib:\$$LD_LIBRARY_PATH"

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

# Check that generated protobuf code is up to date (CI target).
proto-check:
	buf generate
	@if [ -n "$$(git diff --name-only gen/)" ]; then \
		echo "ERROR: generated protobuf code is stale. Run 'make proto' and commit."; \
		git diff --name-only gen/; \
		exit 1; \
	fi

# Lint protobuf definitions.
proto-lint:
	buf lint

# ---------------------------------------------------------------------------
# Web frontend (Next.js)
# ---------------------------------------------------------------------------

# Install web frontend dependencies
web-install:
	cd web && npm ci

# Run web frontend dev server
web-dev:
	cd web && npm run dev

# Build web frontend for production
web-build:
	cd web && npm run build

# Lint web frontend
web-lint:
	cd web && npm run lint

# Clean build artifacts
clean:
	rm -rf bin/ coverage.out
