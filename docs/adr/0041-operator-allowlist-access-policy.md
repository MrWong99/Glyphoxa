# Operator access: mandatory Discord allowlist, no trust-on-first-use

The shipped Discord OAuth login (ADR-0016/0039) authenticates but does not authorize: any Discord User completing the flow is upserted, claims-or-creates a Tenant, and receives a 30-day session (`internal/auth/oauth.go`). This ADR closes that gap with a **mandatory operator allowlist** as the single gate.

## Amendment: boot-time session revocation sweep (2026-07-04, #184)

The allowlist as shipped gated only **new** logins: `storage.AuthenticateSession` never re-checks the owning user against the allowlist, so sessions issued before the gate existed (the pre-gate stranger hole this ADR closes) — or before a snowflake was removed — stayed valid for their full 30-day TTL with no revocation path.

**Decided: revocation is a boot-time sweep.** Every non-dev `web`/`all` boot deletes all sessions whose owner's `discord_user_id` is not on the parsed allowlist (`storage.RevokeSessionsOutsideAllowlist`), immediately after the pool opens. Rationale: the allowlist is parsed at boot, so a restart is already the moment any grant change takes effect — a sweep at that moment makes the restart apply the change *fully*. Side benefit: leftover `GLYPHOXA_DEV_MODE` sessions (the synthetic dev operator is never allowlisted) are flushed by the first real boot. Dev mode skips the sweep (it has no allowlist; an empty list would revoke everything — storage refuses it defensively).

- **Per-request re-check** (Contains() after AuthenticateSession) — rejected *for now*: the only session-minting paths are the gated callback and the loopback-only dev seed, so it adds wiring (allowlist threaded through the interceptor stack + RequireSession, dev-mode carve-out) without closing a real hole. **It becomes mandatory the day the allowlist is runtime-editable** (config UI) — revisit then.
- **Upgrade note**: instances that ran pre-gate builds with OAuth configured should also audit `tenant` for stranger-claimed rows — those are data, not sessions; the sweep does not touch them.

## What this decides

- **`GLYPHOXA_OPERATOR_IDS` is the gate.** A comma/whitespace-separated list of Discord User snowflakes, checked at the OAuth callback. A Discord User not on the list is rejected *before* any session issuance or Tenant write and redirected to the login screen with a `not_authorized` signal.
- **The allowlist is mandatory in `web`/`all` Mode.** A Web Instance refuses to boot unless either (a) all three `DISCORD_OAUTH_*` variables **and** a non-empty `GLYPHOXA_OPERATOR_IDS` are set, or (b) `GLYPHOXA_DEV_MODE` is set. The refusal is a fatal startup error naming the missing variables. `voice` Mode is unaffected.
- **`GLYPHOXA_DEV_MODE` is the dev opt-out — auto-auth plus forced loopback.** When set, the Web Instance boots without OAuth, authenticates every request as the seeded Operator, **forces the listen address to `127.0.0.1`** (overriding any configured address), and logs a loud insecure-mode warning. The loopback force makes production misuse structurally ineffective (a container port-mapping cannot reach a loopback bind). This replaces the manual DB-session-insert dev flow.
- **No first-login-lock (trust-on-first-use).** Rejected: a fresh, exposed deploy would be claimable by the first stranger, and mis-claim recovery is DB surgery (reset `tenant.operator_user_id`, delete sessions). The target operator already registers a Discord OAuth app; copying their own snowflake (Developer Mode → Copy ID) is a smaller hurdle than the race is a risk. Issue #107 is wontfix.
- **Guild-membership as a gate is an explicit non-goal** for v1.0; it may return with the multi-tenant tier.
- **Multiple allowlist entries are allowed; each Operator claims-or-creates their own Tenant.** `storage.ResolveOperatorTenant` stays unchanged: the first entry to log in claims the seeded Tenant, later ones get fresh, empty Tenants (isolated — own provider keys, own deployment config). Intended use is a single entry; multiple entries are a documented edge (e.g. a second test account), not a shared-tenant feature. Shared-Tenant membership waits for `tenant_members` (ADR-0002).

## Corrected premise

Epic #96 claimed a deploy with missing OAuth env is "wide open". It is not: the `auth.Stack` gates every Connect service and the SSE reads regardless, so absent OAuth nobody can *obtain* a session — the deploy is locked, not open. The boot refusal above is therefore an **operability** fix (a deploy nobody can log in to must fail loud instead of looking healthy), while the allowlist is the **security** fix.

## Considered options

- **Allowlist + first-login-lock fallback** (the epic's original shape) — rejected; see above. The convenience win is small for an audience that configures OAuth apps.
- **First-login-lock only** — rejected: lockout/mis-claim recovery is always DB surgery, and there is no declarative record of who the operator is.
- **Boot-only opt-out (gate stands, login impossible)** — rejected for `GLYPHOXA_DEV_MODE`: it would preserve the manual session-insert friction the flag exists to remove.
- **Auto-auth opt-out without the loopback force** — rejected: one mis-set flag in production would serve an unauthenticated operator console.
- **Enforcing exactly one allowlist entry** — rejected: multi-value costs nothing with unchanged tenant machinery and keeps second-account testing possible.

## Relationship to other ADRs

- **ADR-0016** — its open-by-default tenant creation (`GLYPHOXA_OPEN_TENANT_CREATION`) is superseded for the single-operator web tier by this ADR (amendment noted there). Cookie sessions, CSRF double-submit, and Discord-only OAuth stay as decided.
- **ADR-0039** — the single-operator fast-path gains its missing authorization half; the seeded-Tenant claim flow is unchanged.
- **ADR-0003** — unaffected: Players are still not Tenant Members and never appear on the allowlist.
- **ADR-0055 (2026-07-18)** — supersedes the allowlist-as-sole-admission **only** for deployments that opt into `open` Admission Mode (self-signup); in `allowlist` Admission Mode every decision here stands, minus one carve-out: the boot sweep exempts admitted Linked Players' auth sessions (ADR-0056; they are authorized per-request against their links). The amendment's pre-commitment above ("It becomes mandatory the day the allowlist is runtime-editable") is triggered by ADR-0055/0056 — admission and Player links become runtime-editable — and the re-check lands there. The boot sweep otherwise splits by mode (kept here as the lock-down escape hatch; suspension-based in `open` mode). The allowlist itself is re-scoped as the platform-administration list, not retired. Admission via Player Invitation on an `allowlist` deployment is operator-enabled per Tenant only (ADR-0056), never GM-unilateral.
