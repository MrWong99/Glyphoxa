# Slash command surface: 6 commands, mixed flat/grouped

High-frequency commands are flat; admin/session commands are grouped under `/glyphoxa`. The v1.0 minimum surface is six commands:

- `/roll <dice>` — anyone, anywhere
- `/say <text> as:<agent>` — GM only, requires active Voice Session
- `/glyphoxa start` — GM only; joins voice, runs DAVE handshake, binds session to Campaign
- `/glyphoxa end` — GM only
- `/glyphoxa search <query>` — GM only; searches Active Campaign's transcripts
- `/glyphoxa use <campaign>` — GM only; sets Active Campaign

Permission checks are server-side against `tenant_members.role`; Discord's command permissions are a UX hint only. Deferred: `/glyphoxa lookup`, `/glyphoxa character-claim`, context menus, player commands beyond `/roll`.

**Why:** Discord-side permission settings can be edited by Guild admins; trusting them would let a Guild admin escalate themselves into GM operations. Server-side check is the only safe place.

## Amendment: standing presence + v1.0 permission mapping (2026-07-04, #102)

- **The command surface lives on one standing shared disgo client.** The `bot.Client` moves out of the per-session `wirenpc` wiring into a boot-owned presence component, created lazily once a Bot token exists in `deployment_config` (the token arrives via the web UI, so "no token yet" is a wait-state, not a crash) and rebuilt when the token changes. The voice `Manager` and the interaction handlers share this one gateway connection — no second client per Voice Session.
- **Commands register per-Guild** (the configured `guild_id`), idempotently at presence start; global registration is deferred with the multi-tenant tier.
- **v1.0 permission mapping:** the `tenant_members.role` check named above does not exist yet. Until it does, "GM only" means *the invoking Discord User's snowflake is on the operator allowlist* (`GLYPHOXA_OPERATOR_IDS`, ADR-0041). `/roll` stays anyone-in-the-configured-Guild as decided above — the server-side check for it validates the interaction's Guild, not the user. *Amended by ADR-0055 (2026-07-18): "GM only" now means the invoking snowflake passes the GM-identity checker — a tenant-bound operator (`tenant.operator_user_id`) or an allowlisted snowflake — pending `tenant_members.role`.*

## Amendment: `/glyphoxa recap` (2026-07-09, #273)

The command surface gains a seventh command:

