# Self-signup: Admission Modes, create-only Tenant provisioning, and the ADR-0054 entitlement gates going live

The hosted-SaaS trajectory (ADR-0054) needs strangers to found their own Tenants;
today ADR-0041's operator allowlist is the sole admission gate and each allowlisted
login claims-or-creates a Tenant. Decided with the operator 2026-07-18; full
grounding, verified code citations, and the adversarial-review findings live in
`docs/devs/self-signup-and-invitations-design.md`. This ADR records the admission,
provisioning, and entitlement decisions. Payment-processor integration stays OUT
(ADR-0054); membership (`tenant_members`) and Player access stay out (the latter is
ADR-0056).

Product-scope decision recorded here: self-signup targets **concurrent voice per
Tenant** — the single-active-session guard (`internal/session`) and the
single-Bot/single-guild presence must be lifted before open signup is sold as a
voice product. The concurrency *shape* (K Voice Sessions in one process vs one
Voice Session per pod plus the ADR-0014/0039-deferred backplane) is deliberately
undecided and gets its own ADR; the design note scopes both shapes.

## What this decides

- **Admission Mode: `allowlist` (default) | `open`.** `allowlist` is exactly
  ADR-0041 — nothing changes for self-hosts. `open` admits any Discord User who
  completes OAuth. The posture is recorded in deployment-persistent state (a DB
  settings row the boot reads), with the env var as the operator-facing switch —
  env-only posture is a rollback trap: an older binary would silently boot in
  allowlist posture, mass-revoke every signup's auth session at the boot sweep,
  and reject their re-login (or refuse to boot entirely if the allowlist is
  empty). The Helm chart grows the corresponding value; `DISCORD_OAUTH_*` stays
  mandatory in both modes (OAuth is the signup mechanism), and only the
  allowlist-nonempty boot requirement is relaxed in `open` mode (empty list logs
  a loud "no platform admins" warning).
- **Provisioning in `open` mode is create-only.** A signup always founds a fresh
  Tenant; it never runs `ResolveOperatorTenant`'s claim-earliest-unbound path, so
  a stranger can never claim the operator's seeded Tenant — ADR-0041's rejection
  of trust-on-first-use carries over intact. Allowlisted logins keep today's
  claim-or-create semantics in both modes. Signup is transactional: Discord User
  upsert → Tenant create → default-plan bind → auth-session mint, all-or-nothing
  (no ownerless half-provisioned Tenants on retry).
- **The allowlist is re-scoped, not retired.** In `open` mode it stops being the
  admission gate and becomes the deployment's platform-administration list (the
  operator's own identity for billing CLI parity and future admin surfaces). Its
  name and exact capabilities are an open knob (design-note D5); what is decided
  is that it survives, because lock-down and platform-admin identity still key
  off it.
- **The boot revocation sweep splits by Admission Mode.** In `allowlist` mode
  `RevokeSessionsOutsideAllowlist` keeps running on every non-dev boot — it is
  the lock-down escape hatch, and flipping `open` → `allowlist` plus a restart
  evicts every signup, exactly as ADR-0041's amendment intends — with one
  carve-out: auth sessions belonging to admitted Linked Players (ADR-0056) are
  exempt, since they are authorized per-request against their links, not the
  allowlist. In `open` mode the sweep must not run (it would log out every
  signup each restart); revocation there is suspension-based
  (`users.suspended_at` or equivalent) **plus per-request authorization
  re-check**, which goes live with this ADR: admission is now runtime-editable,
  which is precisely the trigger ADR-0041's amendment pre-committed to
  ("becomes mandatory the day the allowlist is runtime-editable").
- **Every signup is bound to a default Plan at provisioning.** The slug comes
  from deployment configuration (`GLYPHOXA_SIGNUP_PLAN_SLUG`-shaped), NOT from a
  new field in the plan catalog — the catalog decoder uses
  `DisallowUnknownFields`, so a catalog field would hard-fail the plans-sync hook
  Job on any older binary and abort a Helm rollback mid-flight. An `open`-mode
  boot preflights that the slug resolves to a non-archived Plan (otherwise every
  signup would fail at runtime, after OAuth, forever). The default Plan is a free
  BYOK tier; its name and price are an open knob (design-note D4). This widens
  ADR-0054's operator-CLI-only binding surface by exactly one automated caller —
  the snapshot semantics of `tenant_subscription` are unchanged, and platform-key
  Plans remain operator-CLI-assigned until a payment processor lands.
