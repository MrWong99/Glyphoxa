# Web-managed access: Platform Admins in the environment, Access Grants and Campaign Guests in the database

Until now the only way to let anyone into an `allowlist` deployment was to edit
`GLYPHOXA_OPERATOR_IDS` and restart, and nobody but a Tenant's single operator
could open a Campaign at all. Decided with the operator 2026-11-26: every level
of access below the instance's own administrators is managed from the web UI.
This ADR builds the two lanes that makes possible and supersedes the parts of
ADR-0056 they replace. It follows the SaaS-first direction: where a self-host
assumption and a multi-tenant one disagree, multi-tenant wins.

## What this decides

### Three levels, three places they are configured

| Who | Where configured | What they can do |
|---|---|---|
| **Platform Admin** | environment only (`GLYPHOXA_OPERATOR_IDS`) | everything a GM can in their own Tenant, plus the Admin screen: admit and remove people, suspend, decide who spends Platform Keys |
| **GM** (a Tenant's operator) | admitted by a Platform Admin: an **Access Grant** or an **access invitation** link (or `open` self-signup, ADR-0055) | owns a Tenant; manages **Campaign Guests** of their own Campaigns |
| **Campaign Guest** | by the Campaign's GM: added by Discord User ID, or a **campaign invitation** link | exactly the **Guest Rights** granted, on that one Campaign |

`GLYPHOXA_OPERATOR_IDS` keeps its name and its parsing; ADR-0055 already
re-scoped it to "the platform-administration list" and this ADR makes that its
only meaning. Platform Admins are deliberately NOT manageable from the web: the
one credential that can admit everyone else must not be editable by a
compromised browser session. In `GLYPHOXA_DEV_MODE` the synthetic dev operator
is a Platform Admin so the Admin screen is reachable on a dev instance.

### Lane 1 — instance access

- **Access Grant** (`access_grant`, keyed by Discord snowflake so it can be
  written before the person ever signs in). On an `allowlist` deployment the
  OAuth callback admits, in order: a Platform Admin (unchanged claim-or-create
  path) → a holder of an Access Grant → someone redeeming a live access
  invitation (which creates their grant) → an existing Campaign Guest (lane 2)
  → nobody else. In `open` mode grants are not consulted; self-signup admits.
- **A granted user founds their own Tenant, create-only** — the ADR-0055
  signup transaction without a Plan bind. They never claim the seed Tenant
  (that stays the Platform Admin's ADR-0041 path), and land on the
  name-your-Tenant onboarding step exactly like an `open` signup.
- **Platform Keys are a per-Tenant switch, default OFF for granted users.**
  `tenant.platform_keys` gates the env-key fallback on `allowlist` deployments
  (ADR-0054 seam (a)); it defaults true so every existing Tenant is unchanged,
  and a Tenant founded through a grant takes the grant's value. Before this,
  `allowlist` mode granted every Tenant the fallback — fine with one operator,
  an open wallet the moment a second person is admitted. `open` mode keeps its
  Plan-based gate untouched.
- **Invitation links are `/join/<token>`** for both kinds (one `invitation`
  table, `kind` = `access` | `campaign`). 256-bit `crypto/rand` token, stored
  hashed, shown once at mint. Optional: label, max uses, expiry, a pinned
  Discord User. The token rides a short-lived HttpOnly cookie across the OAuth
  round trip — never the `state` nonce (ADR-0056's reasoning stands).
  Redemption locks the row and counts the use in the same transaction as the
  grant. A join requires the Nutzungsbedingungen/Datenschutz acknowledgment
  in every Admission Mode (the #518 gate, extended): an invited person is a new
  account holder regardless of how the deployment admits.
- **Revocation is immediate.** Revoking a grant deletes the user's sessions;
  the callback refuses them from then on. Their Tenant and data are kept, so
  re-granting restores everything. Suspension (ADR-0055) now works in every
  Admission Mode and is what a Platform Admin uses on a GM they did not grant.
  The `allowlist` boot sweep keeps sessions of grant holders and Campaign
  Guests; it still evicts everyone else.
- **No impersonation.** The Admin screen lists every Tenant and its operator
  and can cut them off, but a Platform Admin cannot open another Tenant's
  Campaigns from it. (They remain GM-on-Discord in every Tenant, ADR-0055 —
  unchanged.) Reading someone else's table is a privacy decision this ADR does
  not make.

### Lane 2 — Campaign Guests

- **A Campaign Guest is a third principal**, not a Member: no Member Role, no
  Tenant, no Subscription (ADR-0002/0003/0054 untouched). `campaign_guest`
  binds (campaign, user) to a set of Guest Rights.
- **Guest Rights replace ADR-0056's Player Access Levels.** A fixed
  three-step ladder could not express "may edit the map but not read the
  transcript". The vocabulary (`auth.GuestRight`, mirrored by the proto enum):
  `wiki.read` (public material only), `wiki.secrets`, `wiki.edit`,
  `maps.read`, `maps.edit`, `transcripts.read`, `highlights.read`,
  `planning.use`. Three imply others and the stored set is normalized:
  `wiki.edit` ⇒ read + secrets (an editor blind to private Aspects would erase
  them on save); `maps.edit` ⇒ `maps.read`; `planning.use` ⇒ read + secrets
  (the Butler answers from the whole Knowledge Graph — the right cannot
  honestly be narrower than what it reveals).
- **The workspace is chosen per browser session, authorized per request.**
  `sessions.guest_campaign_id` records what the session acts as; the policy
  seam joins it against `campaign_guest` on every request. A stale selection
  stops resolving. A user with no Tenant of their own resolves to their most
  recently added guest Campaign. When acting as a guest, the request's Tenant
  is the HOST Tenant and the Active Campaign is pinned to the guest Campaign
  on every surface — the three-step Active Campaign walk never runs.
- **Guests are denied by default, in one place.** The policy seam (#446) holds
  a table mapping each guest-reachable procedure and byte mount to the right
  it needs; anything not in the table is `PermissionDenied` for a guest before
  the handler runs. A new RPC is therefore GM-only until someone decides
  otherwise, and a drift test fails when a procedure is in neither set.
  Handlers reached by a guest additionally: scope any by-id read to the guest
  Campaign (several are tenant-scoped today, which is not enough once a
  principal exists that is narrower than a Tenant); withhold `gm_private`
  Nodes, private Aspects and Edges touching them without `wiki.secrets`;
  serve promoted Highlights only; and scope planning threads to their creator.
- **Guests are admitted on every deployment.** ADR-0056 made invitation
  admission on `allowlist` deployments an operator opt-in per Tenant. Dropped:
  a guest founds nothing and can spend nothing except through `planning.use`,
  which their GM granted knowingly; and the Platform Admin sees every guest on
  the Admin screen and can suspend any of them.
- **ADR-0056's share-transcripts toggle is dropped too.** Granting
  `transcripts.read` IS the explicit GM action ADR-0051 asks for; a second
  switch saying the same thing only creates states where one is on and the
  other off.
- **Not built here:** binding a guest to their Character
  (`character.linked_user_id`) and the `own-character` slice it would enable;
  per-speaker text consent; guests on Discord (a guest is never a GM for the
  Butler voice gate or slash commands); a guest founding their own Tenant
  later on an `open` deployment.

## Considered and rejected

- **Managing Platform Admins from the web** — see above.
- **`tenant_members` with Member Roles as the vehicle** — still the right
  answer for a second GM-tier human who should administer a Tenant; wrong for
  "let my players read the wiki". Remains a separate future ADR.
- **A workspace header or path segment chosen by the client per request** —
  every existing RPC and byte mount would need to carry it, and a missing one
  would silently act as the wrong principal. A server-side selection re-authorized
  per request needs no client cooperation to be safe.
- **Per-right enforcement inside each handler only** — one forgotten check is a
  leak. The default-deny table makes forgetting safe.
- **Reusing Plans/Subscriptions for the Platform Keys switch** — `allowlist`
  deployments run with zero Plan rows (ADR-0055); a boolean says what is meant.

## Relationship to other ADRs

- **ADR-0056** — superseded in part: Player Access Levels → Guest Rights; the
  share toggle and per-Tenant invitation-admission opt-in are dropped; "Player
  Invitation" → campaign invitation; `max_linked_players` is not consumed. Its
  redemption-security decisions and `/join/<token>` stand and are implemented.
- **ADR-0041** — the allowlist is no longer the sole admission gate of an
  `allowlist` deployment; it is the Platform Admin list.
- **ADR-0055** — Suspension applies in every Admission Mode; the boot sweep
  spares grant holders and Campaign Guests.
- **ADR-0054** — seam (a) gains the per-Tenant flag gate for `allowlist` mode.
- **ADR-0039 / #446** — the policy seam now resolves a workspace, not just a
  Tenant, and owns the guest gate.
- **ADR-0062** — planning threads gain an owner.

## Amendment 2026-11-27 — the Workspace in the URL (web)

Decided while building the guest shell. The decision above stands unchanged:
the Workspace is a property of the server-side session, re-authorized per
request, and no request carries it. What this adds is how the SPA *names* it.

- **The tenant slug in the path names the Workspace.** `/t/default/…` (and any
  other slug, as before — ADR-0039's pass-through) is the user's own Tenant;
  `/t/guest-<campaignId>/…` is that Campaign, opened as its guest. The slug
  authorizes nothing and the server never reads it.
- **The URL is the intent, and the session follows it.** When the path names a
  Workspace the user can enter and the session acts as another, the SPA calls
  `SwitchWorkspace` and re-reads `GetCurrentUser` before mounting anything.
  That one rule is the workspace switcher (which only navigates), a bookmark
  into a shared Campaign, and a second tab taking the session back. A
  guest-only account on an own-Workspace URL (every sign-in lands on
  `/t/default`) has its URL rewritten instead; a URL naming a Campaign the
  user is not a guest of gets a page saying so, with the ways out.
- **The cache never crosses Workspaces.** Requests carry no Workspace, so two
  Workspaces' reads share the same cache keys. The shell is unmounted while
  URL and session disagree, everything but the session probe is dropped once
  they agree, and only then is a fresh shell mounted.
- **Considered and rejected: one fixed slug, with the Workspace known only to
  the session.** A bookmarked guest view would silently reopen as the same
  view of whichever Workspace the session happened to be in — the GM's own
  wiki under a link they saved from someone else's campaign.
- **Known gap:** between another tab switching the session and this tab
  noticing (its next probe: a refused call, a window focus, or the 30-second
  guest poll), a request from this tab is answered for the other Workspace.
  Nothing is written to the wrong place (by-id writes are scoped server-side)
  and the cache is dropped on recovery, but closing the window entirely needs
  the client to state the Workspace it expects per request and the server to
  refuse a mismatch — the shape the "workspace header" alternative above
  rejected as an *authorization* mechanism, used only as an assertion.