- `/glyphoxa recap [session] [delivery]` — GM only; recaps a Voice Session of the Active Campaign (#252/#271, ADR-0040).

Behavior:

- **Session:** the default pick and the autocomplete offer **ended sessions with a transcript** (`line_count > 0`, written at close — this skips the running row on top and any empty ended row, un-hiding an older recappable session). With no `session` option it recaps the latest such session. An explicitly given `session` id may target **any** session of the Active Campaign, including a running one (a partial recap) — the slash surface deliberately matches #274's `GenerateRecap` RPC, which allows recapping a live session. It must still belong to the Active Campaign; a foreign or unparsable id is an ephemeral error and the engine is not called. An id whose session has no transcript yields a friendly ephemeral "no transcript to recap" (a normal state, not an error).
- **Active Campaign** is resolved by the SAME strict shared slash resolver as `/glyphoxa start`/`search` (ADR-0009: live session's campaign → durable `/glyphoxa use` selection → fail; no most-recently-created fallback).
- **Delivery** (invoker's choice per request, #271 decision 6): `voiced` — Butler speaks it in the voice channel (requires a live session with the Butler present; today the Butler is never voiced (ADR-0009/0024), so a voiced request DEGRADES to public text with a hint); `public` — public in-channel text; `ephemeral` — GM-only, the DEFAULT. The delivery vocabulary lives in the presence tier, deliberately NOT in proto (the RPC recap #274 is a separate surface).
- **ACK:** the handler ALWAYS Defers ephemerally, so the "thinking…" placeholder is GM-only on every path and an error (always an ephemeral reply) never leaves a dangling PUBLIC placeholder. **(The following claim is SUPERSEDED — see the #335 amendment below: Discord deprecated the first-followup-edits shim, so the first post-Defer reply now routes through `EditOriginal` registry-wide.)** Per Discord's then-documented behavior the FIRST followup after a deferred response EDITS the original placeholder (the ephemeral flag is ignored — the defer's visibility is preserved); only later followups create fresh messages honoring their own flag. So a public/voiced-degraded SUCCESS first CONSUMES the ephemeral placeholder with a short ephemeral note ("Recap posted below.") and THEN posts the recap as real PUBLIC followups — never relying on the placeholder itself carrying public content. The recap runs under its own 120s timeout (the Defer stops the first-response watchdog); a slow recap surfaces a friendly "took too long" reply, other failures the generic one.
- **Over-length:** a recap past Discord's 2000-char cap is delivered as multiple ordered Followups (rune-safe boundaries, same visibility), NEVER truncated.

## Amendment: post-Defer replies route through EditOriginal registry-wide (2026-07-10, #335)

Discord **deprecated the first-followup-edits shim** — the server-side behavior where the first `CreateFollowupMessage` after a deferred response implicitly edited the "thinking…" placeholder. A followup now ALWAYS creates a fresh message honoring its own ephemeral flag; the ONLY way to resolve the deferred placeholder is `EditOriginal` (Edit Original Interaction Response), whose visibility stays fixed to the Defer's.

The presence dispatch layer therefore owns one **registry-wide routing rule**, not a per-command one: after a Defer, the FIRST reply (from any of `Reply`/`ReplyEphemeral`/`Followup`) resolves the placeholder via `EditOriginal`, and every LATER reply is a real `CreateFollowupMessage`. This lives on the `Interaction` (`sendPostDefer`), so every command behaves identically and no handler leaves a dangling placeholder. `/glyphoxa recap`'s public path no longer calls `EditOriginal` by hand — it just posts its short ephemeral "Recap posted below." note (which lands as the placeholder edit) and then the public recap followups.

## Amendment: per-tenant client registry replaces the standing singleton (2026-07-19, #489)

The #102 amendment above put the command surface on ONE standing shared client built from the **globally-newest** `deployment_config` row (tenant-unscoped). Under the multi-tenant tier that is a presence-**hijack**: Tenant B saving Discord settings rebuilt the single client from B's row, tearing down Tenant A's live presence and commands (ADR-0055 open-mode blocker).

Decided:

- **The standing presence is a registry of clients keyed by resolved Bot token**, not a singleton. A per-Tenant `EnsureTenant` reads the **tenant-scoped** `deployment_config` (the global-latest read is gone from the presence path) and builds/reuses one client per distinct token. A per-Tenant ensure/rebuild touches only that Tenant's client, so a Tenant B save can never disconnect Tenant A.
- **Central-token mode**: every Tenant resolves to the same token → ONE shared client serving many Guilds, refcounted — a Tenant swapping to its own BYOK token detaches its ref without closing a client another Tenant still holds. **BYOK mode**: a Tenant's own token gets its own client whose terminal token-death (close 4004 / REST 401, ADR-0043) marks THAT Tenant's integration `failed` (surfaced on the Configuration read) without affecting others.
- **Commands still register per configured Guild** via the idempotent bulk overwrite; a repeat ensure with an unchanged token+Guild re-PUTs nothing, and central-token Tenants share one client without duplicate registration churn.
- **Voice Sessions borrow the Tenant's client from the registry** (`session.ClientSource`) instead of a single shared `ClientProvider`; a session start resolves the client keyed by its own Tenant's token.
- **Interim GM/Guild gate**: with no single configured Guild, the server-side Gate authorizes an interaction from ANY resolved Tenant's Guild (`Clients.KnownGuild`); #490's `TenantResolver` narrows an interaction to its owning Tenant (and restores per-Tenant GM scoping).
