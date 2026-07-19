# Moving Glyphoxa to the cloud: provider suggestions & cost estimates

The home-lab k3s deployment ([k3s-proxmox.md](k3s-proxmox.md)) and a cloud
deployment run the **same chart with the same values** — the move is a DNS
change and a `helm install` on a different cluster, plus a `pg_dump`/
`pg_restore`. This doc is about picking where, cheaply.

All prices are **approximate, captured 2026-07**, and change; treat them as
order-of-magnitude for comparison, and check current pricing before deciding.

## What Glyphoxa actually needs

- **One always-on amd64 node**, 2–4 vCPU / 4–8 GiB — the `all`-mode web pod +
  Postgres fit comfortably (the voice pipeline needs no GPU). The web
  tier is single-replica by design (ADR-0039), so a multi-node HA cluster buys
  you nothing yet — don't pay for it.
- **Postgres with pgvector** — in-chart StatefulSet (default) or a managed DB
  *that supports the pgvector extension* (verify before committing; most
  managed Postgres offerings support it now, but plans/regions differ).
- **A reachable Ollama server for embeddings** — v1.0 embeds through Ollama
  only (ADR-0011), the image ships none, and the default is loopback: set the
  chart's `ollamaUrl` value (env `GLYPHOXA_OLLAMA_URL`) to a server serving
  `nomic-embed-text`, or semantic memory (L2) stalls with a WARN loop while
  everything else keeps working. See docs/configuration.md.
- **Ingress + TLS** on 80/443 — the chart's existing Traefik/nginx +
  cert-manager path.
- **Modest, latency-sensitive traffic** — Discord voice is ~50–100 kbps Opus
  per active speaker plus provider API calls; a month of heavy sessions is a
  few tens of GB. What matters is round-trip latency to Discord's voice edge
  and to Groq/ElevenLabs/Deepgram — for European players pick an EU region,
  for US players a US one. Egress-metered hyperscalers are fine on volume but
  overpriced anyway (below).

## Recommendations

### 1. Hetzner Cloud + self-managed k3s — the price/performance pick

- **CX32** (4 vCPU shared, 8 GiB, 80 GB NVMe): **~€7–8/mo**; a **CX22**
  (2 vCPU/4 GiB, ~€4/mo) runs it for light use. 20 TB traffic included.
- EU (DE/FI) and US regions; excellent latency to both Discord and the
  provider APIs from either.
- You run k3s exactly as on the Proxmox VM — the entire §2–§7 of the home-lab
  guide applies verbatim, minus DynDNS (you get a stable public IP; point a
  normal A record at it).
- Add-ons worth it: automated snapshots (~20% of the server price), a
  separate volume for Postgres if you outgrow the root disk.
