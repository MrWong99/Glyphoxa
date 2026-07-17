# Running Glyphoxa as a SaaS: Plans, platform keys, cost & revenue

This runbook covers operating a Glyphoxa deployment that hosts **paying
users**: defining subscription tiers (Plans), including provider usage in a
subscription (platform keys), binding Tenants to Plans, and reading cost and
revenue out. The design and its deliberate boundaries are
[ADR-0054](../adr/0054-saas-plans-platform-keys-usage-ledger.md).

**Current honest scope, before you sell anything:**

- The web tier is still the single-operator tier (ADR-0039/0041): every login
  must be on the operator allowlist, and each allowlisted Discord User gets an
  isolated Tenant. "SaaS" today means *you onboard each customer by adding
  their snowflake to `GLYPHOXA_OPERATOR_IDS` and assigning a Plan*. Self-signup
  is the next epic, not this one.
- There is **no payment processor integration**. You collect money out of band
  (PayPal, bank transfer, Stripe payment links) and record the result with
  `glyphoxa billing subscribe`. The tables are processor-ready (price
  snapshots, subscription history), so automation can land later without a
  migration.
- Every cost figure is an **estimate** from the static price map (ADR-0046) —
  good for attribution and margin sanity, never an invoice.

## 1. Plans (tiers)

Tiers are pure configuration: a JSON catalog synced into the `plan` table.
Nothing about their structure is hardcoded — add, edit, or retire tiers by
editing the file and re-syncing.

```json
{
  "plans": [
    {
      "slug": "byok-free",
      "display_name": "BYOK Free",
      "description": "Bring your own Groq/ElevenLabs keys.",
      "monthly_price_usd": 0
    },
    {
      "slug": "all-inclusive",
      "display_name": "All Inclusive",
      "description": "Groq + ElevenLabs usage included, on our keys.",
      "monthly_price_usd": 20,
      "key_source": "platform",
      "included_usage_usd": 15,
      "limits": { "max_campaigns": 10 }
    }
  ]
}
```

Field notes:

- `slug` — the stable handle; syncs upsert by it, subscriptions snapshot it.
  Lowercase alphanumerics + hyphens.
- `key_source` — `byok` (default): the Tenant saves its own provider keys.
  `platform`: the Tenant runs on the deployment's env keys (§2) and the
  subscription price covers the usage.
- `included_usage_usd` — the monthly estimated-USD usage allowance a platform
  tier includes. Today this is **reporting information** (the report shows
  usage against it); automated gating on it is a named follow-up (ADR-0054).
- `limits` — a free-form bag for future per-tier knobs. Consumers read the
  keys they know; unknown keys are inert, so you can annotate tiers ahead of
  enforcement.

Sync and inspect:

```sh
glyphoxa billing plans-sync -file plans.json            # upsert by slug
glyphoxa billing plans-sync -file plans.json -archive-missing
glyphoxa billing plans-list
```

`-archive-missing` archives tiers you removed from the file. Archived plans
accept no new subscriptions but existing ones keep running — plans are never
deleted, so revenue history always resolves.

**On Kubernetes**, the chart does this for you: put the same catalog under
`plans.catalog` in your values file and set `plans.enabled=true` — a hook Job
runs the sync on every `helm upgrade` (see `deploy/charts/glyphoxa/values.yaml`
for the annotated example). Tier edits become values edits.

## 2. Platform keys ("usage included")

