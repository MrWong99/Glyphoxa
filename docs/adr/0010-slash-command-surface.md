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
- **v1.0 permission mapping:** the `tenant_members.role` check named above does not exist yet. Until it does, "GM only" means *the invoking Discord User's snowflake is on the operator allowlist* (`GLYPHOXA_OPERATOR_IDS`, ADR-0041). `/roll` stays anyone-in-the-configured-Guild as decided above — the server-side check for it validates the interaction's Guild, not the user.
