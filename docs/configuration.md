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

The binary defaults `-mode` to **`all`** (ADR-0005/ADR-0034, the self-host
target): a bare `glyphoxa` boots the web tier plus the in-process voice loop and
**auto-applies the embedded migrations at startup** under the advisory lock
(ADR-0031) — no manual `migrate up` step. An explicit `-mode voice`/`-mode web`
still overrides it; `voice` mode continues to demand `-guild`/`-channel`, and a
web-only replica assumes a current schema (it does **not** auto-migrate, so N
replicas never race). The OAuth + allowlist gate below applies to **`web` and
`all`** only; `voice` Mode is unaffected.

The fastest way to a running instance is **Docker Compose** (§9) or the
**systemd** unit (§10), both below. The step-by-step build-from-source runbook
(§1–§8) follows.

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

## 9. Zero-to-running with Docker Compose

`compose.yml` at the repo root stands up a pgvector Postgres and the Glyphoxa
image in the default `-mode all`. Because `all` mode auto-migrates at startup,
`docker compose up` reaches the login screen against a migrated DB with **no
separate migrate step**.

The `glyphoxa` service pulls the published `ghcr.io/mrwong99/glyphoxa` image
(built by `release-image.yml` on each release, ADR-0034), so a machine with
only Docker needs nothing else — no buf, no Node/npm, no local build. The image
is pinned to an explicit version tag (`:v0.2.0`) in `compose.yml`; to upgrade,
bump that tag to the release you want and re-run `docker compose pull glyphoxa`:

```sh
cp .env.example .env
$EDITOR .env        # GLYPHOXA_SECRET, DISCORD_OAUTH_*, GLYPHOXA_OPERATOR_IDS (§5–§6)
docker compose up
```

Then open `http://127.0.0.1:8080` and **Sign in with Discord**.

To run against a source checkout instead of the published image (e.g. testing
an unreleased change), use the `build:` fallback still in `compose.yml`. That
path is **context-fed** (ADR-0034): the gitignored `gen/` proto stubs and the
Vite SPA bundle are produced on the host and shipped into the build context,
not generated inside `docker build`:

```sh
make proto                        # buf generate → gen/  (must exist in the build context)
(cd web && npm ci && npm run build)   # Vite bundle → internal/spa/dist (else a blank page)
docker compose up --build
```

- **Secrets** load from `.env` via the service's `env_file`. Compose strips the
  shell `export ` prefix and quotes, so the same `.env` the source build sources
  works unchanged. The `GLYPHOXA_DATABASE_URL` is overridden in the compose
  `environment:` to point at the in-compose `postgres` service (the `.env` DSN
  targets `127.0.0.1` for a bare-metal run).
- **Postgres data** persists in the named `pgdata` volume across `up`/`down`.
  The DB password defaults to `glyphoxa`; set `POSTGRES_PASSWORD` in the shell
  (or the compose interpolation `.env`) to change it — it feeds both the DB and
  the app DSN. It is spliced raw into the DSN, so keep it URL-safe
  (alphanumerics); a `@`/`:`/`/`/`?` would corrupt the URL unless URL-encoded.
- The app port publishes on `127.0.0.1:8080` (loopback), matching the default
  OAuth redirect. To reach it from a LAN, change the host side of the `ports:`
  mapping and update `DISCORD_OAUTH_REDIRECT_URL` to match.
- **Smoke steps:** `docker compose up` → wait for the app log line that
  the web tier is listening → `curl -fsS http://127.0.0.1:8080/` returns the SPA
  → the browser reaches the login screen. `docker compose down -v` tears it down
  and drops the volume.

## 10. Self-host with systemd

For a bare-metal host, `deploy/glyphoxa.service` runs the binary in `-mode all`
as a non-root user with startup auto-migrate — the full "point it at Postgres +
keys, `systemctl start`" story (ADR-0034).

