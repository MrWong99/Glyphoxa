# SaaS foundation: Plans as synced data, Subscriptions with price snapshots, a durable Usage Ledger, and platform keys on the env-fallback path

Glyphoxa's next deployment target after the proven self-host on-ramp (ADR-0034)
is a hosted SaaS: first on a k3s cluster in the operator's home network
(exposed via DynDNS), later on a cloud provider. BYOK (ADR-0004) stays fully
supported; additionally a subscription may **include** Groq/ElevenLabs usage on
the deployment's own keys. Tier structure is undecided by design, so tiers must
be pure configuration — easy to add, edit, and retire — and their **cost and
revenue must be measurable**. Decided with the operator 2026-07-17; this ADR
records the shape. Payment-processor integration is explicitly OUT of this
slice.

## What this decides

- **Plans are data, not code: a `plan` table synced from a declarative JSON
  catalog.** `glyphoxa billing plans-sync -file plans.json` upserts tiers by
  `slug` (the stable handle) inside one transaction; `-archive-missing`
  archives tiers absent from the file. Plans are **never deleted** — archived
  ones accept no new subscriptions but keep resolving for history. A plan
  carries `display_name`, `description`, `monthly_price_usd`,
  `key_source ('byok'|'platform')`, `included_usage_usd` (platform-only monthly
  estimated-USD allowance), and a free-form `limits` jsonb bag so future knobs
  (max campaigns, feature flags) need no schema churn. The Helm chart renders
  the same catalog from values into a ConfigMap and syncs it via a hook Job
  (weight -3, after migrate/seed) — editing a tier is a `helm upgrade`.
- **Subscriptions snapshot price at bind time.** `tenant_subscription` binds a
  Tenant to a plan with `plan_slug` + `monthly_price_usd` copied onto the row;
  a partial unique index enforces at most one active (ended_at IS NULL)
  subscription per tenant, and switching plans ends the old row — the row
  history IS the revenue record, immune to later catalog edits. Binding is
  operator-CLI (`glyphoxa billing subscribe|cancel|tenants`): money changes
  hands out of band (payment links, bank transfer) and the operator records
  the result; automating that with a processor (Stripe webhooks → subscribe)
  is a later layer that slots in above these tables without changing them.
- **A durable per-Tenant Usage Ledger is the cost side.** `internal/billing.
  Ledger` implements `observe.UsageSink` and tees beside the recorder and the
  in-memory spend meter at session Start (`observe.TeeUsage` composes; zero new
  pipeline plumbing, the ADR-0045 capture points are untouched). It buckets
  usage by `(tenant, day UTC, component, provider, model)` with quantities AND
  an `estimated_usd` priced at capture time from the ADR-0046 price map
  (exported as `spend.Estimate*USD`), and is flushed once at loop exit through
  the session Manager's new optional `UsageWriter` seam
  (`storage.Store.AddUsage`, upsert-accumulate). Attribution only — it never
  gates; gating stays with the spend meter. Accepted losses, documented:
  a crash loses the unflushed session remainder, and every figure is an
  ESTIMATE (never billing truth). Off-session usage (Recap, Highlight
  enrichment — the `spend.PriceOnly` sites) is **not yet** tenant-attributed;
  it logs per-call spend today and joins the ledger as a follow-up.
- **`glyphoxa billing report [-month YYYY-MM]` is the measurement surface:**
  per tenant, the subscription price snapshot(s) overlapping the month
  (un-prorated, labelled as such) against the ledger's summed quantities and
  estimated cost, with totals and margin. It is deliberately a CLI + SQL story
  for now; RPC/console surfaces come with the multi-tenant web tier.
