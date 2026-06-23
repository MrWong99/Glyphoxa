# syntax=docker/dockerfile:1
#
# Glyphoxa — single multi-stage OCI image for the one binary (ADR-0005, ADR-0034).
#
# There is ONE image. `mode` and all config (Postgres URL, provider keys, guild/
# channel, etc.) are supplied at RUNTIME via args/env — there are no per-mode
# images. The build stage compiles the live binary with the production CGO tags
# (`opus dave nolibopusfile`) after building the whisper.cpp static lib and
# installing libopus + libdave, exactly as the Makefile / CI do. The runtime
# stage is a slim glibc base (distroless deferred — the CGO deps need glibc,
# ADR-0034) carrying the binary plus the native libs it needs: libopus + libdave
# (link-time deps, via pkg-config) and a bundled libonnxruntime (the version the
# Silero VAD pins in pkg/voice/vad/silero/runtime.go — dlopen'd at runtime, not
# linked). GLYPHOXA_ONNX_LIB points at that bundled lib so the VAD never
# downloads a runtime at container start.
#
# The embedded Silero model (pkg/voice/vad/silero/data/silero_vad.onnx) and the
# SQL migrations (internal/storage/migrations/*.sql) are go:embed'd into the
# binary — they need no separate runtime files.

# ---------------------------------------------------------------------------
# Build args — pinned versions live here so a bump is one obvious edit.
# ONNX_VERSION MUST match onnxRuntimeVersion in pkg/voice/vad/silero/runtime.go;
# the smoke test fails loudly if the bundled lib is missing, but it cannot tell
# you the version drifted, so keep these in lockstep.
# ---------------------------------------------------------------------------
ARG GO_VERSION=1.26
ARG ONNX_VERSION=1.26.0
ARG DAVE_VERSION=v1.1.0

# ===========================================================================
# Stage: build — compile the live binary + gather the native runtime deps.
# Debian trixie (glibc 2.41) so the CGO toolchain and the runtime base agree on
# libc. trixie — NOT the older bookworm — because the prebuilt libdave asset is
# linked against GLIBC_2.38 / GLIBCXX_3.4.32 (it's built on a newer toolchain,
# the same Ubuntu-24.04 glibc the CI audio job links it against); bookworm's
# glibc 2.36 / older libstdc++ can't satisfy those symbols at link time.
# ===========================================================================
FROM golang:${GO_VERSION}-trixie AS build
ARG ONNX_VERSION
ARG DAVE_VERSION

# A fixed HOME so the libdave install script (which targets $HOME/.local) lands
# its lib/include/pkgconfig at a path we control and can replicate verbatim in
# the runtime stage — the generated dave.pc bakes `-Wl,-rpath,$HOME/.local/lib`
# into the binary, so the runtime stage recreates that exact directory too.
ENV HOME=/opt/build
ENV CGO_ENABLED=1

