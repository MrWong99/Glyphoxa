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

## Implementation notes (lane 2, 2026-11-27)

Decisions the build had to make that the text above implies but does not
spell out. None changes what was decided; each says how it holds.

- **The own-Tenant workspace is re-authorized per request too.** Lane 1
  enforced "operating a Tenant on an `allowlist` deployment needs a Platform
  Admin or an Access Grant" at the OAuth callback only — enough while a revoked
  GM could not sign in at all. Guests are admitted on every deployment, so a
  revoked GM who is somebody's guest CAN sign in, and a session that then fell
  back to "their own Tenant" would undo the revocation. `auth.Workspaces`
  therefore applies the same rule on every workspace resolution
  (`storage.ResolveWorkspace(…, ownTenantNeedsGrant)`); `open` mode is
  unaffected. For the same reason the bundle import mount became
  `TenantRequired` and imports into the Tenant the policy authorized: it used
  to look up "the Tenant this user operates" by itself, which a revoked GM
  still has.
- **A join whose token names a campaign invitation is terminal.** It is
  decided before the Platform Admin and self-signup steps and never falls
  through to them: the redeemer becomes a guest or the join fails, so a dead
  campaign link cannot found a Tenant on an `open` deployment. A Platform Admin
  or grant holder who opens one becomes a guest for that session and reaches
  their own Tenant from the workspace switcher.
- **In `open` mode a Campaign Guest who operates no Tenant signs in as that
  guest**, not as a self-signup — otherwise returning to the Campaign they were
  invited to would found a Tenant.
- **The Tenant's own operator cannot be a guest of its Campaigns** — refused
  on add, on redemption (as an invalid link) and ignored at resolution.
- **An archived Campaign is not reachable for its guests**; the guest rows are
  kept and unarchiving restores them.
- **`ListSessions` needs `transcripts.read` OR `highlights.read`.** It carries
  session metadata only, and Highlights are listed per session. For a guest
  the metadata stops at the status: `end_reason` is blanked, because it holds
  the host's operational detail (raw error text, the spend-cap cause).
- **Prep boards need `wiki.edit`, even to read.** Boards are the GM's working
  surface and a board's name can be a spoiler; the web only shows them to a
  guest who may edit. Tags on public entries stay readable with `wiki.read`.
- **Archiving a Campaign clears the session selections pointing at it**, so
  unarchiving does not move a session back into it. The guest rows are kept.
- **Known limit: a RUNNING Voice Session is not stopped by a revoke or by
  switching Platform Keys off** — keys are resolved once at session start, so
  the session keeps what it started with until it ends; suspending the
  operator and the session's own stop are the tools, and no new session
  starts.
- **`planning.use` does not reveal the spoken record.** Without
  `transcripts.read` the exchange runs with no transcript-reading tool
  (`search_transcripts`, `appearances_of`, `recap`). Its one write,
  `propose_knowledge`, lands in the GM's Proposals queue.
- **A Highlight's excerpt stays visible with `highlights.read`**: it captions
  the clip that right lets the guest play. The speakers' Discord ids and the
  classifier score do not.
- **Pins show their entry's name and type to a `maps.read`-only guest**; the
  unpinned-entries tray carries entry bodies and needs `wiki.read`.
- **The Party Marker has no RPC**, so `maps.edit` is the three Pin writes.
- **The live SSE transcript relay is GM-only**; a guest reads a session, live
  or finished, through the snapshot.

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
  By-id writes are scoped server-side and the cache is dropped on recovery,
  but a write that names no id — creating an entry or a board, renaming a tag
  — lands in the other Workspace when the user holds `wiki.edit` there.
  Archiving no longer opens this window from the host's side (the selection
  is cleared, see above); closing it entirely needs
  the client to state the Workspace it expects per request and the server to
  refuse a mismatch — the shape the "workspace header" alternative above
  rejected as an *authorization* mechanism, used only as an assertion.

## Review hardening (2026-11-27)

Holes found reviewing the build, and how each was closed. None changes the
model above; each makes a sentence in it true.