- **Platform keys ride the existing env-fallback path.** The hybrid BYOK
  policy (ADR-0039: `credentials_last4 = "env"` ⇒ adapter falls back to its
  `*_API_KEY` env var) is mechanically already "the deployment's pooled keys" —
  a `key_source='platform'` plan makes that arrangement a *product*: the
  operator sets GROQ_API_KEY/ELEVENLABS_API_KEY on the deployment (the chart's
  existing Secret values) and subscribed Tenants use env-placeholder Provider
  Configs. **Entitlement enforcement is deferred, deliberately:** in the v1.0
  single-operator web tier (ADR-0039/0041) every Tenant is the operator, so
  there is nothing to enforce against yet. When self-signup lands, enforcement
  belongs at (a) Provider-Config save/resolve — a tenant without a platform
  plan cannot hold an env-placeholder config — and (b) a monthly allowance
  gate over the ledger patterned on the spend-cap mechanics (ADR-0046), both
  reading `included_usage_usd`.
- **Deployment path:** the same Helm chart serves the whole ladder — Docker
  Compose/systemd self-host (unchanged), k3s-on-Proxmox home SaaS
  (docs/deploy/k3s-proxmox.md), cloud k8s later
  (docs/deploy/cloud-providers.md). The known scale ceiling is unchanged and
  honest: one `all`-mode web pod (session backplane deferred, ADR-0039).

## Considered and rejected

- **Plans as code constants** (the prices.go pattern) — rejected: the whole
  point is editing tiers without a deploy; a declarative file syncs from
  GitOps and the DB row is the runtime read.
- **Plan CRUD via RPC/console** — deferred, not rejected: a file + hook Job is
  versionable and reviewable; a UI adds surface before there is a second
  operator to use it.
- **Per-event usage rows** — rejected: unbounded growth for no attribution
  gain; daily (tenant, component, provider, model) buckets answer every
  cost question the report asks. Quantities are stored beside the priced
  estimate so a price-map change never rewrites history.
- **Prometheus as the ledger** — rejected: counters are process-lifetime,
  deliberately tenant-unlabelled (ADR-0032 cardinality bound), and not
  durable. The ledger is a table; the counters remain the live ops view.
- **A `key_source` column on usage_ledger rows** — deferred until per-component
  mixed sourcing (one tenant BYOK for LLM, platform for TTS) needs attribution;
  today the tenant's plan classifies its usage at report time.
- **Stripe (or any processor) in this slice** — rejected for now: it would
  force tier structure decisions the operator has explicitly not made, and the
  snapshot tables are exactly the substrate a processor integration writes to
  later.
- **Enforcing platform-plan entitlement now** — rejected as dead code: the
  single-operator tier has no non-operator tenants to refuse. The enforcement
  seams are named above so the self-signup epic can land it deliberately.

## Relationship to other ADRs

ADR-0004 (BYOK; "operator-pooled keys are a future-additive path" — this is
that path), ADR-0039 (hybrid env-fallback policy; single-operator ceiling),
ADR-0045 (usage capture points the ledger tees into), ADR-0046 (price map,
estimate posture, cap mechanics the future allowance gate mirrors), ADR-0031/
0034 (migration + Helm hook conventions the plans-sync Job follows), ADR-0041
(operator allowlist — the current SaaS admission control).

*Amended by ADR-0055 (2026-07-18), which lands the deliberately deferred
pieces: the two entitlement seams go live in `open` Admission Mode — (a)
provider-key resolution fails closed for a Tenant without an active
`key_source='platform'` Subscription (including the no-config-row path), and
(b) a monthly allowance gate compares month-to-date estimated USD against
`plan.included_usage_usd`, patterned on the ADR-0046 cap mechanics. In
`allowlist` mode both are no-ops — self-hosts keep the hybrid env-fallback
policy with zero subscription rows. The Usage Ledger itself stays
attribution-only ("never a gate" stands): the gate is a separate mechanism
that reads it. And the signup default-plan auto-bind widens the
operator-CLI-only binding surface by exactly one automated caller; snapshot
semantics are unchanged, platform-key Plans remain operator-CLI-assigned.*