```sh
# 1. Install the binary (build from source per §3, or copy it out of the image).
sudo install -m 0755 bin/glyphoxa /usr/local/bin/glyphoxa

# 2. Create the non-root service user.
sudo useradd --system --no-create-home --shell /usr/sbin/nologin glyphoxa

# 3. Write /etc/glyphoxa/env in systemd EnvironmentFile format: KEY=VALUE, one
#    per line, NO `export`, NO surrounding quotes (this is NOT the shell .env).
sudo install -d -m 0755 /etc/glyphoxa
sudo tee /etc/glyphoxa/env >/dev/null <<'EOF'
GLYPHOXA_DATABASE_URL=postgres://glyphoxa:CHANGE_ME@127.0.0.1:5432/glyphoxa?sslmode=disable
GLYPHOXA_SECRET=CHANGE_ME_base64_32_bytes
DISCORD_BOT_TOKEN=CHANGE_ME
DISCORD_OAUTH_CLIENT_ID=CHANGE_ME
DISCORD_OAUTH_CLIENT_SECRET=CHANGE_ME
DISCORD_OAUTH_REDIRECT_URL=http://your-host:8080/auth/discord/callback
GLYPHOXA_OPERATOR_IDS=000000000000000000
# Uncomment ONLY if you preinstalled the ONNX Runtime at this path — the value is
# used verbatim with no existence check, so a wrong/missing path breaks the VAD.
# Left commented, the VAD auto-downloads to /var/cache/glyphoxa on first use.
# GLYPHOXA_ONNX_LIB=/usr/local/lib/libonnxruntime.so
EOF
sudo chmod 0600 /etc/glyphoxa/env    # holds secrets

# 4. Install and start the unit.
sudo install -m 0644 deploy/glyphoxa.service /etc/systemd/system/glyphoxa.service
sudo systemctl daemon-reload
sudo systemctl enable --now glyphoxa

# 5. Smoke check.
systemctl status glyphoxa            # want: active (running)
journalctl -u glyphoxa -n 50         # migrations applied + web tier listening
curl -fsS http://127.0.0.1:8080/     # SPA served → login screen reachable
```

Validate the unit file itself (no host state needed) with
`systemd-analyze verify deploy/glyphoxa.service`. The unit is hardened
(`NoNewPrivileges`, `ProtectSystem=full`, `ProtectHome`, `PrivateTmp`). The
service user has no home, so `ProtectHome=true` would block the Silero VAD's
default `$HOME/.cache` download of the ONNX Runtime: the unit therefore sets
`XDG_CACHE_HOME` to a systemd-managed `CacheDirectory` (`/var/cache/glyphoxa`).
Pointing `GLYPHOXA_ONNX_LIB` at a preinstalled `libonnxruntime.so` (as the env
file above does) skips that download entirely — recommended if you run the voice
loop. `/etc/glyphoxa/env` is required (no `-` prefix on `EnvironmentFile`): a
missing file fails the unit up front rather than crash-looping the binary on
absent secrets.

## Session highlights (rollover tape)

Highlights are GM-curated clips cut from a 120-second rolling audio buffer of
a Voice Session ("the rollover tape") — see
[ADR-0051](adr/0051-rollover-tape-consent-retention.md) for the full consent
and retention model. The feature is **off by default** and entirely opt-in per
Campaign. The flow, end to end:

1. **Arm.** Open the campaign menu in the top bar, choose **Campaign
   settings**, flip the **Rollover tape** toggle on, and save. This is a
   Campaign-level GM opt-in — it does not, by itself, record anything.
2. **Consent, at the next session start.** Arming takes effect from the
   **next** Voice Session start, not mid-session: there is no re-post of the
   disclosure into an already-running session. When a session with the tape
   armed starts, the Bot posts an in-channel message with **Consent**/**Revoke**
   buttons. Every human participant decides individually and can revoke later;
   **the GM is not auto-consented** — they must press Consent like anyone else.
   Only consenting speakers' audio ever enters the buffer; non-consenting
   speakers are never captured, even transiently.
3. **Detect.** While the tape is armed and running, a detector flags
   noteworthy moments and cuts them into ephemeral **Highlight Candidates**,
   blob-backed but retained only until the GM reviews them at session end
   (7-day safety auto-purge for anything never reviewed).
4. **Promote.** From the Session screen's Highlights strip, the GM promotes a
   candidate into a durable **Highlight** (kept until explicitly deleted) or
   deletes it. Nothing leaves the instance without an explicit GM
   share action — consent covers capture, the GM gate covers distribution.

If a Voice Session has no Highlights yet, the Highlights strip's empty state
points the GM at the top-bar campaign menu → Campaign settings, where the
**Rollover tape** toggle lives.

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
