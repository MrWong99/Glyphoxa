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

## Amendment: the ledger is flushed during the session, and first at its end (2026-11-20, #660)

"Flushed once at loop exit" is superseded. A running session's `billing.Ledger` is now flushed every 60 s while the loop runs and again at loop exit. The accepted loss shrinks from "the unflushed session remainder" (in practice the whole session, since nothing was flushed before its end) to at most one interval of usage, and the plan-allowance gate and the planning chat see a live session's spend with at most that lag. `Ledger.Flush` snapshots, clears and re-merges a failed batch, with one exception that the periodic flush makes matter. The write is an additive upsert, so a batch whose COMMIT was in flight when its deadline fired or its connection dropped may already be in the table; re-merging it would add it a second time on the next tick. Such a batch (`storage.ErrCommitOutcomeUnknown`) is dropped and logged at WARN with its amounts instead: an over-count spends a Tenant's allowance on usage that never happened, which is worse than the under-count. A failure that provably did not commit (the server answered, or the request never left) is still retried.

At loop exit the order is now: flush the ledger, drain the transcript (it yields `line_count`), write the terminal `voice_sessions` row, then the best-effort finalizers (highlights, appearance index, chunk flush), then a second ledger flush for anything those metered. The finalizers each carry their own 5 s budget and together can outlast a termination grace period; a SIGKILL there must cost a convenience, not the session's usage or a row stuck `running`.

The issue's premise that sessions "running concurrently in one tenant" each snapshot the full remaining allowance does not apply: the Manager runs at most one session per Tenant. The real exposure was the crash loss and the planning chat reading a total that excluded the live session.

## Amendment: every paid off-session call goes through one seam (2026-11-20, #658)

"Off-session usage … is **not yet** tenant-attributed; it logs per-call spend today and joins the ledger as a follow-up" is superseded, and with it ADR-0055's "known undercount". The follow-up is done, and it is a rule rather than a list of fixed call sites:

**Every provider call that costs money and runs outside a live Voice Session opens a call on `billing.OffSession`. There is no other way to meter off-session.** `spend.PriceOnly`, which priced a call for a log line and nothing else, is deleted; a new feature that wants a priced recorder has to take the seam, and the seam always does three things together:

1. **Gate.** Before anything is spent it reads the Tenant's monthly allowance (ADR-0055 gate (b), the same `spend.PlanAllowance` the session start uses) and refuses with `billing.ErrAllowanceExhausted`. A read error fails closed. Who is gated is decided exactly as for a session start: the gate is wired only in `open` Admission Mode, and it only binds a Tenant whose Plan carries `included_usage_usd`. A BYOK Plan cannot carry one (the catalog rejects it), so a BYOK Tenant, who pays its own provider, is never refused; neither is anyone in `allowlist` mode.
2. **Ledger.** It tees a `billing.Ledger`, so the call's usage reaches `usage_ledger` under the Tenant, flushed when the call closes, on failure paths too (usage a provider reported before a mid-stream error was still billed). The flush is detached from the request context. One retry is safe because `Ledger.Flush` keeps only a batch that provably did not commit and drops one whose commit outcome is unknown (the #660 rule above).
3. **Price.** It keeps the caps-free meter, so each call site's attribution log line still carries `estimated_usd`.

"The ledger never gates" still stands. The gate is the separate mechanism that reads it; this amendment gives that mechanism its second decision point.

The seam's clients, which is every off-session paid call there is today: the recap engine when no caller meters it (the `/glyphoxa recap` slash command and the `GenerateRecap` RPC), the assist engine (`GeneratePersona`, `GenerateKnowledge`, and the text call map-pin suggestions borrow), generated maps, generated portraits, Highlight image enrichment, Highlight sound enrichment, `PreviewVoice`, and the planning chat (which already did all three by hand and now does them through the seam). A recap run from inside a live session or a planning chat exchange is NOT a client: it charges the caller's recorder (`recap.WithMetering`), which already has a ledger and already passed its own gate, and a second ledger row would bill the Tenant twice. The live Voice Session is not a client either; it keeps its own meter, hard cap and periodic flush. Embeddings run on the deployment's local Ollama, cost nothing, and stay outside.

**Background jobs.** Highlight image and sound enrichment run from the job queue with no request to refuse. On an exhausted allowance the job **skips the paid step and completes**: no provider call, the Highlight stays promoted with its clip and without the image or sound, a WARN line names the Highlight and the Tenant, and the job ends `done`. It does not return an error, because a returned error is retried with backoff, then dead-lettered, and the boot reconciliation sweep treats a dead job as absent and re-enqueues it: an exhausted Tenant would have the runner re-check a refusal on every backoff and every boot until the month turned. The price of `done` is that the sweep counts it as satisfied, so that Highlight does not get an image when the allowance resets. That is the same trade the "image generation is not configured" and "provider returned no image" exits already make. Sound is opt-in and re-requestable, so the GM can ask again next month; a manual "generate image" action for an image-less Highlight would close the gap for images and is not built. An allowance that cannot be READ is not an exhausted one: the job returns the error and retries. `SetHighlightSound`, the RPC that schedules the sound job, checks the allowance itself (`OffSession.Check`) so the GM is told at the click instead of watching a sound that never arrives.

**What the client sees.** Every RPC in front of a seam call maps the refusal to one error: `ResourceExhausted` with the metadata entry `Glyphoxa-Error-Reason: allowance_exhausted`. `ResourceExhausted` alone is ambiguous (a provider's own 429 and the concurrent-session cap use it too), so the web keys its translated sentence on the reason, not on the code or the English message. Discord's `/glyphoxa recap` answers with a plain ephemeral sentence.

**The issue's three open decisions.** (1) Which features count: all of them. A cheap text draft and an image differ in price, not in kind, and a per-feature exemption list is the thing this seam exists to prevent. (2) Refuse or only meter: refuse. (3) A per-Tenant rate limit: not added. The gate reads the flushed ledger, so calls already in flight when the allowance runs out still complete, and concurrent requests can each pass the check before any of them is flushed. The overshoot is bounded by what is in flight at that moment, not by time, which is the same lag the session start has always had. A rate limit would bound it tighter and is a separate decision.

One accepted inaccuracy: the gate is per Plan, not per key. A Tenant on a platform Plan who has saved its own key for one component is still refused once the allowance is spent, and its own-key usage still counts toward it, exactly as in a live session. Splitting attribution by key source would need the ledger to record it.
