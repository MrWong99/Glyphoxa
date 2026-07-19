# Deployment artifacts: one container image (mode as arg), Helm for k8s, systemd unit for self-host

Glyphoxa ships as a **single OCI image** built from the one binary (ADR-0005), with `mode` selected by argument/env at runtime — not separate images per mode. Kubernetes/SaaS deploys via a **Helm chart** that runs migrations as a pre-deploy hook; the self-host target gets a **systemd unit** running `all` mode. Both honour ADR-0031's migration-running split exactly.

## What this decides

- **Image:** one multi-stage `Dockerfile`. The build stage compiles with the live tags (`-tags "opus dave nolibopusfile"`) and installs libopus/libdave; the runtime stage is a slim base carrying those shared libs + the binary + embedded migrations (ADR-0031) + embedded Silero model + the SPA assets. Entry is `glyphoxa`; `mode` and config come from args/env. Tagged by version + git SHA.
- **Self-host (the v1.0 target):** a **systemd** unit running `glyphoxa -mode all`. `all` mode auto-applies migrations at startup under the advisory lock (ADR-0031), so the operator's whole story is "point it at a Postgres URL + provider keys, `systemctl start`." A `compose.yml` (app + Postgres+pgvector) is provided as the zero-to-running on-ramp. Secrets come from the OS keyring / env, never baked into the image (consistent with the keyring work and the plaintext-ConfigMap finding being fixed in infra).
- **Kubernetes / SaaS path:** a **Helm chart**. Migrations run **once** as a Helm pre-install/pre-upgrade **hook Job** (`glyphoxa migrate up`) — NOT as an init-container on every replica, and the serving Deployments (`web`, `voice`) do **not** auto-migrate (ADR-0031: they assume a current schema and fail fast if behind). The chart can later split `web` into `gateway`+`voice` roles (ADR-0005's scale path) without changing the image. pgvector Postgres is a chart dependency/external value.

- **The SPA bundle is context-fed, not built in the image (2026-07-04, #114).** The Vite console bundle is produced *outside* the `docker build` — by the CI `web` job (which already builds it for vitest on every PR) or a local make target — and lands in `internal/spa/dist` in the build context, exactly like the gitignored `gen/` proto stubs. A node stage inside the Dockerfile was rejected: it would still not be self-contained (the SPA build consumes the context-fed `gen/` TS stubs anyway), it would compile the SPA twice in CI, and it would put npm's network/cache surface inside the image build. The committed placeholder `internal/spa/dist/index.html` stays as the fresh-checkout fallback so pure-Go builds compile with no node step; image smoke checks must fail when the embedded root is the placeholder.

## Why

ADR-0005 already committed to one binary with modes and "no audio across process boundaries"; the artifact layer must not re-fragment that — one image with `mode` as a parameter keeps build/scan/sign surface single and lets the same artifact run `all` on a hobbyist box and `web`+`voice` in a cluster. ADR-0031 already decided *when* migrations run per mode; Q18's only real freedom is *where the migrate invocation lives* in each deploy, and the answer is dictated by 0031: startup-auto for `all`/systemd, a one-shot pre-deploy Helm hook for the multi-replica path. Putting migrate in a Helm hook (not an init-container) is the specific detail that keeps N booting replicas from racing — the advisory lock makes it *safe*, but the hook makes it *run once and observably*. Helm + systemd (rather than only one) matches the dual audience the product targets: self-hosters who want `systemctl`, and operators who want `helm upgrade`.

## Considered options

- **Per-mode images** (`glyphoxa-web`, `glyphoxa-voice`) — rejected. Triples build/scan/publish surface and contradicts ADR-0005's single-binary intent; the libopus/libdave layer would be duplicated anyway since `voice` needs it.
- **Migrate as an init-container on every replica** — rejected. Every replica racing the migration is exactly what ADR-0031's advisory lock guards against, but it turns a one-time schema change into N contending startups and muddies "did the migration run?"; a single pre-deploy hook Job is cleaner and observable.
- **k8s-only (no systemd/compose)** — rejected. The v1.0 audience is self-hosters (ADR-0001); requiring Kubernetes to run one bot is hostile to that target.
- **Distroless/scratch runtime** — deferred, not chosen: the CGO deps (libopus, libdave, ONNX runtime) need a glibc base; a slim glibc image now, distroless revisited if the CGO surface shrinks.

---

**Amendment (2026-07-16, pion/opus + dave-go migration):** the image build no
longer installs libopus or libdave; the live tags are `-tags "opus dave"`
(pure Go) and the runtime stage carries exactly one native lib — the dlopen'd
ONNX runtime for the Silero VAD. The trixie base is no longer forced by
libdave's glibc 2.38 requirement (kept as current stable). The
distroless/scratch deferral above should be revisited once the ONNX/CGO
dependency falls (tracked as the pure-Go Silero forward-pass follow-up): with
it gone the binary can build `CGO_ENABLED=0`, statically linked, `FROM scratch`.

**Amendment (2026-07-16, #468 pure-Go Silero — the runtime stage is `FROM scratch`):**
the deferral above is resolved. The Silero VAD now runs as a bespoke pure-Go
forward pass of the upstream "op18 ifless" export (embedded via go:embed, like
the migrations and the SPA), so the last CGO dependency is gone: the binary
builds `CGO_ENABLED=0`, statically linked. The runtime stage is `FROM scratch`
carrying exactly two files — the static binary and the CA bundle
(`/etc/ssl/certs/ca-certificates.crt`, copied from the build stage for outbound
provider TLS). No glibc base, no shared libs, no ldconfig, no shell; tzdata is
not included (the app does no zone-local time math) and the non-root user is
the numeric `USER 65532:65532` (scratch has no /etc/passwd; none is needed).
`GLYPHOXA_ONNX_LIB` and the runtime download/dlopen machinery are deleted, the
Helm chart's `voice.onnxLib` value with them. The container smoke test now
asserts the static property directly (ldd: "not a dynamic executable", no
`/bin/sh` in the image) and CI's fast gate builds the production binary
`CGO_ENABLED=0` to keep the property from regressing.

**Amendment (2026-07-19, outbound Opus encoder back to statically-linked libopus):**
the pion/opus half of the 2026-07-16 pure-Go migration is partially reverted.
pion/opus's v0.1 encoder plateaus at ~4.1 dB aligned SNR on real speech
regardless of bitrate (measured on the hello-test clip, decoded by reference
libopus) vs ~6.0 dB for libopus at its VoIP defaults — an audibly metallic
NPC voice. The OUTBOUND encoder therefore returns to hraban/opus (system
libopus, CGO, `nolibopusfile` companion tag) until pion/opus reaches
speech-quality parity; the INBOUND decode stays pion/opus (RFC-conformance-
tested upstream, feeds only VAD/STT). A tagged SNR quality gate
(`TestPlaybackSource_SpeechQualityGate`, floor 5.5 dB) pins the encode path
against future silent regressions. The runtime stage stays `FROM scratch`:
the image build installs `libopus-dev` + `pkg-config` in the build stage and
links fully statically (`CGO_ENABLED=1`, `-extldflags "-static -lm"`, plus
`netgo osusergo` so the pure-Go net/user resolvers are used — static glibc
getaddrinfo would dlopen NSS libs absent from scratch). The live tags are
`-tags "opus dave nolibopusfile"`; the smoke test's static assertion (ldd:
"not a dynamic executable") is unchanged and CI's fast gate mirrors the
static CGO build.
