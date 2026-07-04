# Configuration & self-host setup

This runbook takes a fresh checkout to a working **Operator** login for the
single-operator web tier (ADR-0039), and lists every environment variable the
`glyphoxa` binary reads. The access policy — a **mandatory Discord allowlist,
no trust-on-first-use** — is decided by
[ADR-0041](adr/0041-operator-allowlist-access-policy.md); read it for the *why*.
For the `voice`-only live loop, see
[docs/agents/live-npc-run.md](agents/live-npc-run.md).

## Modes

The binary runs one Mode at a time via `-mode` (ADR-0005):

| Mode | Serves | Needs |
|------|--------|-------|
| `voice` | Discord voice loop only | DB (or `-hardcoded`), `DISCORD_BOT_TOKEN`, provider keys |
| `web` | Web app + admin API | DB, all three `DISCORD_OAUTH_*`, `GLYPHOXA_OPERATOR_IDS` |
| `all` | `web` + the in-process voice loop | everything for `web` and `voice` |

The MVP binary defaults `-mode` to `voice`. The OAuth + allowlist gate below
applies to **`web` and `all`** only; `voice` Mode is unaffected.

## 1. Prerequisites

- **Go 1.26+** and a C toolchain — the build runs with `CGO_ENABLED=1`
  (Makefile).
- **Node.js 20+ and npm** — the operator console is a Vite/React bundle the Go
  binary embeds; without it you get a blank placeholder page (see §3).
