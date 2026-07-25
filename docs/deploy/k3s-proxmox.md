# Deploying Glyphoxa on k3s (Proxmox home lab, DynDNS + TLS)

This runbook takes the Helm chart (ADR-0034) from "works on a dev cluster" to
an **internet-reachable deployment on a k3s cluster inside a Proxmox VM**,
exposed via DynDNS with Let's Encrypt TLS. It is the first stop on the SaaS
path (ADR-0054): the same chart, values, and operational habits carry over
unchanged to a cloud provider later — see
[cloud-providers.md](cloud-providers.md) for that step.

For plain single-machine self-hosting, Docker Compose or systemd
([configuration.md §9–§10](../configuration.md)) remain simpler and fully
supported; this guide is for running Glyphoxa **as a service for others**.

## Topology

```
Internet ──▶ router (DynDNS name, forwards 80/443)
                 │
                 ▼
   Proxmox VM (Ubuntu 24.04, k3s single node)
                 │
        Traefik (k3s built-in ingress, TLS via cert-manager)
                 │
        ┌────────┴─────────┐
        ▼                  ▼
  glyphoxa-web        glyphoxa-postgres
  (-mode all,         (pgvector StatefulSet,
   1 replica)          local-path PV)
```

Notes on the shape, so nothing here surprises you later:

- **One web pod, `-mode all`, by design.** The v1.0 web tier holds Voice
  Sessions in-process and cross-pod session control is deferred (ADR-0039), so
  the chart pins one replica with a `Recreate` strategy. Scaling out is a
  design change (a session backplane), not a values tweak — don't set
  `replicas: 2` and expect it to work.
- **The chart's voice Deployment stays off.** `voice.enabled=true` runs a
  fixed-guild/channel NPC loop (the demo path); in an `all`-mode deployment the
  web pod drives the voice loop itself, so enable only one of the two.
- **TLS terminates at Traefik** (ADR-0039); the app behind it speaks plain
  HTTP in-cluster.

## Prerequisites