1. **`open` mode: being added as a guest no longer blocks self-signup.** A GM
   adds a guest by Discord User ID without that person's consent, possibly
   before they ever signed in. The sign-in rule "a Campaign Guest who operates
   no Tenant signs in as that guest" therefore let any GM keep anybody from
   founding a Tenant, with no way out for the person or a Platform Admin. It
   now applies only to someone who **joined** — at least one reachable guest
   row of theirs came through a campaign invitation link. Someone merely added
   self-signs-up like everyone else and finds the shared Campaign in the
   workspace switcher. `allowlist` deployments are unchanged: there the guest
   row is the only thing that admits them. A guest who was added by ID and
   later opens a link of that Campaign that would have admitted them (live,
   not pinned to someone else, not refused to them) has joined from then on:
   the row is linked to that invitation, with no use counted and their rights
   untouched.
2. **Revoking a grant is complete.** "Revocation is immediate" held for the
   web session only. Now, on an `allowlist` deployment:
   - revoking switches **Platform Keys off** on every Tenant the person
     operates (re-granting re-syncs the switch from the new grant);
   - **GM identity on Discord follows the grant**: the operator binding that
     feeds the slash-command gate and the Butler voice gate lists only
     operators who are Platform Admins or hold an Access Grant, so a revoked
     operator stops being GM within one snapshot refresh (a minute). This
     amends ADR-0055's "a bound operator stays GM whatever the allowlist
     says", which now holds in `open` mode only;
   - **host standing**: a Campaign of a Tenant whose operator is suspended
     (any mode), or is neither Platform Admin nor grant holder (`allowlist`),
     is not reachable for its guests, and its campaign invitation links do not
     redeem or preview. One predicate, applied wherever a guest resolves,
     lists, selects or joins a Campaign. Guest rows are kept, so re-granting or
     un-suspending restores every guest. A Tenant with no operator is
     unaffected.

   The Admin people list counts a person's guest Campaigns by the same
   predicate, so it never shows someone as admitted by a Campaign that does
   not admit them.

   On the web tier the rule reaches storage as a per-process policy set at
   boot from the same Admission Mode and Platform Admin list as the boot
   sweep; `open` mode and `GLYPHOXA_DEV_MODE` require nothing. Voice workers
   (and a standalone voice node) are given neither `GLYPHOXA_OPERATOR_IDS` nor
   `GLYPHOXA_ADMISSION_MODE`, and are not restarted when the web tier's values
   change. They therefore latch nothing at boot: the web tier records its
   effective Admission Mode and a read-only copy of the Platform Admin list at
   every boot (`deployment_settings.admission_mode`, `.platform_admin_ids`),
   and a worker reads both live, in the query. A recorded `open` binds every
   operator; a recorded `allowlist` binds Platform Admins and grant holders;
   a flip in either direction reaches the worker with its next GM refresh (a
   minute). The environment stays the only place either is configured. Until
   a web boot has recorded an admin list, a worker requires nothing rather
   than dropping the deployment's own admin — or every GM.
3. **A removal cannot be undone through any link that existed at the time.**
   A multi-use link stays live for the people it has yet to admit, so a
   revoked GM or a removed guest could simply open one. Revoking a grant now
   records a refusal (`invitation_refusal`, one row per link and person) for
   **every** access invitation existing at that moment, and removing a guest
   for every invitation of that Campaign — whichever way the grant or guest
   row was made, by link or by Discord User ID. Redemption refuses such a
   pair and counts no use; the links keep working for everyone else. What
   lets the person back in is a decision made after the removal: a link
   created afterwards, or an explicit grant / add by Discord User ID.
   Refusals are never cleared — they are inert while the person holds a grant
   or is a guest, and clearing them on a re-grant or re-add would re-open
   every old link at the next removal. The person who removed them may change
   their mind; the removed person cannot change it for them. (Redemption
   locks the invitation `FOR NO KEY UPDATE`, so the refusal insert of a
   concurrent removal does not deadlock against it.) Separately, redeeming a
   campaign link as someone who
   already is a guest of that Campaign is now only a sign-in: it never widens
   or resets the rights their GM last set, and counts no use. (The original
   build merged the link's rights in, which let a narrowed guest restore
   themselves.)
