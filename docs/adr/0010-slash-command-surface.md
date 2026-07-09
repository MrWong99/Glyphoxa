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

## Amendment: `/glyphoxa recap` (2026-07-09, #273)

The command surface gains a seventh command:

- `/glyphoxa recap [session] [delivery]` — GM only; recaps a Voice Session of the Active Campaign (#252/#271, ADR-0040).

Behavior:

- **Session:** with no `session` option it recaps the Active Campaign's **latest ENDED** Voice Session (the running row, while live, is skipped — this is not the "latest session", which would be the running one). An explicit `session` option (autocompleted from the campaign's ended sessions, value = session UUID) recaps that session, provided it belongs to the Active Campaign; a foreign or unparsable id is an ephemeral error and the engine is not called.
- **Active Campaign** is resolved by the SAME strict shared slash resolver as `/glyphoxa start`/`search` (ADR-0009: live session's campaign → durable `/glyphoxa use` selection → fail; no most-recently-created fallback).
- **Delivery** (invoker's choice per request, #271 decision 6): `voiced` — Butler speaks it in the voice channel (requires a live session with the Butler present; today the Butler is never voiced (ADR-0009/0024), so a voiced request DEGRADES to public text with a hint); `public` — public in-channel text; `ephemeral` — GM-only, the DEFAULT. The delivery vocabulary lives in the presence tier, deliberately NOT in proto (the RPC recap #274 is a separate surface).
- **ACK:** the handler Defers (matching the final reply's visibility, decided before the Defer) and runs the LLM recap under its own timeout, since the Defer stops the first-response watchdog. A slow/failed recap surfaces the generic friendly ephemeral failure message.
- **Over-length:** a recap past Discord's 2000-char cap is delivered as multiple ordered Followups (rune-safe boundaries, same visibility), NEVER truncated.
