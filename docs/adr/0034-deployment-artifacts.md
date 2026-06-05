# Deployment artifacts: one container image (mode as arg), Helm for k8s, systemd unit for self-host

Glyphoxa ships as a **single OCI image** built from the one binary (ADR-0005), with `mode` selected by argument/env at runtime — not separate images per mode. Kubernetes/SaaS deploys via a **Helm chart** that runs migrations as a pre-deploy hook; the self-host target gets a **systemd unit** running `all` mode. Both honour ADR-0031's migration-running split exactly.

## What this decides

- **Image:** one multi-stage `Dockerfile`. The build stage compiles with the live tags (`-tags "opus dave nolibopusfile"`) and installs libopus/libdave; the runtime stage is a slim base carrying those shared libs + the binary + embedded migrations (ADR-0031) + embedded Silero model + the SPA assets. Entry is `glyphoxa`; `mode` and config come from args/env. Tagged by version + git SHA.
- **Self-host (the v1.0 target):** a **systemd** unit running `glyphoxa -mode all`. `all` mode auto-applies migrations at startup under the advisory lock (ADR-0031), so the operator's whole story is "point it at a Postgres URL + provider keys, `systemctl start`." A `compose.yml` (app + Postgres+pgvector) is provided as the zero-to-running on-ramp. Secrets come from the OS keyring / env, never baked into the image (consistent with the keyring work and the plaintext-ConfigMap finding being fixed in infra).
- **Kubernetes / SaaS path:** a **Helm chart**. Migrations run **once** as a Helm pre-install/pre-upgrade **hook Job** (`glyphoxa migrate up`) — NOT as an init-container on every replica, and the serving Deployments (`web`, `voice`) do **not** auto-migrate (ADR-0031: they assume a current schema and fail fast if behind). The chart can later split `web` into `gateway`+`voice` roles (ADR-0005's scale path) without changing the image. pgvector Postgres is a chart dependency/external value.

## Why

ADR-0005 already committed to one binary with modes and "no audio across process boundaries"; the artifact layer must not re-fragment that — one image with `mode` as a parameter keeps build/scan/sign surface single and lets the same artifact run `all` on a hobbyist box and `web`+`voice` in a cluster. ADR-0031 already decided *when* migrations run per mode; Q18's only real freedom is *where the migrate invocation lives* in each deploy, and the answer is dictated by 0031: startup-auto for `all`/systemd, a one-shot pre-deploy Helm hook for the multi-replica path. Putting migrate in a Helm hook (not an init-container) is the specific detail that keeps N booting replicas from racing — the advisory lock makes it *safe*, but the hook makes it *run once and observably*. Helm + systemd (rather than only one) matches the dual audience the product targets: self-hosters who want `systemctl`, and operators who want `helm upgrade`.

## Considered options

- **Per-mode images** (`glyphoxa-web`, `glyphoxa-voice`) — rejected. Triples build/scan/publish surface and contradicts ADR-0005's single-binary intent; the libopus/libdave layer would be duplicated anyway since `voice` needs it.
- **Migrate as an init-container on every replica** — rejected. Every replica racing the migration is exactly what ADR-0031's advisory lock guards against, but it turns a one-time schema change into N contending startups and muddies "did the migration run?"; a single pre-deploy hook Job is cleaner and observable.
- **k8s-only (no systemd/compose)** — rejected. The v1.0 audience is self-hosters (ADR-0001); requiring Kubernetes to run one bot is hostile to that target.
- **Distroless/scratch runtime** — deferred, not chosen: the CGO deps (libopus, libdave, ONNX runtime) need a glibc base; a slim glibc image now, distroless revisited if the CGO surface shrinks.
