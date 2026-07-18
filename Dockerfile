# syntax=docker/dockerfile:1
#
# Glyphoxa — single multi-stage OCI image for the one binary (ADR-0005, ADR-0034).
#
# There is ONE image. `mode` and all config (Postgres URL, provider keys, guild/
# channel, etc.) are supplied at RUNTIME via args/env — there are no per-mode
# images. The build stage compiles the live binary with the production tags
# (`opus dave nolibopusfile`). The outbound Opus encoder is libopus again
# (hraban/opus via CGO — pion/opus's encoder is below speech-quality parity,
# ADR-0034 amendment 2026-07-19), so CGO is ON, but libopus and libc are linked
# STATICALLY (-extldflags "-static") and the binary stays fully static. The
# runtime stage is therefore still FROM scratch (ADR-0034 amendment): the
# static binary plus CA certificates — no libc, no shared libs, no ldconfig,
# no shell.
#
# The embedded Silero model (pkg/voice/vad/silero/data/silero_vad_op18_ifless.onnx),
# the SQL migrations (internal/storage/migrations/*.sql), and the SPA bundle
# are go:embed'd into the binary — they need no separate runtime files.

# ---------------------------------------------------------------------------
# Build args — pinned versions live here so a bump is one obvious edit.
# ---------------------------------------------------------------------------
# Pinned to the exact patch go.mod requires (go-version-file equivalent for the
# image build; golang:1.26-trixie can lag a fresh patch release behind go.dev).
ARG GO_VERSION=1.26.5

# ===========================================================================
# Stage: build — compile the fully static live binary.
# ===========================================================================
FROM golang:${GO_VERSION}-trixie AS build

# CGO on for the libopus outbound encoder (the only native dependency); the
# link below is fully static so the runtime stage stays scratch. The base
# image's ca-certificates are copied into the runtime stage below.
ENV CGO_ENABLED=1

# libopus headers + static archive (libopus-dev ships libopus.a) and pkg-config
# so hraban/opus's `#cgo pkg-config: opus` resolves. The golang base image
# already carries gcc/libc-dev.
RUN apt-get update && apt-get install -y --no-install-recommends \
		pkg-config \
		libopus-dev \
	&& rm -rf /var/lib/apt/lists/*

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

# Compile the live binary with the production tags. `nolibopusfile` keeps
# hraban/opus from also requiring libopusfile (unused). `netgo osusergo` force
# the pure-Go net/user resolvers: statically linked glibc getaddrinfo would
# dlopen NSS libraries that do not exist in a scratch image, silently breaking
# DNS. -extldflags "-static" links libopus/libc statically (`-lm` last resolves
# libopus.a's libm references); `-s -w` strip debug info, matching goreleaser.
RUN go build -tags "opus dave nolibopusfile netgo osusergo" \
		-ldflags '-s -w -extldflags "-static -lm"' \
		-o /out/glyphoxa ./cmd/glyphoxa

# Fail the build immediately if the binary is not fully static (a dynamically
# linked libopus, or a new CGO dependency outside the static link): a static
# binary is the load-bearing property that lets the runtime stage be scratch.
RUN ldd /out/glyphoxa 2>&1 | grep -q 'not a dynamic executable'

# ===========================================================================
# Stage: runtime — FROM scratch (ADR-0034 amendment, #468): the static binary
# plus only what genuinely cannot be embedded.
# ===========================================================================
FROM scratch AS runtime

# CA certificates so outbound TLS to the providers works. The only file the
# image carries besides the binary — tzdata is not needed (the app never does
# zone-local time math) and there is no libc to configure.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt

# The binary itself, at the conventional path (also what container-smoke.sh
# extracts and asserts on).
COPY --from=build /out/glyphoxa /usr/local/bin/glyphoxa

# Run as a non-root user (uid/gid 65532, the conventional "nonroot" id).
# Numeric — scratch has no /etc/passwd, and none is needed: the app looks up
# no user database and needs no home directory (config comes from env).
USER 65532:65532

# Entry is the binary; `mode` and config are args/env at runtime (ADR-0034).
# Absolute path — scratch has no shell and no guaranteed $PATH. Default to
# `all` mode per ADR-0005 (the self-host default); override with e.g.
# `docker run … glyphoxa -mode voice -guild … -channel …` or `glyphoxa migrate up`.
ENTRYPOINT ["/usr/local/bin/glyphoxa"]
CMD ["-mode", "all"]
