# =============================================================================
# Multi-stage build for Glyphoxa with native whisper.cpp and libdave bindings
# =============================================================================
#
# whisper.cpp is compiled from source and statically linked into the Go binary
# via CGO. libdave (Discord DAVE E2EE) is downloaded as a prebuilt shared
# library and dynamically linked.
#
# The final image is based on distroless/cc (includes glibc/libstdc++) because
# libdave is dynamically linked. Whisper model files are NOT bundled — mount
# them at runtime via a volume.
# =============================================================================

# ---------------------------------------------------------------------------
# Stage 1: Build whisper.cpp static library
# ---------------------------------------------------------------------------
FROM debian:bookworm-slim AS whisper-build

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
# Stage 2: Download libdave prebuilt shared library
# ---------------------------------------------------------------------------
FROM debian:bookworm-slim AS dave-download

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
# Stage 3: Build Glyphoxa Go binary
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
# Stage 4: Final minimal image
# ---------------------------------------------------------------------------
FROM gcr.io/distroless/cc-debian12:nonroot

COPY --from=dave-download /dave/lib/libdave.so /usr/lib/
COPY --from=build /out/glyphoxa /usr/local/bin/glyphoxa

ENTRYPOINT ["glyphoxa"]
