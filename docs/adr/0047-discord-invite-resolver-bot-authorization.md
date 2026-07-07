# Discord invite resolver and bot-authorization surface

Implementing E7 (#101/#105/#110) required deciding the wire contract for invite resolution, the shape of the Discord REST client, error semantics, and the bot-authorization URL. The operator delegated these decisions to the implementation run (2026-07-07); this ADR records them.

## What this decides

- **The client owns link parsing; the server receives a bare `invite_code`.** The #101 client-side parser (`web/src/lib/discordLink.ts`, zero network) grows an invite branch (`discord.gg/{code}`, `discord.com/invite/{code}`, scheme/subdomain/trailing-slash/query tolerant); `ResolveGuildInvite` validates `^[A-Za-z0-9-]{2,64}$` and path-escapes. URL-parsing bugs stay client-side and testable in vitest.
- **`internal/discordinvite` mirrors `internal/discordtag` exactly**: plain `net/http` (not disgo's rest client — its rate limiter leaks a goroutine per call), 15s client timeout, `Bot` auth header, package-private `resolve` with a base-URL + `export_test.go` seam. Flow: `GET /invites/{code}` → guild; `GET /guilds/{id}/channels` → keep only type-2 `GUILD_VOICE` (stage channels excluded for MVP), sorted by position then name.
- **Error semantics:** invite 404 or guild-less (group-DM) invite → `ErrNotFound` → gRPC `NotFound` ("invalid or expired"); channels 403 *or* 404 → `ErrNoAccess` → `FailedPrecondition` ("the Bot is not a member of that server" — Discord is inconsistent about which code it returns for non-member guilds, so both map identically); no saved bot token → `FailedPrecondition` with a distinct message. The RPC is `NO_SIDE_EFFECTS` (CSRF-exempt read, same precedent as `GetProviderHealth`); the deployment Bot token is decrypted server-side and never crosses the wire.
- **The bot-authorization link (#110)** is built client-side from a server-provided application id: `ListProviderConfigsResponse.discord_application_id` (non-secret, sourced from `DISCORD_OAUTH_CLIENT_ID`, empty when unset → disabled control with note). Scope is **`bot applications.commands`** — not bare `bot` — because Glyphoxa registers `/glyphoxa` slash commands (E5) that would otherwise be invisible in freshly added guilds. Permissions constant: View Channel + Connect + Speak = `3146752`; a mute bot that can only connect fails its purpose.
- **UI fill path:** picker selections and link autofill go through the Configuration screen's dirty-tracking edit path (`idsDirty`), never raw state set — otherwise a config refetch clobbers the fill. Failed resolves leave fields and any previous picker untouched.

## Considered and rejected

- **Sending the full invite URL to the server** — moves parsing behind the RPC boundary where UI iteration is slowest; the client already owns link parsing for #101.
- **disgo rest client for the two REST calls** — known goroutine leak per call (documented at `discordtag.go`); plain HTTP matches the established seam pattern.
- **Including stage channels** — a Voice Session has no stage semantics (speaker requests); revisit on demand.
- **A dedicated RPC for the application id** — one non-secret string on an existing read response is strictly simpler.

## Relationship to other ADRs

ADR-0016 (the operator-login application backs the bot-authorization URL), ADR-0033 (live Discord behind seams, keyless default suite), ADR-0004/0039 (token sealed at rest, decrypted server-side only), ADR-0014 (management RPC surface).
