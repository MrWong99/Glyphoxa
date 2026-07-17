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

The migrate hook runs before the new pod rolls (pre-upgrade hook, ADR-0034);
`all` mode uses a `Recreate` strategy, so expect a brief (seconds) outage per
upgrade — schedule around live Voice Sessions.

## 8. Backups

The in-chart Postgres is a plain StatefulSet on a local-path PV — **one disk,
one VM**. Two layers, use both:

1. **Proxmox level:** schedule VM backups (vzdump) or ZFS snapshots of the VM
   disk. Crash-consistent, catches everything.
2. **Logical dumps** for point-in-time restore and migration off the VM:

```yaml
# pgdump-cronjob.yaml — nightly logical dump into its own PVC
apiVersion: batch/v1
kind: CronJob
metadata:
  name: glyphoxa-pgdump
  namespace: glyphoxa
spec:
  schedule: "15 4 * * *"
  concurrencyPolicy: Forbid
  jobTemplate:
    spec:
      template:
        spec:
          restartPolicy: OnFailure
          containers:
            - name: pgdump
              image: pgvector/pgvector:pg17
              command: ["/bin/sh", "-c"]
              args:
                - pg_dump "$GLYPHOXA_DATABASE_URL" -Fc
                  -f "/backup/glyphoxa-$(date +%F).dump"
                  && find /backup -name '*.dump' -mtime +14 -delete
              env:
                - name: GLYPHOXA_DATABASE_URL
                  valueFrom:
                    secretKeyRef:
                      name: glyphoxa        # the chart's app Secret
                      key: database-url
              volumeMounts:
                - name: backup
                  mountPath: /backup
          volumes:
            - name: backup
              persistentVolumeClaim:
                claimName: glyphoxa-pgdump
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: glyphoxa-pgdump
  namespace: glyphoxa
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 10Gi
```

Copy the dumps off the VM regularly (rsync to a NAS, restic to any S3) — a
backup on the same physical disk is not a backup. Restore with
`pg_restore -d "$DSN" --clean --if-exists <file>.dump`.

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

- The operator allowlist (ADR-0041) is your admission control: nobody outside
  `GLYPHOXA_OPERATOR_IDS` can complete a login, even with the console reachable
  from the internet. Keep it tight.
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
