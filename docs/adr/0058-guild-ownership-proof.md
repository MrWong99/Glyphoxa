# Guild-ownership proof for Discord settings and the guild release path

#483 made guild binding first-registrar-wins (a partial UNIQUE index on
`deployment_config.guild_id`), which closed silent rebinding but opened a squat:
any authenticated tenant could bind a guild_id it does not administer — first,
before the legitimate owner — and the owner then hit "already linked by another
tenant" with no recourse. #504 closes both halves: a proof that the saver
administers the guild before it binds, and a release path so a legitimate
transfer between tenants is possible without DB surgery.

## What this decides

- **SaveDiscordSettings proves guild administration before ANY write.** Binding
  a guild_id requires the authenticated saver (`auth.CurrentUser`'s session
  principal — never an env allowlist, ADR-0055) to be the guild's owner or hold
  a role carrying ADMINISTRATOR (0x8) or MANAGE_GUILD (0x20). The proof runs
  before the token save too, so a rejected request mutates nothing — and the
  `ErrGuildTaken` "another tenant" message is only reachable by PROVEN guild
  admins, so it leaks no cross-tenant existence to strangers.
- **`internal/discordguild` mirrors `internal/discordinvite` exactly**
  (ADR-0047 pattern reuse, amendment noted there in spirit): plain `net/http`
  (never disgo's rest client — goroutine leak per call), 15s client timeout,
  `Bot` auth header, package-private `checkAdmin` with a base-URL +
  `export_test.go` seam. Two REST calls: `GET /guilds/{id}` (owner_id + role
  table; owner short-circuits — owners may hold no explicit role) and
  `GET /guilds/{id}/members/{userID}` (role ids; the implicit @everyone role —
  id == guild id — is unioned in). The `permissions` field is a decimal STRING
  (`strconv.ParseUint(.., 10, 64)`). No third REST call: the role table already
  rides the guild read.
- **The check token is the same token that will serve the guild.** Ladder:
  request-plaintext `bot_token` (pre-seal) > stored BYOK token (decrypted
  server-side only, ADR-0004) > the central `DISCORD_BOT_TOKEN` env token
  (`SetEnvBotToken`, wired at boot) — so BYOK and central-token tenants are both
  covered. All empty → `FailedPrecondition` "save the Discord bot token first".
  There is deliberately NO user-OAuth-token path: the auth tier discards the
  Discord access token after login, and the Bot must be in the guild anyway to
  serve it.
- **Error granularity avoids a membership oracle.** Bot cannot read the guild
  (403 *or* 404 — Discord is inconsistent for a non-member Bot, ADR-0047
  precedent) → `FailedPrecondition` "the Bot is not a member of that server".
  User-not-in-guild and member-without-permission collapse to ONE
  `PermissionDenied` message ("you need the Manage Server permission in that
  Discord server to link it"), so the RPC cannot be used to probe who is in
  which guild.
- **Release is a dedicated RPC, not an empty-ID save.** `ReleaseDiscordGuild`
  frees the caller's own binding; present-but-empty IDs on Save stay rejected
  (#142 posture: an empty ID on the wire is an accident). The request must echo
  the currently-bound guild_id and storage does an atomic compare-and-clear
  (`WHERE tenant_id = $1 AND guild_id = $2 AND guild_id <> ''`) — no
  read-then-write race, no schema change (guild_id `''` is already the
  unconfigured state, migration 00037). Mismatch or no binding →
  `FailedPrecondition`. **No Discord proof on release**: the operator may have
  lost guild access entirely, and release only touches the caller's own
  tenant-local row. A transfer is: A releases, B saves with proof.
- **Release reconciles the standing presence.** After a successful release the
  health cache is busted and `refreshPresence` fires (ADR-0057: the guild
  binding is the Guild→Tenant routing truth), tearing down the freed guild's
  standing client — otherwise the old tenant's presence squats the guild the
  next tenant just claimed. A release during a live Voice Session rides the
  existing reconcile semantics; no new handling.
- **SaaS-first: the proof is enforced in BOTH Admission Modes, no self-host or
  dev bypass.** Where ADR-0039's single-operator convenience implied "the
  operator configures their own deployment, no proof needed", this narrows it:
  a single-operator self-host must also have the Bot in the guild and hold
  Manage Server there to bind it. This is deliberate — multi-tenant SaaS is the
  standard posture, and mode-conditional security checks are how bypasses ship.
  (Under `GLYPHOXA_DEV_MODE` the synthetic dev operator has no real Discord
  snowflake, so an ID-binding save fails the proof; dev flows that need a bound
  guild seed the DB directly, as the k3d scripts already do via Helm values.)
- **TOCTOU accepted.** The proof is checked at save time; a saver who later
  loses Manage Server keeps the binding until released. Discord permissions are
  live state — continuous re-verification would mean polling every bound guild
  forever for marginal gain. The victim's recourse is Discord-side (kick the
  Bot) plus release/rebind, which the proof now protects.

## Considered and rejected

- **Proof via the saver's OAuth access token** (`GET /users/@me/guilds`) — the
  auth tier deliberately discards the Discord access token after login
  (cookie-session posture, ADR-0016); persisting user tokens to check guilds
  widens the secret surface for a check the Bot token already serves.
- **Release as SaveDiscordSettings with empty IDs** — reopens the #142
  silent-wipe accident class; an explicit, echo-confirmed RPC cannot fire from
  a half-loaded form.
- **Proof on release too** — locks out any operator whose guild access was
  revoked (the exact moment they should clean up), for no security gain: release
  only clears the caller's own row.
- **Self-host / allowlist-mode bypass** — see SaaS-first above; rejected per
  operator directive (2026-07-20).

## Relationship to other ADRs

ADR-0047 (client shape, error-collapse precedent, no-disgo rule — reused
verbatim), ADR-0004 (Bot token decrypted server-side only, never on the wire),
ADR-0055 (saver identity = session principal; enforced in both Admission
Modes), ADR-0057 (guild binding is routing truth; release refreshes presence),
ADR-0033 (all Discord behind seams; the default suite stays keyless), ADR-0039
(single-operator convenience narrowed as recorded above).