- **The ADR-0054 entitlement seams go live in `open` mode.** (a) Provider-key
  resolution fails **closed** for a Tenant without an active
  `key_source='platform'` Subscription — including the no-config-row path:
  `ResolveKey(nil)` returning `""` (adapter env fallback) is the same hole as an
  `'env'`-placeholder row and both are refused, otherwise every signup silently
  spends the deployment's `*_API_KEY`s. In `allowlist` mode the hybrid
  env-fallback policy (ADR-0039) is untouched — self-hosts keep working with zero
  subscription rows. (b) A monthly allowance gate compares month-to-date
  estimated USD against `plan.included_usage_usd`, patterned on the ADR-0046
  spend-cap mechanics. The Usage Ledger itself remains attribution-only ("never a
  gate" stands): the gate is a separate mechanism that *reads* the ledger, and
  its known undercount (off-session Recap/Highlight-enrichment spend is not yet
  tenant-attributed — ADR-0054's documented follow-up gap) is documented, not
  fixed here.
- **GM identity moves off the env allowlist.** Of the four
  allowlist-as-authorization sites, the OAuth callback keeps the allowlist as
  *admission* in `allowlist` mode; the three GM-*identity* sites — the
  voice-tier GM speaker gate, the speaker resolver's GM transcript labels, and
  the presence gate's GM-only slash-command check — resolve GM identity from
  the Tenant binding instead (interim:
  `tenant.operator_user_id` → Discord snowflake; Member-Role-based once
  `tenant_members` exists). This amends ADR-0050's "GM identity stays
  operator-allowlist membership" clause (amendment note added there); its
  no-per-session-GM-binding rule stands. Known migration edge: an operator who
  never completed a web login has `operator_user_id = NULL` and needs a
  backfill/fallback before the switch.
- **A hardening prerequisite gates all of this.** No `open` deployment before the
  cross-tenant isolation debt is closed: the tenant-blind active-campaign
  selection/resolution, the id-only campaign Update/Archive/Delete writes, the
  session-start tenant/campaign mix, and the global-latest `deployment_config`
  read (single-Bot presence hijack). Enumerated with citations in the design
  note; tracked as its own epic.
- **Onboarding surface:** `/login` gains signup framing in `open` mode (same
  Discord button); a minimal name-your-Tenant onboarding step (the shape ADR-0016
  originally decided); `GetCurrentUserResponse` grows user and Tenant ids.
  `GLYPHOXA_DEV_MODE` precedence is explicit: dev mode preempts Admission Mode
  entirely (auto-auth + loopback force are the backstop), and the OAuth-based
  signup flow is not exercisable under it.

## Considered and rejected

- **Open admission by default (no mode)** — recreates the fresh-exposed-deploy
  claimability ADR-0041 rejected; self-hosts must opt in to strangers.
- **Retiring the allowlist in `open` mode** — it still anchors platform-admin
  identity and the `allowlist`-mode lock-down sweep; removal buys nothing.
- **Env-only admission posture (no DB record)** — the rollback trap above; an
  invisible posture flip that mass-revokes customers is not an acceptable failure
  mode.
- **A `default_for_signup` field in the plan catalog** — breaks older binaries'
  plans-sync (`DisallowUnknownFields`) exactly when a rollback is in progress;
  the env slug has no cross-version coupling.
- **Claim-earliest provisioning for signups** — hands the seeded Tenant to the
  first stranger; mis-claim recovery is DB surgery (ADR-0041's own argument).
- **Replacing the allowlist sweep with suspension everywhere** — deletes the
  lock-down escape hatch: strangers admitted during an `open` phase are "not
  suspended", and flipping to `allowlist` must still evict them.
- **Enforcing the key-resolve gate in `allowlist` mode too** — breaks every
  existing self-host, whose only Tenant deliberately rides the env fallback with
  zero subscription rows.
- **Google/GitHub signup** — unchanged from ADR-0016: Discord-only in this tier,
  slots stay "coming soon".

## Relationship to other ADRs

- **ADR-0016** — its struck open-tenant-creation clause returns here as `open`
  Admission Mode with create-only semantics (supersession note updated there).
  Cookie sessions, CSRF double-submit, Discord-only OAuth, and the JWT rejection
  all stand; invitation-based admission for Players is ADR-0056.
- **ADR-0041** — superseded *only* for deployments that opt into `open` mode; in
  `allowlist` mode every one of its decisions stands. Its amendment's
  per-request-re-check precondition is triggered and landed here; the boot sweep
  splits by mode as above.
- **ADR-0054** — the two named entitlement seams land here; the signup plan-bind
  widens its binding surface as recorded above; plans stay synced data; the
  ledger stays attribution-only.
- **ADR-0050** — GM-identity clause amended (note added there); Speaker Lanes
  and the no-per-session-binding rule untouched.
- **ADR-0039** — its deliberately thin pass-throughs (the `X-Tenant-Id`
  interceptor, the `/t/:slug` route prefix) and its deferred onboarding begin to
  fill, as does the schema's thin `tenant.operator_user_id` binding
  (`00003_auth.sql`); its single-replica ceiling is the subject of the separate
  concurrency ADR.
- **ADR-0003 / ADR-0056** — Players remain outside all of this; a Player linking
  a Character must never traverse the signup provisioning path (the intent-forked
  callback is specified in ADR-0056).
- **ADR-0034** — the Helm chart grows the Admission Mode value and relaxes the
  `operator-ids` requirement for `open` mode only.