- A Proxmox host with capacity for the VM below.
- A DNS name you control. Either a DynDNS provider (DuckDNS, deSEC,
  dynv6, your registrar's API) or a real domain with a DynDNS-updatable
  record. You need **one hostname**, e.g. `glyphoxa.example.dedyn.io`.
- Router access to forward TCP **80 and 443** to the VM. (80 is needed for
  Let's Encrypt HTTP-01 renewal, not just the first issue.)
- A Discord application for OAuth (and the Bot token) —
  [configuration.md §5](../configuration.md) walks through registering it.
- `kubectl` and `helm` (v3.14+) on your workstation.

## 1. Create the VM

A single k3s node running Glyphoxa (web `all` mode + Postgres) is comfortable
at:

| Resource | Minimum | Recommended |
|----------|---------|-------------|
| vCPU | 2 | 4 |
| RAM | 4 GiB | 8 GiB |
| Disk | 20 GiB | 40+ GiB (Postgres + transcripts/blobs grow) |

In Proxmox: Ubuntu Server 24.04 (or Debian 13) cloud image or ISO, VirtIO
disk/NIC, `qemu-guest-agent` installed, and a **static IP or DHCP
reservation** — the router's port forward must keep pointing at it. Enable
"Start at boot".

```sh
# inside the VM
sudo apt-get update && sudo apt-get install -y qemu-guest-agent curl
sudo systemctl enable --now qemu-guest-agent
```

## 2. Install k3s

k3s ships Traefik as the bundled ingress controller and `local-path` as the
default StorageClass — both are exactly what this deployment uses, so a stock
install is fine:

```sh
curl -sfL https://get.k3s.io | sh -
# kubeconfig for your workstation:
sudo cat /etc/rancher/k3s/k3s.yaml   # copy to ~/.kube/config, replace 127.0.0.1 with the VM IP
kubectl get nodes                    # want: Ready
```

Traefik listens on the node's 80/443 out of the box (ServiceLB), so the
router's port forward terminates at the VM with nothing else to install.

## 3. DynDNS and port forwarding

1. Point your hostname at your public IP and keep it updated. Prefer the
   **router's built-in DynDNS client** (Fritz!Box, OpenWrt, UniFi all have
   one); otherwise run `ddclient` on the VM or a tiny CronJob in the cluster.
2. Forward **TCP 80 → VM:80** and **TCP 443 → VM:443** on the router.
3. Verify from outside your LAN (e.g. phone hotspot):
   `curl -I http://glyphoxa.example.dedyn.io` should reach Traefik (a 404 is
   fine at this point — it means Traefik answered).

> **CG-NAT / DS-Lite warning:** if your ISP doesn't give you a public IPv4,
> inbound port forwarding won't work. Options: ask the ISP for real dual
> stack, use an IPv6-only setup (works if all your users have IPv6), or relay
> through a cheap VPS/Cloudflare Tunnel. This is the single most common
> home-lab blocker — check it before anything else.

### 3b. Alternative: Cloudflare Tunnel (no port forwarding, no static IP)

If inbound ports are not an option — CG-NAT, DS-Lite, a landlord's router, or
simply not wanting to open 80/443 — skip DynDNS, port forwarding **and**
cert-manager entirely and let the chart run a `cloudflared` tunnel instead: it
dials **out** to Cloudflare and forwards requests to the web Service from
inside the cluster (Cloudflare terminates TLS at its edge).

1. In the Cloudflare Zero Trust dashboard: **Networks → Tunnels → Create a
   tunnel** (Cloudflared), copy the tunnel **token**.
2. Values:

   ```yaml
   ingress:
     enabled: false           # nothing inbound to route

   cloudflared:
     enabled: true
     token: "<tunnel token>"  # or existingSecret.name/key for a Secret you manage

   web:
     oauth:
       # NOT derived without an Ingress — set the public origin explicitly
       redirectUrl: "https://glyphoxa.example.com/auth/discord/callback"

   privacyPolicyUrl: "https://glyphoxa.example.com/privacy"
   ```

3. Back in the dashboard, add a **Public Hostname** on the tunnel pointing at
   `http://glyphoxa-web.glyphoxa.svc.cluster.local:8080` (the Service name the
   install notes print), and register the same redirect URL on the Discord
   application.

The voice tier is unaffected either way — Discord voice is outbound-only.

## 4. cert-manager + Let's Encrypt

The chart has an opt-in cert-manager path (`ingress.certManager.*`); install
cert-manager and a ClusterIssuer once per cluster:

```sh
helm repo add jetstack https://charts.jetstack.io --force-update
helm install cert-manager jetstack/cert-manager \
  --namespace cert-manager --create-namespace \
  --set crds.enabled=true
```

```yaml
# clusterissuer.yaml — HTTP-01 through the Traefik ingress class
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: you@example.com          # expiry notices
    privateKeySecretRef:
      name: letsencrypt-prod-account-key
    solvers:
      - http01:
          ingress:
            class: traefik
```

```sh
kubectl apply -f clusterissuer.yaml
```

## 5. Values

Create a namespace and a values file. Secrets belong in a proper secret
manager eventually; for a home lab, a **non-committed** values file with
tight permissions is the pragmatic start (the chart also supports templating
everything from external Secrets — see the comments in `values.yaml`).

```sh
kubectl create namespace glyphoxa
```

```yaml
# glyphoxa-values.yaml — chmod 0600, NEVER commit this file
image:
  tag: v0.2.0                # pin the release you deploy

# openssl rand -base64 32
appSecret: "<base64-32-bytes>"

discordBotToken: "<bot token>"
# BYOK deployment: leave the provider keys empty — Tenants bring their own
# (ADR-0004). Fill them only if this deployment itself provides managed
# provider usage (platform keys, ADR-0054 / saas-operations.md).
elevenLabsApiKey: ""
geminiApiKey: ""
groqApiKey: ""

# Embeddings (semantic memory, L2): the image ships no Ollama and defaults to
# loopback, so point this at a reachable Ollama server serving
# nomic-embed-text — otherwise L2 stalls with a WARN loop (everything else
# keeps working). See docs/configuration.md, "Environment variable reference".
ollamaUrl: "http://<ollama-host>:11434"
# ...or drop ollamaUrl and let the chart run one in-cluster instead:
#   ollama:
#     enabled: true          # Deployment + Service; ollamaUrl derives from it
#     persistence:
#       size: 20Gi           # the model cache — nomic-embed-text is pulled on
#                            # first start, so the PVC saves a re-download

database:
  password: "<generate a real one; URL-safe characters>"

postgres:
  persistence:
    size: 20Gi               # local-path StorageClass by default on k3s

seed:
  enabled: false             # no demo data on a real deployment

voice:
  enabled: false             # the web pod drives the voice loop in `all` mode

web:
  enabled: true
  mode: all                  # web console + in-process voice loop (ADR-0039)
  oauth:
    clientId: "<discord client id>"
    clientSecret: "<discord client secret>"
    # leave redirectUrl empty: with the Ingress enabled it is DERIVED from
    # ingress.host + /auth/discord/callback, so it can never drift
  operatorIds: "<your discord snowflake>"
  resources:                 # `all` mode runs the voice loop in-process
    requests:
      cpu: 500m
      memory: 512Mi
    limits:
      cpu: "2"
      memory: 1Gi

ingress:
  enabled: true
  host: glyphoxa.example.dedyn.io
  className: traefik
  certManager:
    enabled: true
    clusterIssuer: letsencrypt-prod
```

Register the derived redirect URL on the Discord application **exactly**:
`https://glyphoxa.example.dedyn.io/auth/discord/callback`.

## 6. Install

```sh
helm install glyphoxa deploy/charts/glyphoxa \
  --namespace glyphoxa \
  --values glyphoxa-values.yaml
```

What happens, in order: the migrate hook Job applies the embedded schema
(ADR-0031), then the web Deployment starts and `EnsureCurrent` confirms the
schema before serving.

Verify:

```sh
kubectl -n glyphoxa get pods                      # web Running, migrate Completed
kubectl -n glyphoxa get certificate               # READY True (first issue ~1 min)
curl -I https://glyphoxa.example.dedyn.io/        # 200, valid certificate
```

Then open the host in a browser, **Sign in with Discord**, and configure
Provider Configs in the console.

## 7. Upgrades

```sh
# bump image.tag in glyphoxa-values.yaml to the new release, then:
helm upgrade glyphoxa deploy/charts/glyphoxa \
  --namespace glyphoxa --values glyphoxa-values.yaml
```

> On a cloud box installed with
> [`deploy/saas/install.sh`](../../deploy/saas/install.sh), use
> [`deploy/saas/update.sh`](../../deploy/saas/update.sh) instead — it resolves
> the latest release, takes a pre-upgrade dump, and upgrades with that
> release's own chart ([cloud-providers.md](cloud-providers.md)).

The migrate hook runs before the new pod rolls (pre-upgrade hook, ADR-0034);
`all` mode uses a `Recreate` strategy, so expect a brief (seconds) outage per
upgrade — schedule around live Voice Sessions.

## 8. Backups

The in-chart Postgres is a plain StatefulSet on a local-path PV — **one disk,
one VM**. Two layers, use both:

1. **Proxmox level:** schedule VM backups (vzdump) or ZFS snapshots of the VM
   disk. Crash-consistent, catches everything (certificates, tunnel
   credentials, the cluster itself).
2. **Logical dumps** for point-in-time restore and migration off the VM. The
   chart ships these (#520) — turn them on in your values:

```yaml
backup:
  enabled: true
  schedule: "15 4 * * *"     # nightly, cluster timezone
  retentionDays: 14
  persistence:
    size: 10Gi               # its OWN PVC, never the database's volume

  # A dump on the same disk survives a bad migration, not a dead disk. Point
  # this at any S3-compatible bucket (B2, Wasabi, MinIO, S3):
  offsite:
    enabled: true
    bucket: glyphoxa-backups
    endpoint: https://s3.eu-central-003.backblazeb2.com
    existingSecret:
      name: glyphoxa-backup-offsite   # you create this one
```

Each run writes `pg_dump -Fc` into the backup PVC, records it in
`/backup/.latest`, and rotates dumps older than `retentionDays` **after** the
new one succeeds. With the off-site push enabled the dump runs as an
initContainer and the upload follows it, so nothing half-written is ever pushed.

**Rehearse the restore before you need it** — the full procedure (including
restoring into a scratch database first) is in
[backup-restore.md](backup-restore.md). The short version:

```sh
kubectl -n glyphoxa create job --from=cronjob/glyphoxa-backup backup-manual-1
kubectl -n glyphoxa logs job/backup-manual-1
# ...then restore with: pg_restore -d "$DSN" --clean --if-exists <file>.dump
```

[`deploy/saas/install.sh`](../../deploy/saas/install.sh) sets up an equivalent
CronJob for the single-cloud-box topology (writing to a host path instead of a
PVC — on one box that is the disk you have), and
[`deploy/saas/update.sh`](../../deploy/saas/update.sh) triggers a run of it
before every upgrade.

## 9. Monitoring

Both workloads expose `/metrics` on an internal port with `prometheus.io/*`
scrape annotations (ADR-0032) — a stock kube-prometheus-stack or a lone
Prometheus with annotation discovery picks them up with zero chart changes.
Useful once you host other people: the per-provider usage counters
(`glyphoxa_voice_llm_tokens_total`, `…_tts_characters_total`,
`…_stt_audio_seconds_total`) are the live view of what your platform keys are
burning; the durable per-Tenant ledger is the billing-grade view
([saas-operations.md](saas-operations.md)).

## 10. Security posture for an internet-facing home lab

- The operator allowlist (ADR-0041) is your admission control — in the default
  `allowlist` Admission Mode: nobody outside `GLYPHOXA_OPERATOR_IDS` can
  complete a login, even with the console reachable from the internet. Keep it
  tight. Setting `GLYPHOXA_ADMISSION_MODE=open` (chart: `web.admissionMode`,
  ADR-0055) enables self-signup instead — any Discord User may found a Tenant —
  and re-scopes the allowlist to the platform-admin list; only flip it once you
  mean to host strangers.
- Keep the VM patched (`unattended-upgrades`) and k3s current
  (`curl -sfL https://get.k3s.io | sh -` re-runs are in-place upgrades).
- The container is `FROM scratch`, static binary, non-root, read-only rootfs
  (ADR-0034) — the app surface is small; the router and VM are your real
  perimeter. Don't forward anything but 80/443.
- Never set `GLYPHOXA_DEV_MODE` here.

## See also

- [configuration.md](../configuration.md) — every environment variable; the
  chart sets them for you but the reference is there.
- [cloud-providers.md](cloud-providers.md) — moving this exact setup to a paid
  cloud, with provider suggestions and monthly cost estimates.
- [saas-operations.md](saas-operations.md) — running Glyphoxa for paying
  users: Plans, platform keys, cost & revenue measurement.
- ADR-0034 (deployment artifacts), ADR-0039 (web tier), ADR-0041 (allowlist),
  ADR-0054 (SaaS path).