# Build/runtime native deps:
#   - cmake/git/build-essential: build whisper.cpp (static) and (fallback) libdave.
#   - pkg-config + libopus-dev: the opus codec links libopus via pkg-config opus.
#   - curl/unzip: fetch the libdave prebuilt + the ONNX runtime tarball.
RUN apt-get update && apt-get install -y --no-install-recommends \
		ca-certificates \
		git \
		cmake \
		build-essential \
		pkg-config \
		libopus-dev \
		curl \
		unzip \
	&& rm -rf /var/lib/apt/lists/*

# --- whisper.cpp static library (mirrors Makefile `whisper-libs`) ----------
# Static (BUILD_SHARED_LIBS=OFF) so the .a is linked into the binary; nothing
# from whisper needs to ship in the runtime image. GGML_NATIVE=OFF here (unlike
# the Makefile's ON): the image must run on hosts other than the builder, so we
# must NOT bake in -march=native instructions the runtime CPU may lack.
ARG WHISPER_DEST=/tmp/whisper-install
RUN git clone --depth 1 https://github.com/ggml-org/whisper.cpp.git /tmp/whisper-src \
	&& cmake -B /tmp/whisper-src/build -S /tmp/whisper-src \
		-DCMAKE_BUILD_TYPE=Release \
		-DBUILD_SHARED_LIBS=OFF \
		-DGGML_NATIVE=OFF \
		-DWHISPER_BUILD_TESTS=OFF \
		-DWHISPER_BUILD_SERVER=OFF \
	&& cmake --build /tmp/whisper-src/build --config Release -j"$(nproc)" \
	&& mkdir -p "${WHISPER_DEST}/include" "${WHISPER_DEST}/lib" \
	&& cp /tmp/whisper-src/include/whisper.h "${WHISPER_DEST}/include/" \
	&& cp -r /tmp/whisper-src/ggml/include/* "${WHISPER_DEST}/include/" \
	&& find /tmp/whisper-src/build -name '*.a' -exec cp {} "${WHISPER_DEST}/lib/" \;

# --- libdave (mirrors Makefile `dave-libs`) --------------------------------
# Installs libdave.so → $HOME/.local/lib, dave.h → $HOME/.local/include, and
# generates $HOME/.local/lib/pkgconfig/dave.pc. NON_INTERACTIVE so the script's
# shell-profile prompt is skipped. Prefers the prebuilt asset, builds from
# source otherwise.
#
# SHELL must be set: the script's final (cosmetic) update_shell_profile step
# reads $SHELL, which is unset under `sh -c` in a Docker RUN, and the script's
# `set -u` would abort with exit 2 — AFTER it has already installed the lib and
# written dave.pc, but still failing the layer. Setting SHELL lets it finish.
RUN git clone --depth 1 https://github.com/disgoorg/godave.git /tmp/godave \
	&& SHELL=/bin/sh NON_INTERACTIVE=1 sh /tmp/godave/scripts/libdave_install.sh "${DAVE_VERSION}"

# --- ONNX Runtime (the exact version the Silero VAD pins) ------------------
# Bundled so GLYPHOXA_ONNX_LIB can point at it and the VAD never reaches the
# network at container start (ADR-0034). Pulled from the same Microsoft release
# pkg/voice/vad/silero/runtime.go resolves; we keep just the lib/ payload.
RUN curl -fsSL \
		"https://github.com/microsoft/onnxruntime/releases/download/v${ONNX_VERSION}/onnxruntime-linux-x64-${ONNX_VERSION}.tgz" \
		-o /tmp/onnxruntime.tgz \
	&& mkdir -p /opt/onnxruntime/lib \
	&& tar -xzf /tmp/onnxruntime.tgz -C /tmp \
	&& cp -P /tmp/onnxruntime-linux-x64-${ONNX_VERSION}/lib/libonnxruntime.so* /opt/onnxruntime/lib/ \
	&& rm /tmp/onnxruntime.tgz

WORKDIR /src

# Warm the module cache on the manifests alone so a code-only change doesn't
# re-download every dependency.
COPY go.mod go.sum ./
RUN go mod download

# The generated protobuf/Connect stubs (gen/, gitignored, ADR-0039) are expected
# to ALREADY exist in the build context — produced on the host by `make proto`
# (which `make docker-build` depends on) or by the CI `proto` job (downloaded as
# the `gen` artifact before the build). They are NOT generated inside the image:
# buf/node deliberately stay out of the builder. .dockerignore does not exclude
# gen/, so this COPY brings them in; the go build below then compiles them.
COPY . .

# Compile the live binary with the production CGO tags and the whisper/dave/opus
# env the Makefile + .goreleaser.yml use. PKG_CONFIG_PATH carries both the
# system libopus (.pc shipped by libopus-dev) and the libdave .pc the script
# wrote under $HOME/.local. ldflags `-s -w` strip debug info, matching goreleaser.
ENV C_INCLUDE_PATH=${WHISPER_DEST}/include
ENV LIBRARY_PATH=${WHISPER_DEST}/lib
ENV PKG_CONFIG_PATH=/opt/build/.local/lib/pkgconfig
RUN go build -tags "opus dave nolibopusfile" -ldflags "-s -w" \
		-o /out/glyphoxa ./cmd/glyphoxa

# ===========================================================================
# Stage: runtime — slim glibc base carrying the binary + its native deps.
# trixie-slim matches the build stage's glibc/libstdc++ (the prebuilt libdave
# needs GLIBC_2.38 / GLIBCXX_3.4.32 at load time, not just link time).
# ===========================================================================
FROM debian:trixie-slim AS runtime
ARG ONNX_VERSION

# Runtime-only system deps: libopus shared lib (the codec links it) and the
# loader bits. ca-certificates so outbound TLS to the providers works. No build
# tooling, no -dev packages.
RUN apt-get update && apt-get install -y --no-install-recommends \
		ca-certificates \
		libopus0 \
	&& rm -rf /var/lib/apt/lists/*

# libdave: copied to /usr/local/lib (a default ldconfig search path) AND to the
# exact $HOME/.local/lib the build stage used, because the binary's baked rpath
# (from dave.pc) names that path. Either resolver then finds it.
COPY --from=build /opt/build/.local/lib/libdave.so /usr/local/lib/libdave.so
RUN mkdir -p /opt/build/.local/lib \
	&& ln -s /usr/local/lib/libdave.so /opt/build/.local/lib/libdave.so

# Bundled ONNX runtime (lib + its versioned soname symlinks).
COPY --from=build /opt/onnxruntime/lib/ /usr/local/lib/

# Refresh the dynamic linker cache so /usr/local/lib libs resolve without
# LD_LIBRARY_PATH. The smoke test's `ldd` assertion verifies this worked.
RUN ldconfig

# The binary itself. On $PATH so `glyphoxa <mode>` / `glyphoxa migrate` just work.
COPY --from=build /out/glyphoxa /usr/local/bin/glyphoxa

# Point the Silero VAD at the bundled runtime so it never downloads one at start
# (ADR-0034). ensureRuntime() short-circuits on this env var.
ENV GLYPHOXA_ONNX_LIB=/usr/local/lib/libonnxruntime.so

# Run as a non-root user (uid/gid 65532, the conventional "nonroot" id). The app
# needs no write access to its own files at runtime; config comes from env.
RUN groupadd --gid 65532 glyphoxa \
	&& useradd --uid 65532 --gid 65532 --no-create-home --shell /usr/sbin/nologin glyphoxa
USER 65532:65532

# Entry is the binary; `mode` and config are args/env at runtime (ADR-0034).
# Default to `all` mode per ADR-0005 (the self-host default); override with e.g.
# `docker run … glyphoxa -mode voice -guild … -channel …` or `glyphoxa migrate up`.
ENTRYPOINT ["glyphoxa"]
CMD ["-mode", "all"]
