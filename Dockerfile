# =============================================================================
# Multi-stage build for Glyphoxa with native whisper.cpp, ONNX Runtime, and
# libdave bindings
# =============================================================================
#
# whisper.cpp is compiled from source and statically linked into the Go binary
# via CGO. libdave (Discord DAVE E2EE) and ONNX Runtime (Silero VAD) are
# downloaded as prebuilt shared libraries and dynamically linked.
#
# The final image is based on debian:trixie-slim (glibc 2.38+, libstdc++ with
# GLIBCXX_3.4.32+) because libdave and libonnxruntime are dynamically linked
# and require these library versions. Whisper model files and the Silero VAD
# ONNX model are NOT bundled — mount them at runtime via a volume.
# =============================================================================

# ---------------------------------------------------------------------------
# Stage 1: Build whisper.cpp static library
# ---------------------------------------------------------------------------
FROM debian:trixie-slim AS whisper-build

RUN apt-get update && apt-get install -y --no-install-recommends \
    cmake g++ make git ca-certificates \
    && rm -rf /var/lib/apt/lists/*

ARG WHISPER_CPP_VERSION=master

RUN git clone --depth 1 --branch ${WHISPER_CPP_VERSION} \
    https://github.com/ggml-org/whisper.cpp.git /whisper.cpp

WORKDIR /whisper.cpp

RUN cmake -B build \
    -DCMAKE_BUILD_TYPE=Release \
    -DBUILD_SHARED_LIBS=OFF \
    -DWHISPER_BUILD_EXAMPLES=OFF \
    -DWHISPER_BUILD_TESTS=OFF \
    -DWHISPER_BUILD_SERVER=OFF \
    -DGGML_NATIVE=OFF \
    && cmake --build build --config Release -j$(nproc)

# ---------------------------------------------------------------------------
# Stage 2: Download ONNX Runtime shared library (for Silero VAD)
# ---------------------------------------------------------------------------
FROM debian:trixie-slim AS onnx-download

RUN apt-get update && apt-get install -y --no-install-recommends \
    curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*

ARG TARGETARCH
ARG ONNX_VERSION=1.24.1

RUN ONNX_ARCH="x64" \
    && if [ "${TARGETARCH}" = "arm64" ]; then ONNX_ARCH="aarch64"; fi \
    && curl -fsL "https://github.com/microsoft/onnxruntime/releases/download/v${ONNX_VERSION}/onnxruntime-linux-${ONNX_ARCH}-${ONNX_VERSION}.tgz" \
       -o /tmp/onnxruntime.tgz \
    && mkdir -p /onnx \
    && tar xzf /tmp/onnxruntime.tgz -C /onnx --strip-components=1 \
    && rm -f /tmp/onnxruntime.tgz

# ---------------------------------------------------------------------------
# Stage 3: Download libdave prebuilt shared library
# ---------------------------------------------------------------------------
FROM debian:trixie-slim AS dave-download

RUN apt-get update && apt-get install -y --no-install-recommends \
    curl unzip ca-certificates \
    && rm -rf /var/lib/apt/lists/*

ARG TARGETARCH
ARG DAVE_VERSION=v1.1.0/cpp

RUN DAVE_ARCH="X64" \
    && if [ "${TARGETARCH}" = "arm64" ]; then DAVE_ARCH="ARM64"; fi \
    && curl -fsL "https://github.com/discord/libdave/releases/download/${DAVE_VERSION}/libdave-Linux-${DAVE_ARCH}-boringssl.zip" \
       -o /tmp/libdave.zip \
    && mkdir -p /dave/include /dave/lib /dave/lib/pkgconfig \
    && unzip -j -o /tmp/libdave.zip "include/dave/dave.h" -d /dave/include \
    && unzip -j -o /tmp/libdave.zip "lib/libdave.so" -d /dave/lib \
    && rm -f /tmp/libdave.zip

RUN cat > /dave/lib/pkgconfig/dave.pc <<'EOF'
prefix=/dave
exec_prefix=${prefix}
libdir=${exec_prefix}/lib
includedir=${prefix}/include

Name: dave
Description: Discord Audio & Video End-to-End Encryption (DAVE) Protocol
Version: 1.1.0
Libs: -L${libdir} -ldave -Wl,-rpath,${libdir}
Cflags: -I${includedir}
EOF

# ---------------------------------------------------------------------------
# Stage 4: Build Glyphoxa Go binary
# ---------------------------------------------------------------------------
FROM golang:1.26 AS build

RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc g++ libc6-dev libopus-dev \
    && rm -rf /var/lib/apt/lists/*

# Force static linking for opus (remove .so so linker picks .a).
RUN rm -f /usr/lib/*/libopus.so

# Copy whisper.cpp headers and static libraries from the build stage.
COPY --from=whisper-build /whisper.cpp/include /whisper.cpp/include
COPY --from=whisper-build /whisper.cpp/ggml/include /whisper.cpp/ggml/include
COPY --from=whisper-build /whisper.cpp/build/src/libwhisper.a /whisper.cpp/lib/
COPY --from=whisper-build /whisper.cpp/build/ggml/src/libggml.a /whisper.cpp/lib/
COPY --from=whisper-build /whisper.cpp/build/ggml/src/libggml-base.a /whisper.cpp/lib/
COPY --from=whisper-build /whisper.cpp/build/ggml/src/libggml-cpu.a /whisper.cpp/lib/

# Copy libdave shared library and pkg-config metadata.
COPY --from=dave-download /dave /dave

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ENV C_INCLUDE_PATH=/whisper.cpp/include:/whisper.cpp/ggml/include
ENV LIBRARY_PATH=/whisper.cpp/lib
ENV PKG_CONFIG_PATH=/dave/lib/pkgconfig

RUN CGO_ENABLED=1 go build \
    -o /out/glyphoxa \
    -ldflags='-s -w' \
    ./cmd/glyphoxa

# ---------------------------------------------------------------------------
# Stage 5: Final minimal image
# ---------------------------------------------------------------------------
# Debian Trixie provides glibc 2.38+ and libstdc++ with GLIBCXX_3.4.32+,
# required by the Go binary (built with golang:1.26/Trixie) and the
# prebuilt libdave.so from Discord's releases.
FROM debian:trixie-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -r glyphoxa && useradd -r -g glyphoxa -s /sbin/nologin glyphoxa

COPY --from=dave-download /dave/lib/libdave.so /usr/lib/
COPY --from=onnx-download /onnx/lib/libonnxruntime.so /usr/lib/
COPY --from=build /out/glyphoxa /usr/local/bin/glyphoxa

RUN ldconfig

USER glyphoxa
ENTRYPOINT ["glyphoxa"]