- Caveat: their ARM instances (CAX) are cheaper still, but the published
  Glyphoxa image is **amd64-only** today — stick to CX/CPX, or build your own
  arm64 image (the Dockerfile isn't multi-arch yet, and since the libopus
  encoder revert — ADR-0034 amendment 2026-07-19 — the binary is no longer a
  trivial pure-Go cross-compile: build on an arm64 host or under QEMU, where
  the build stage installs the target arch's libopus).

**Total: ~€5–10/mo.** This is the natural first step off the home lab: same
operational model, real IP, real uptime, no CG-NAT worries.

#### Scripted install & updates

Two scripts automate this exact single-box path (they are generic k3s, but
Hetzner is the deployment they were written for):

- [`deploy/saas/install.sh`](../../deploy/saas/install.sh) — the whole of
  the home-lab guide's §2–§8 on a bare box: prompts for the DNS name, ACME
  email, Discord credentials, Admission Mode and (optionally) a nightly
  `pg_dump` backup directory on disk; installs k3s + helm + cert-manager;
  installs the **latest released** version with chart and image pinned to the
  same tag. Every prompt can be pre-answered with the `GX_*` env var it
  names, so unattended installs work too.
- [`deploy/saas/update.sh`](../../deploy/saas/update.sh) — updates a scripted
  install to the latest release (or `--version vX.Y.Z`): takes a pre-upgrade
  dump when backups are configured, then `helm upgrade`s with the target
  release's own chart — the pre-upgrade migrate hook brings the schema
  current before the new pod rolls. Downgrades are refused (ADR-0055's
  rollback caveat) unless forced.

```sh
# on the fresh server, as root
curl -fsSLO https://raw.githubusercontent.com/MrWong99/Glyphoxa/main/deploy/saas/install.sh
chmod +x install.sh && ./install.sh        # prompts for everything it needs

# any later day
curl -fsSLO https://raw.githubusercontent.com/MrWong99/Glyphoxa/main/deploy/saas/update.sh
chmod +x update.sh && ./update.sh          # latest release + schema migration
```

State lands in `/etc/glyphoxa/` (values file `0600`, install state, backup
manifest); re-running the installer reuses it — secrets, notably
`appSecret`, are never rotated on a re-run (ADR-0004: rotating it strands
every sealed BYOK credential).

### 2. netcup (or similar EU VPS: Contabo, IONOS) — cheapest raw compute

Root/VPS servers with generous specs (~€5–8/mo for 4 vCPU/8 GiB) and no hourly
billing. Same self-managed k3s story as Hetzner. Fine choice if you already
have an account; Hetzner's API/tooling and snapshot story are better.

### 3. Managed Kubernetes with a free control plane — less ops, a bit more €

If you'd rather not own the k3s node itself:

| Provider | Control plane | Cheapest sensible node | Ballpark total |
|----------|--------------|------------------------|----------------|
| **Civo** (k3s-based) | free | ~$10–20/mo (2 vCPU/4 GiB) | ~$15–25/mo |
| **Scaleway Kapsule** | free (mutualized) | DEV1-M ~€10/mo | ~€10–15/mo |
| **DigitalOcean DOKS** | free | $12–24/mo (2–4 GiB) | ~$15–30/mo |
| **OVHcloud MKS** | free | ~€10–15/mo | ~€12–20/mo |

All four hand you a kubeconfig; from there it's the identical
`helm install` + cert-manager + ingress flow. Watch for: forced load-balancer
charges (a cloud LB adds $5–15/mo — on a single-node cluster you can often
use the ingress controller's hostPort/NodePort instead), and block-storage
minimums for the Postgres PVC.

### 4. The hyperscalers (AWS/GCP/Azure) — not recommended at this scale

EKS charges ~$70+/mo for the control plane alone; GKE/AKS have free tiers but
node, LB, and **egress** pricing still lands you at several times the options
above for one small always-on service. Choose them only if you're already
there (credits, existing infra, compliance).

## Managed Postgres, or keep it in-chart?

The in-chart pgvector StatefulSet is fine well past hobby scale **if you keep
backups honest** (nightly `pg_dump` + off-box copy — the CronJob from the
home-lab guide §8). Move to managed Postgres when backups/failover become the
thing you worry about:

- DigitalOcean Managed PG (~$15/mo) and Scaleway both support pgvector.
- Point the chart at it with `postgres.enabled=false` +
  `database.url=postgres://...` — that render path is CI-validated already.
- Check extension support explicitly (`CREATE EXTENSION vector`) before
  migrating; it's the first migration and fails loudly without it.

## Migration from the home lab, concretely

1. Stand up the new cluster; install cert-manager + issuer (home-lab guide §4).
2. `helm install` with your existing values file (new host in `ingress.host`).
3. `pg_dump -Fc` on the old cluster → `pg_restore` into the new DB **before**
   first login (fresh visits would otherwise seed state you'd then collide
   with; restoring over the migrate-hook schema with `--clean` is the easy
   path).
4. Update the DNS record and the Discord OAuth redirect URL to the new host.
5. Keep the old deployment stopped-but-intact for a week before deleting.

Voice Sessions don't survive the move (they're bound to a live process —
ADR-0006 explicitly rejects mid-session migration); schedule the cutover
between game nights.

## See also

- [k3s-proxmox.md](k3s-proxmox.md) — the home-lab deployment and the install
  steps every option above reuses.
- [saas-operations.md](saas-operations.md) — plans, platform keys, and the
  cost report that tells you when hosting bills are covered by subscriptions.
- ADR-0034 (deployment artifacts), ADR-0054 (SaaS foundation).