Mechanically, platform keys are the **env-fallback path** that already exists
(ADR-0039's hybrid policy): a Provider Config whose credential is the `env`
placeholder makes the adapter read the deployment's own `GROQ_API_KEY` /
`ELEVENLABS_API_KEY` / `GEMINI_API_KEY`. For a SaaS deployment:

1. Set the provider keys on the deployment (the chart's
   `groqApiKey`/`elevenLabsApiKey`/`geminiApiKey` values, or the env of the
   systemd/compose service). These are YOUR keys — the platform pool.
2. Give paying Tenants a `key_source: "platform"` plan.
3. Their env-placeholder Provider Configs (the seeded default) now run on your
   pool; the Usage Ledger (§3) attributes every token/character/second they
   burn to their Tenant, so you can see each subscription's real cost.

BYOK Tenants coexist on the same deployment: they save real keys in the
console (encrypted with `GLYPHOXA_SECRET`, ADR-0004), and a real saved key
always wins over the env fallback.

> **Deferred, deliberately:** nothing yet *prevents* a Tenant without a
> platform plan from using env-placeholder configs — in the single-operator
> tier every Tenant is you, so there is nobody to refuse. Before opening
> self-signup, the entitlement checks land at Provider-Config save/resolve and
> as a monthly allowance gate (ADR-0054 names the seams). Until then, don't
> hand out logins you wouldn't trust with your keys.

Per-session guardrails already exist today: set per-Tenant **spend caps**
(soft/hard, ADR-0046) so a runaway Voice Session stops itself.

## 3. Cost: the Usage Ledger

Every Voice Session started from the web tier accumulates its metered usage
(LLM tokens, TTS characters, STT seconds — ADR-0045) into per-Tenant daily
buckets and flushes them to the `usage_ledger` table at session end. This is
automatic — no configuration. Notes:

- Buckets are `(tenant, day UTC, component, provider, model)` with raw
  quantities AND a USD estimate priced at capture time; price-map updates
  never rewrite history.
- A crash loses the unflushed remainder of a live session (estimates-only
  posture; the Prometheus counters still moved).
- Off-session LLM spend (Recap, Highlight enrichment) is logged per call but
  not yet in the ledger — a named follow-up.

Ad-hoc SQL is fair game for anything the report doesn't cover, e.g. platform
cost by provider for a month:

```sql
SELECT provider, component,
       SUM(estimated_usd)     AS est_usd,
       SUM(llm_input_tokens)  AS in_tok,
       SUM(llm_output_tokens) AS out_tok
  FROM usage_ledger
 WHERE day >= '2026-07-01' AND day < '2026-08-01'
 GROUP BY provider, component
 ORDER BY est_usd DESC;
```

## 4. Revenue: subscriptions

```sh
glyphoxa billing tenants                                  # find tenant ids + current plans
glyphoxa billing subscribe -tenant <uuid> -plan all-inclusive
glyphoxa billing cancel -tenant <uuid>
```

`subscribe` snapshots the plan's slug and current price onto the subscription
row and ends any previous subscription — the row history is the revenue
record. Editing a plan's price later affects only *new* subscriptions; if you
want existing customers on the new price, re-`subscribe` them (that's a
deliberate, visible act, not a silent repricing).

## 5. The monthly report

```sh
glyphoxa billing report -month 2026-07
```

Per tenant: plan, revenue (price snapshot, un-prorated), estimated provider
cost, and the usage quantities behind it; then totals and margin. Use it to
answer the two questions that decide tier structure: *what does a typical
campaign's month actually cost me* and *which tiers are underwater*. Because
plans are data, adjusting a tier in response is a catalog edit + sync.

A tenant that switched plans mid-month shows one line per subscription (usage
attached to the first, so totals never double-count). Un-prorated revenue is a
deliberate simplification for now — the subscription rows carry exact
started_at/ended_at timestamps, so proration is a query change later, not a
schema change.

## 6. Operational checklist for going paid

- [ ] Spend caps set for every hosted Tenant (ADR-0046) — your hard backstop.
- [ ] Provider dashboards (Groq/ElevenLabs) checked against the ledger's
      estimates for the first month — calibrate expectations; the price map is
      an estimate by design.
- [ ] Backups running and restore-tested ([k3s-proxmox.md §8](k3s-proxmox.md))
      — the ledger and subscription history are now business records.
- [ ] `plans.json` (or the Helm values catalog) in version control.
- [ ] Terms with your users about recording/consent (the Rollover Tape is
      consent-gated by design, ADR-0051 — point users at it).

## See also

- [ADR-0054](../adr/0054-saas-plans-platform-keys-usage-ledger.md) — the
  design, its boundaries, and the named follow-ups.
- [k3s-proxmox.md](k3s-proxmox.md) — the home-lab deployment this operates on.
- [cloud-providers.md](cloud-providers.md) — moving to a paid cloud.
- [configuration.md](../configuration.md) — env vars, OAuth, allowlist.