- **[buf](https://buf.build/docs/installation)** — the Connect/protobuf stubs
  under `gen/` are generated, not committed.
- **Postgres with the [pgvector](https://github.com/pgvector/pgvector)
  extension available** — the first migration runs
  `CREATE EXTENSION IF NOT EXISTS vector` and fails on a stock Postgres without
  the extension package. Easiest local path:
  `docker run -d -p 5432:5432 -e POSTGRES_PASSWORD=... pgvector/pgvector:pg17`.
- `openssl` (or any source of 32 random bytes) for the app secret.

No local toolchain? The container image (ADR-0034) ships everything prebuilt —
see `deploy/`.

## 2. Configure the environment

Copy the committed template and fill it in — never edit a checked-in secret:

```sh
cp .env.example .env
$EDITOR .env       # set GLYPHOXA_DATABASE_URL; paste `openssl rand -base64 32` into GLYPHOXA_SECRET
source .env        # the template is shell-sourced (export NAME='value')
```

- **Database DSN**: `GLYPHOXA_DATABASE_URL` (or the `DATABASE_URL` fallback);
  both empty is fatal at startup. Example:
  `postgres://glyphoxa:...@127.0.0.1:5432/glyphoxa?sslmode=disable`.
- **App secret** for BYOK-at-rest (ADR-0004): `GLYPHOXA_SECRET`, a base64
  32-byte key from `openssl rand -base64 32`. `glyphoxa seed` (§4) and every
  credential **save** require it; without it the server still boots and
  provider-key reads work, but saves fail (`CodeFailedPrecondition`). Set a
  real one now — the template placeholder is not valid base64.

`.env` (and any `.env.*`) is gitignored; only `.env.example` — placeholders
only — is tracked. The OAuth/allowlist values are filled in by §5–§6.

## 3. Build

```sh
make proto                        # buf generate → gen/ (Go + TS stubs)
(cd web && npm ci && npm run build)   # Vite bundle → internal/spa/dist (go:embed)
make build                        # → bin/glyphoxa
```

Order matters: the Go build imports the generated `gen/` packages, and the web
bundle must exist **before** `make build` embeds `internal/spa/dist`. Skipping
the web step still compiles — the committed placeholder `index.html` satisfies
the embed — but serves a **blank page** instead of the login screen.

## 4. Apply the schema and seed

With `.env` sourced (the seed needs the DSN **and** `GLYPHOXA_SECRET`):

```sh
./bin/glyphoxa migrate up
./bin/glyphoxa seed
```

This seeds the demo Tenant/Campaign/NPC once, idempotently.

## 5. Register a Discord OAuth application

The web login is **Discord-only** OAuth (ADR-0016; Google/GitHub are "coming
soon", disabled). Register one app:

1. Open the [Discord Developer Portal](https://discord.com/developers/applications)
   → **New Application**.
2. **OAuth2** tab → copy the **Client ID** → set `DISCORD_OAUTH_CLIENT_ID`.
3. **Reset Secret** → copy it → set `DISCORD_OAUTH_CLIENT_SECRET`. (Shown once.)
4. **OAuth2 → Redirects → Add Redirect**: enter the callback URL, e.g.
   `http://127.0.0.1:8080/auth/discord/callback`, and **Save**. The path is
   fixed at `/auth/discord/callback`; host/port match where you serve `-web-addr`.
5. Set `DISCORD_OAUTH_REDIRECT_URL` to the **exact same string** you registered
   (scheme, host, port, path all match) — a mismatch fails the OAuth exchange.

## 6. Find your Discord snowflake and set the allowlist

The allowlist is the single gate (ADR-0041): only a listed Discord User can
complete login. There is **no first-login/trust-on-first-use** claim (issue #107
is wontfix), so you must list yourself up front.

1. Discord → **Settings → Advanced → Developer Mode: ON**.
2. Right-click your own user → **Copy User ID** (a 17–19 digit snowflake).
3. Set `GLYPHOXA_OPERATOR_IDS` to it:

   ```sh
   export GLYPHOXA_OPERATOR_IDS='000000000000000000'
   ```

Comma or whitespace separates multiple entries. **Single entry is the intended
use.** Multiple is a documented edge (e.g. a second test account): each entry
claims-or-creates its **own** isolated Tenant — the first to log in claims the
seeded one, later ones get fresh empty Tenants. It is not shared-Tenant access.

## 7. Run in `web`/`all` Mode and log in

```sh
source .env
./bin/glyphoxa -mode web        # or -mode all to also drive the voice loop
```

Open `http://127.0.0.1:8080`, click **Sign in with Discord**, approve the
consent screen. On success you land in the operator console. A Discord User
**not** on `GLYPHOXA_OPERATOR_IDS` is rejected *before* any session or Tenant
write and bounced back to the login screen with a `not_authorized` signal.

## 8. Boot posture: loud fail, and the dev escape hatch

In `web`/`all` Mode the process **refuses to boot** unless either all four gate
variables are usable or dev mode is set:

- Missing **any** of `DISCORD_OAUTH_CLIENT_ID` / `DISCORD_OAUTH_CLIENT_SECRET` /
  `DISCORD_OAUTH_REDIRECT_URL`, **or** a `GLYPHOXA_OPERATOR_IDS` that yields no
  usable allowlist (empty, separators only, or containing a non-numeric entry —
  a pasted username can never match a snowflake) ⇒ a **fatal startup error
  naming the missing variable(s) or bad entries**. This is deliberate: a deploy
  nobody can authorize into must fail loud, not look healthy (ADR-0041 — the
  gate was already closed absent OAuth; this is the operability half).
- `voice` Mode is unaffected by all of the above.

**Local dev opt-out — `GLYPHOXA_DEV_MODE`.** Set it to `1` (any value other
than blank or a falsy spelling — `0`, `false`, `no`, `off` — enables it) to
boot without OAuth:

```sh
export GLYPHOXA_DEV_MODE=1
./bin/glyphoxa -mode web
```

Dev mode auto-authenticates every request as the dev Operator, **forces the
listen address to `127.0.0.1`** (overriding `-web-addr`), and logs a loud
insecure-mode warning. A container port-mapping cannot reach the loopback bind,
which blunts production misuse — but any **same-host** process still can (a
reverse proxy pointed at `127.0.0.1`, a `kubectl port-forward`), so dev mode
additionally **rejects (403) any request carrying proxy headers**
(`X-Forwarded-For` / `X-Forwarded-Proto` / `Forwarded`). **Never set
`GLYPHOXA_DEV_MODE` in production.**

Point dev mode at a **throwaway database**: the dev Operator claims the seeded
Tenant like a first login would. If you later switch the same database to real
OAuth, your first real login takes that Tenant (with everything configured in
dev mode) over from the dev Operator. (This replaces the old manual
DB-session-insert dev flow; the superseded `GLYPHOXA_OPEN_TENANT_CREATION` flag
from ADR-0016 plays no role here.)

## Environment variable reference

Every variable the shipped binary reads. See `.env.example` for a copy-paste
template with placeholders. Provider keys are BYOK (ADR-0004): only the ones a
used provider needs are required.

| Variable | Required | Purpose |
|----------|----------|---------|
| `GLYPHOXA_DATABASE_URL` | all Modes (DB path) | Postgres DSN. Fatal if this and `DATABASE_URL` are both empty. |
| `DATABASE_URL` | fallback | Used only if `GLYPHOXA_DATABASE_URL` is empty. |
| `GLYPHOXA_SECRET` | `seed` + saving BYOK keys | base64 32-byte cipher key (`openssl rand -base64 32`). Empty ⇒ `seed` fails and saves fail; reads still work. |
| `DISCORD_BOT_TOKEN` | `voice` (fatal); web/all fallback | Discord gateway/voice bot token. |
| `DISCORD_OAUTH_CLIENT_ID` | `web`/`all` | OAuth app client ID. |
| `DISCORD_OAUTH_CLIENT_SECRET` | `web`/`all` | OAuth app client secret. |
| `DISCORD_OAUTH_REDIRECT_URL` | `web`/`all` | Must equal the app's registered redirect exactly. |
| `GLYPHOXA_OPERATOR_IDS` | `web`/`all` | Allowlisted Discord snowflakes (comma/whitespace-separated, digits only). Empty, separators-only, or non-numeric entries ⇒ fatal. |
| `GLYPHOXA_DEV_MODE` | never in prod | Non-empty (except `0`/`false`/`no`/`off`) ⇒ OAuth-less local dev on `127.0.0.1` with auto-auth. |
| `GLYPHOXA_LOG_FORMAT` | optional | `json`, or `text` (the default for any other value). |
| `GLYPHOXA_ONNX_LIB` | optional | Explicit path to the ONNX Runtime lib for the Silero VAD. |
| `GROQ_API_KEY` | if Groq used | LLM provider key. |
| `ELEVENLABS_API_KEY` | if ElevenLabs used | STT/TTS provider key. |
| `GEMINI_API_KEY` | if Gemini used | LLM / S2S provider key. |
| `ANTHROPIC_API_KEY` | if Anthropic used | LLM provider key. |

## See also

- [ADR-0041](adr/0041-operator-allowlist-access-policy.md) — operator allowlist, no trust-on-first-use, loud-fail/loopback posture (source of truth).
- [ADR-0039](adr/0039-mvp-ui-backend-single-operator-web-tier.md) — single-operator web tier.
- [ADR-0016](adr/0016-cookies-discord-only-oauth.md) — cookies + Discord-only OAuth.
- [ADR-0004](adr/0004-byok-provider-key-matrix.md) — BYOK provider key matrix.
- [ADR-0034](adr/0034-deployment-artifacts.md) — deployment artifacts (container/Helm).
- [docs/agents/live-npc-run.md](agents/live-npc-run.md) — the `voice`-mode live NPC run guide.
