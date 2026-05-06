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
