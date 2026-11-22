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
| `voice` | Discord voice loop: a claim-plane worker (DB, no `-guild`/`-channel`, ADR-0057) or the legacy standalone node (`-guild`/`-channel` or `-hardcoded`) | DB (or `-hardcoded`), `DISCORD_BOT_TOKEN` (or per-Tenant BYOK tokens + `GLYPHOXA_SECRET`), provider keys |
| `web` | Web app + admin API; Voice Sessions run on `voice` workers via the claim plane | DB, all three `DISCORD_OAUTH_*`, `GLYPHOXA_OPERATOR_IDS` (in `allowlist` Admission Mode) |
| `all` | `web` + the in-process voice loop | everything for `web` and `voice` |

The binary defaults `-mode` to **`all`** (ADR-0005/ADR-0034, the self-host
target): a bare `glyphoxa` boots the web tier plus the in-process voice loop and
**auto-applies the embedded migrations at startup** under the advisory lock
(ADR-0031) — no manual `migrate up` step. An explicit `-mode voice`/`-mode web`
still overrides it; a web-only replica assumes a current schema (it does **not**
auto-migrate, so N replicas never race). The OAuth + allowlist gate below applies to **`web` and
`all`** only; `voice` Mode is unaffected.

The fastest way to a running instance is **Docker Compose** (§9) or the
**systemd** unit (§10), both below. The step-by-step build-from-source runbook
(§1–§8) follows.

## 1. Prerequisites

- **Go 1.27+**.
- **A C toolchain (gcc), `pkg-config` and the libopus headers** — `libopus-dev`
  (Debian/Ubuntu), `opus-devel` (Fedora), `opus` (Arch), or
  `brew install opus pkg-config` (macOS). The voice-capable build
  (`make build-live`, tags `opus dave nolibopusfile`) links the outbound Opus
  encoder against libopus via CGO (ADR-0034 amendment 2026-07-19). DAVE (dave-go)
  and the Silero VAD are pure Go. The untagged `make build` needs none of this —
  and **cannot do voice** (see §3).
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
make build-live                   # → bin/glyphoxa, voice-capable
```

`make build-live` compiles the production tags (`opus dave nolibopusfile`) —
the same as the container image, except that libopus is linked dynamically
here (the Dockerfile links it statically for its `FROM scratch` runtime).
`make build` is the untagged pure-Go build (`CGO_ENABLED=0`): it serves the
console, migrations and the CLI subcommands, but links **stub** Opus and DAVE
code, so every Voice Session fails. A stub binary logs a `stub voice build`
WARN at boot in `voice`/`all` Mode, and `./bin/glyphoxa version` prints
`opus codec: false` / `dave: false` for it. Both targets stamp the binary with
`git describe` (shown by `glyphoxa version` and in the boot log).

Order matters: the Go build imports the generated `gen/` packages, and the web
bundle must exist **before** `make build-live` embeds `internal/spa/dist`. Skipping
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
(`X-Forwarded-For` / `X-Forwarded-Proto` / `Forwarded`). Because your own
browser can also reach the loopback bind on behalf of any page you have open,
dev mode likewise rejects a request whose `Host` is not `localhost` /
`127.0.0.1` / `[::1]` (DNS rebinding) or whose `Origin` is not a loopback
origin (cross-site requests) — so open the instance as `http://localhost:8080`
or `http://127.0.0.1:8080`, not through a custom hostname. **Never set
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
only Docker needs nothing else — no buf, no Node/npm, no local build. The tag
comes from `GLYPHOXA_IMAGE_TAG` and defaults to `latest` (the newest stable
release). Pin a release for reproducible upgrades by setting
`GLYPHOXA_IMAGE_TAG=v0.16.0` in the shell or in `.env` (Compose reads it for
variable substitution), then `docker compose pull glyphoxa`. Never run a
release below v0.16.0:

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
# 1. Install the binary: `make build-live` per §3 (needs the libopus runtime,
#    e.g. libopus0, on this host), or copy the fully static one out of the image:
#    docker create --name gx ghcr.io/mrwong99/glyphoxa:latest && docker cp gx:/usr/local/bin/glyphoxa bin/ && docker rm gx
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
(`NoNewPrivileges`, `ProtectSystem=full`, `ProtectHome`, `PrivateTmp`); the
binary is self-contained (the Silero VAD, migrations and console are embedded —
nothing is downloaded or cached at runtime), so no writable cache directory is
needed. The image's binary is fully static; a `make build-live` binary links
the host's libopus. `/etc/glyphoxa/env` is required (no `-`
prefix on `EnvironmentFile`): a missing file fails the unit up front rather
than crash-looping the binary on absent secrets.

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
used provider needs are required. Embeddings are the exception to the key
matrix: v1.0 embeds through **Ollama only**
([ADR-0011](adr/0011-transcript-chunks-async-embeddings.md)) — no key, just a
reachable endpoint. The default is loopback, so any containerized run (the
image ships no Ollama) must point `GLYPHOXA_OLLAMA_URL` at a real Ollama
server (the Helm chart's `ollamaUrl` value renders it), or semantic memory
(L2) stalls with a WARN loop while everything else keeps working. If the
chart runs Ollama in-cluster (`ollama.enabled: true`), give the pod a real
CPU request via `ollama.resources` — without one it can be throttled to
near-zero under node contention, and speculative memory recall
([ADR-0042](adr/0042-streaming-stt-speculative-memory-recall.md)) falls back
to an inline embedding call under a hard budget (500 ms by default,
`GLYPHOXA_RECALL_BUDGET`; `internal/recall/recall.go`'s `defaultBudget`) that a
CPU-starved pod cannot meet, degrading recall to no-memory on timeout.

| Variable | Required | Purpose |
|----------|----------|---------|
| `GLYPHOXA_DATABASE_URL` | all Modes (DB path) | Postgres DSN. Fatal if this and `DATABASE_URL` are both empty. |
| `DATABASE_URL` | fallback | Used only if `GLYPHOXA_DATABASE_URL` is empty. |
| `GLYPHOXA_SECRET` | `seed` + saving BYOK keys | base64 32-byte cipher key (`openssl rand -base64 32`). Empty ⇒ `seed` fails and saves fail; reads still work. |
| `DISCORD_BOT_TOKEN` | `voice` (fatal); web/all fallback | Discord gateway/voice bot token. |
| `DISCORD_OAUTH_CLIENT_ID` | `web`/`all` | OAuth app client ID. |
| `DISCORD_OAUTH_CLIENT_SECRET` | `web`/`all` | OAuth app client secret. |
| `DISCORD_OAUTH_REDIRECT_URL` | `web`/`all` | Must equal the app's registered redirect exactly. Its scheme also decides the session cookies' `Secure` attribute: `https://…` ⇒ `Secure` cookies, `http://…` ⇒ plain (local dev). |
| `GLYPHOXA_OPERATOR_IDS` | `web`/`all` (`allowlist` mode) | Allowlisted Discord snowflakes (comma/whitespace-separated, digits only). In `allowlist` Admission Mode the sole login gate: empty, separators-only, or non-numeric entries ⇒ fatal. In `open` mode it is the platform-admin list and may be empty. Also a deployment-wide GM override for Slash Commands and the Butler voice gate. |
| `GLYPHOXA_ADMISSION_MODE` | optional | Who may complete the Discord login ([ADR-0055](adr/0055-self-signup-admission-modes.md)): `allowlist` (only `GLYPHOXA_OPERATOR_IDS`) or `open` (any Discord User; each signup founds its own Tenant). Unset ⇒ the posture recorded in the database, `allowlist` on a fresh one. Any other value ⇒ fatal. Flipping `open` → `allowlist` and restarting evicts every signup. |
| `GLYPHOXA_SIGNUP_PLAN_SLUG` | `open` mode | The Plan every open-mode signup is bound to. In `open` mode the boot fails unless it names a synced, non-archived Plan (`glyphoxa billing plans-sync`, [deploy/saas-operations.md](deploy/saas-operations.md)). Ignored in `allowlist` mode. |
| `GLYPHOXA_MAX_VOICE_SESSIONS` | optional | Process-wide cap on concurrent live Voice Sessions (one per Tenant); default `1`, values < 1 ⇒ 1. A start beyond it is refused with a user-visible error. Raising it is soak-gated for DAVE/central-token deployments (#493). |
| `GLYPHOXA_DEV_MODE` | never in prod | Non-empty (except `0`/`false`/`no`/`off`) ⇒ OAuth-less local dev on `127.0.0.1` with auto-auth. |
| `GLYPHOXA_LOG_FORMAT` | optional | `json`, or `text` (the default for any other value). |
| `GLYPHOXA_GATEWAY_IDENTIFY_WARN_THRESHOLD` | optional | Per-application 24h Discord gateway IDENTIFY count above which a structured warning fires (token-reset early-warning, #486). Positive integer; blank/non-numeric/≤0 ⇒ default `500` (below Discord's 1000/token/24h limit). See [deploy/saas-operations.md §7](deploy/saas-operations.md). |
| `GLYPHOXA_TRUSTED_CLIENT_IP_HEADER` | optional (`web`/`all`) | The one request header your reverse proxy sets to the real client address, **overwriting anything the client sent** — used only for the IP stored on the login session's audit record. Set `CF-Connecting-IP` when the instance is reachable *only* through Cloudflare (e.g. a Cloudflare Tunnel); `deploy/saas/install.sh` asks for it (`GX_TRUSTED_CLIENT_IP_HEADER`) and writes it as `web.extraEnv`. Unset ⇒ no header is trusted: the last `X-Forwarded-For` hop (the one the nearest proxy appended), else the socket peer. Never name a header your proxy merely passes through — a client could then choose its own audit address. |
| `GLYPHOXA_OLLAMA_URL` | optional | Ollama embeddings endpoint override; default `http://127.0.0.1:11434`. v1.0 embeddings are **Ollama-only** (ADR-0011), so set it wherever the process has no local Ollama — containers and k8s pods (k3d: `http://host.k3d.internal:11434`). The server must serve `nomic-embed-text`. Unreachable ⇒ semantic memory (L2) stalls loudly; nothing else breaks. |
| `GLYPHOXA_PRIVACY_POLICY_URL` | optional | The deployment's published privacy-policy page. The Bot links it from the transcription disclosure it posts in the voice channel at Voice Session start (#519) — the players at the table are data subjects who may never open the web app. Blank ⇒ the disclosure still posts, without a link. The Helm chart derives it from `ingress.host` + `/privacy` when `privacyPolicyUrl` is unset. |
| `GLYPHOXA_VOICE_IDLE_CLOSE_WINDOW` | optional | How long a Voice Session may process no audio before the hosting Voice Instance closes it ([ADR-0061](adr/0061-idle-close-voice-session-watchdog.md)). Go duration; default `15m`. The literal `off` disables idle closing — a value that is not a duration is treated as a typo and takes the default, so a resource protection is never switched off by accident. "Audio" is inbound room speech or the Bot being asked to speak (`/say`, voiced recap, Highlight replay); a table that is present but silent for the whole window **is** closed. |
| `GLYPHOXA_VOICE_IDLE_CLOSE_SWEEP` | optional | How often the Idle Close watchdog checks; default `30s`. This is the granularity of the close, not of the window. |
| `GLYPHOXA_VOICE_MAX_CONNECT_CYCLES` | optional | How many Discord connect cycles one Voice Session may run through before it is closed as churning; default `200`, `0` disables. Every cycle rebuilds the per-cycle world (voice connection, codec, one HTTP transport per provider adapter, a VAD session per Speaker Lane, a streaming-STT websocket) and not all of it is reclaimed. |
| `GLYPHOXA_VOICE_HEAP_CEILING_MIB` | optional | Go heap footprint in MiB above which the Voice Instance closes the single least-recently-active Voice Session it hosts; default `0` (off). A **process** reading, so with `GLYPHOXA_MAX_VOICE_SESSIONS` > 1 one Tenant's pressure can cost another Tenant their session — deliberate (the alternative at the memory limit is an OOM kill that takes them all), but size it against your own container limit rather than guessing. |
| `GLYPHOXA_VOICE_GOROUTINE_CEILING` | optional | Process goroutine count above which the Voice Instance sheds its quietest Voice Session; default `0` (off). Same process-wide caveat as the heap ceiling. |
| `GLYPHOXA_VOICE_MEDIA_STALL_WINDOW` | optional | How long the voice UDP socket may be silent — despite evidence it should be receiving: the server's keepalive echoes ceasing on a socket that had them, or a participant announcing speech with no RTP following — before the media watchdog declares the Discord media path dead and rebuilds the voice connection ([ADR-0064](adr/0064-voice-media-path-liveness.md), #633). Go duration; default `45s`; the speaking tier is doubled while a fresh connection has yet to carry its first packet. The literal `off` disables the verdict (the once-a-minute liveness log keeps running); an unparsable value takes the default. A quiet table can never trip it: quiet stops RTP, never the echoes, and produces no Speaking announcements. |
| `GLYPHOXA_STT_STREAMING` | optional | Off-switch for streaming STT ([ADR-0042](adr/0042-streaming-stt-speculative-memory-recall.md), amendment 2026-11-22). Streaming — ElevenLabs Scribe v2 Realtime over a websocket instead of one batch POST per utterance — is **on by default**: unset, blank, or any other value streams. Only `0`/`false`/`no`/`off` (any case) turns it **off** and puts every utterance on batch STT. Read once at process start, so changing it needs a pod restart; set it on the pod that runs voice (`web.extraEnv` in `-mode all`, `voice.extraEnv` in a split deployment). While a stream is down an utterance falls back to the batch adapter automatically; local VAD stays the endpointing authority either way. `glyphoxa_voice_stt_final_total{transport,reason}` shows how many finals streamed and why the rest did not. |
| `GLYPHOXA_RECALL_BUDGET` | optional | The longest a turn waits for NPC memory recall before going on without it ([ADR-0042](adr/0042-streaming-stt-speculative-memory-recall.md), amendment 2026-11-20). Go duration; default `500ms`; blank, non-positive or unparsable ⇒ default. A ceiling, not the usual cost: with streaming STT a recall whose utterance closed a sentence in a partial is prefetched during speech and costs about a millisecond; otherwise — short utterances (the first partial arrives ~2.3 s into the audio), and every utterance on batch STT — the turn pays the embeddings time (a few hundred ms on a CPU-only Ollama). |
| `GLYPHOXA_STT_STREAM_MAX_LANES` | optional | With streaming on: how many Speaker Lanes beyond the default lane get their own STT stream; past it a lane transcribes by batch. Integer; default `4`; `0` disables per-lane streaming (the default lane keeps its stream, which it opens on its first speech); negative/non-numeric ⇒ default. Each streaming lane holds one realtime connection to the provider while it is live (lanes are reaped after 2 min of silence). |
| `GLYPHOXA_STT_KEYTERMS` | optional | Off-switch for STT keyterms ([ADR-0042](adr/0042-streaming-stt-speculative-memory-recall.md), keyterm amendment 2026-11-22; #701). **On by default**: each Voice Session sends the names at its table to the recognizer as keyterms — in priority order every Cast NPC's name, the Butler's, then those NPCs' aliases, then the player characters' names and their aliases — on both STT transports (the realtime dial and every batch request), so a short name stops arriving as a near-miss ("Flo" heard as "So" or "Flow") that Address Detection then refuses to route. Only `0`/`false`/`no`/`off` (any case) turns it **off**; unset, blank or anything else leaves it on. Read once at process start (set it where `GLYPHOXA_STT_STREAMING` goes); the list itself is built once per Voice Session, so a renamed NPC or a new player character is picked up by the next session. **Limits:** at most 50 terms and 1000 characters in total are sent — the realtime endpoint refuses a session past either — each term cut to 50 characters and 5 words, with `< > { } [ ] \` removed and a term without any letter or digit dropped; when a table has more names than that the list is cut from its low-priority end (player-character aliases go first, an NPC's name last), and the session-start log line `stt keyterms` reports `terms` and `dropped`. A name that is an everyday word of the Campaign Language ("Wolf", "Karte") is never sent. **If the provider refuses the list anyway** the session keeps hearing: it warns once (`stt keyterms: the provider refused the keyterm list`), stops sending keyterms on both transports until its next reconnect, and transcribes unbiased — no utterance is lost and no restart is needed. **Caveat — homophones:** a keyterm pulls words that sound like it towards its spelling. Speech that sounds nothing like a name never gained one in testing, but a true homophone is rewritten ("Der Floh springt über den Tisch" arrived as "Der Flo springt …", a lone "Flow." as "Flo.") and then counts as the name being said. If a table's vocabulary collides with an NPC's name, rename or alias the NPC, or switch keyterms off. **Cost:** the provider bills keyterms as a surcharge on STT audio — +$0.08 per audio-hour on realtime ($0.22 → $0.30), +$0.05 on batch ($0.20 → $0.25), as of 2026-11-22; the spend meter prices all STT audio at the $0.30 upper bound. No measurable latency change. |
| `GLYPHOXA_STT_LANGUAGE_HINT` | optional | **Opt-in** (`1`/`true`/`yes`/`on`, any case; unset or anything else ⇒ off): send the Campaign Language to the recognizer as `language_code` on both STT transports instead of letting it detect the language of each utterance. Off by default because a table may mix languages (German play with English rules talk), and a pinned language transcribes the other one badly. It does nothing for names — that is `GLYPHOXA_STT_KEYTERMS` — but it stopped wrong-script output on very short utterances in testing. Only the primary subtag is sent (`de-DE` ⇒ `de`); a Campaign without a language sends none. Read once at process start. |
| `GLYPHOXA_TTS_TRANSPORT` | optional | How the ElevenLabs adapter synthesizes ([ADR-0023](adr/0023-tts-provider-matrix-elevenlabs-openai.md), amendment 2026-11-22 "optional WebSocket transport"). Blank or `http` ⇒ one `POST /v1/text-to-speech/{voice}/stream` per sentence on a kept-warm connection (**the default**). `ws` ⇒ sentences of voices on the `eleven_v3*` / `eleven_v4*` model families ride pooled Text-to-Dialogue websockets instead (~45 ms earlier first audio on a warm socket, measured); voices on any other model stay on HTTP, and so does a sentence the pool has no room for. Each open socket holds one **dialogue-session slot** of the ElevenLabs plan behind the key: at most 2 per voice and 8 per Voice Session, closed 60 s after last use in a silent room — while people keep talking, one socket per recently used voice (two voices at most) stays open until that voice has been quiet for 5 minutes. When the account's slots run out (handshake `429`) the sentence is spoken over HTTP and no socket is dialled for 15 s. Each connect logs `tts: synthesis transport` with the transport in use. Case-insensitive; any other value logs a warning and keeps `http`. |
| `GLYPHOXA_LLM_DEFAULT_PROVIDER` | optional | The deployment-default LLM provider ([ADR-0036](adr/0036-voice-llm-llama-3-3-70b-on-groq.md), amendment 2026-11-22): `groq`, `anthropic` or `gemini` (case-insensitive). It is what a Tenant runs when it has saved **no LLM Provider Config of its own** — Voice Sessions, the Highlight classifier, recap, assist and the planning chat all resolve through the same value; a Tenant's own row always wins. Unset ⇒ the code default, Groq. Read once at boot in every Mode, and **fatal** when wrong: an unknown provider, or a named provider whose platform key (`GROQ_API_KEY` / `ANTHROPIC_API_KEY` / `GEMINI_API_KEY`) is empty, stops the process with a message naming the variable — a deployment that promises a default no keyless Tenant can speak on does not start. The platform-key entitlement (ADR-0054/0055) gates this provider's key exactly as it gates Groq's: a BYOK-plan Tenant with no saved key is still refused. Helm: `llm.defaultProvider` (rendered into both pods; the chart fails the render on the same two mistakes). |
| `GLYPHOXA_LLM_DEFAULT_MODEL` | optional | The model asked of the default provider, e.g. `claude-haiku-4-5`. Unset ⇒ that provider's default (`openai/gpt-oss-120b` on Groq, `claude-haiku-5-5` on Anthropic) — one model for voice **and** the off-session engines; nothing falls through to the Anthropic adapter's flagship. Set without a provider it applies to Groq. It is the model requested **and** the model metered, so pick one `internal/spend/prices.go` prices: an unpriced id is charged at one flat fallback rate (\$5 / \$15 per MTok) that can exhaust a trial allowance in a handful of turns on a cheap model and under-meters an Opus-class one — the boot logs a WARN naming it. This env is the only way an unpriced model runs on a platform key: a Tenant riding the platform key can itself only pick priced models. A Tenant row on the default provider that names no model follows this value too. Helm: `llm.defaultModel`. |
| `GLYPHOXA_ADDRESS_CONTINUATION_WINDOW` | optional | How long an unnamed follow-up stays with the Agent just addressed (the Continuation Window, ADR-0024). Go duration; default `30s`; `0s` ⇒ never continue (every utterance must name its Agent); negative/unparsable ⇒ default. |
| `GLYPHOXA_FLIGHT_TRACE_THRESHOLD_MS` | optional | Arms the execution-trace flight recorder (#607) in every process hosting the voice loop: each turn slower than this many milliseconds dumps the last ~10s of `runtime/trace` (newest 8 kept, at most one per 30s). e.g. `2500` = the p95 SLO. Default off; ~1-2% CPU while armed. The Helm chart's `voice.extraEnv` / `web.extraEnv` is the place to set it. |
| `GLYPHOXA_FLIGHT_TRACE_DIR` | optional | Where flight-recorder snapshots go; default `$TMPDIR/glyphoxa-flight` (`/tmp/glyphoxa-flight` — the chart mounts a writable emptyDir at `/tmp`). Must be writable. |
| `GLYPHOXA_TAPE_CONSENT_RECONCILE_INTERVAL` | optional | How often a Voice Instance re-reads Rollover Tape consent from the database, so a grant/revoke made on another pod converges (#492). Go duration; default `5s`; unparsable/≤0 ⇒ default. |
| `GLYPHOXA_PRESENCE_RECONCILE_INTERVAL` | optional | How often the presence registry re-reconciles every Tenant's standing Discord client and Slash Command registration against the database (catching settings saved on another pod). Go duration; default `30s`; blank/invalid/≤0 ⇒ default. |
| `GLYPHOXA_PRESENCE_OWNER_INTERVAL` | optional | `voice` worker: how often the `presence_owner` election winner renews its claim (ADR-0057 (c)). Go duration; default `5s`. Keep well under the expiry. |
| `GLYPHOXA_PRESENCE_OWNER_EXPIRY` | optional | `voice` worker: how long a silent presence owner keeps the row before a challenger takes over interaction dispatch. Go duration; default `15s`. |
| `GLYPHOXA_VOICE_CLAIM_POLL` | optional | Claim-plane poll cadence (#491): how often a `voice` worker looks for unclaimed Voice Session intents, and how often a `web` tier's Start/Stop polls for the worker's answer. Go duration; default `2s`. |
| `GLYPHOXA_VOICE_HEARTBEAT_INTERVAL` | optional | `voice` worker: heartbeat cadence for each claimed intent. Go duration; default `5s`. Must sit well under the expiry. |
| `GLYPHOXA_VOICE_HEARTBEAT_EXPIRY` | optional | When a claimed intent's heartbeat is this stale, its worker is presumed dead and the intent is reaped (worker and web tier use the same horizon). Go duration; default `30s`. |
| `GLYPHOXA_VOICE_DRAIN_BEAT_CAP` | optional | `voice` worker: how long a session wind-down keeps heartbeating before the worker stops beating for it (bounds a wedged drain). Go duration; default `0` ⇒ 10 × the heartbeat expiry (`300s` at defaults). |
| `GLYPHOXA_VOICE_CONTROL_BUDGET` | optional | Split deployment (`web` + `voice`): how long a live control relayed to the hosting worker (mute, `/say`, Butler say, #503) waits for its confirmation. Go duration; default `15s` — keep it above the heartbeat interval plus one execution. |
| `GLYPHOXA_PPROF` | optional | `1` mounts `/debug/pprof` on the observability listener (#586) — including `/debug/pprof/goroutineleak` (Go 1.27+), which reports goroutines blocked on unreachable primitives, i.e. leaks a live pod can name without a rebuild. Off by default: profiles expose goroutine stacks (argument values included) and on-demand CPU profiling burns CPU. The metrics port is cluster-internal (ADR-0032), so this is an operator switch, not a security boundary. |
| `GLYPHOXA_PPROF_BLOCK_RATE` | optional | With `GLYPHOXA_PPROF=1`: nanoseconds of goroutine blocking per sampled event (`runtime.SetBlockProfileRate`) so `/debug/pprof/block` carries data; e.g. `10000000` ≈ one sample per 10ms blocked. Default off — sampling costs the realtime voice path. Non-numeric/≤0 ⇒ ignored. |
| `GLYPHOXA_PPROF_MUTEX_FRACTION` | optional | With `GLYPHOXA_PPROF=1`: sample 1/n of mutex contention events (`runtime.SetMutexProfileFraction`) so `/debug/pprof/mutex` carries data. Default off; non-numeric/≤0 ⇒ ignored. |
| `GLYPHOXA_GOROUTINE_LEAK_SAMPLE_INTERVAL` | optional | Go duration arming the `glyphoxa_goroutine_leaks` gauge: on scrape, at most once per interval, the Go 1.27 goroutineleak profile is sampled and the count of permanently blocked goroutines exported — the leak-trend companion to `GLYPHOXA_VOICE_GOROUTINE_CEILING`'s raw count. Each sample runs a GC cycle, so pick minutes, not seconds (e.g. `5m`). Default off; not-a-duration/≤0 ⇒ off. Independent of `GLYPHOXA_PPROF` (a count exposes no stacks). |
| `GROQ_API_KEY` | if Groq used | LLM provider key. |
| `ELEVENLABS_API_KEY` | if ElevenLabs used | STT/TTS provider key. |
| `GEMINI_API_KEY` | if Gemini used | LLM / S2S provider key. |
| `ANTHROPIC_API_KEY` | if Anthropic used | LLM provider key: the platform key behind `GLYPHOXA_LLM_DEFAULT_PROVIDER=anthropic` and behind the Anthropic slot of the Configuration screen for Tenants on the Platform Keys. Helm: the top-level `anthropicApiKey` value, which lands in the chart Secret and reaches the web and voice pods exactly like `groqApiKey`. An OAuth access token of the `ant` CLI (`sk-ant-oat…`) is also accepted and sent as a bearer; it expires within hours, so it suits a developer machine or a bench run, not a deployment. A voice session on Anthropic with no model configured runs `claude-haiku-5-5`. |

## See also

- [ADR-0041](adr/0041-operator-allowlist-access-policy.md) — operator allowlist, no trust-on-first-use, loud-fail/loopback posture (source of truth).
- [ADR-0039](adr/0039-mvp-ui-backend-single-operator-web-tier.md) — single-operator web tier; [ADR-0055](adr/0055-self-signup-admission-modes.md) — Admission Modes / self-signup; [ADR-0057](adr/0057-shared-voice-worker-pool-multi-tenant.md) — the voice-worker pool and claim plane.
- [ADR-0016](adr/0016-cookies-discord-only-oauth.md) — cookies + Discord-only OAuth.
- [ADR-0004](adr/0004-byok-provider-key-matrix.md) — BYOK provider key matrix.
- [ADR-0034](adr/0034-deployment-artifacts.md) — deployment artifacts (container/Helm).
- [deploy/k3s-proxmox.md](deploy/k3s-proxmox.md) — hosting Glyphoxa on a k3s cluster (Proxmox home lab, DynDNS + TLS); [deploy/saas-operations.md](deploy/saas-operations.md) — subscription Plans, platform keys, and the `glyphoxa billing` cost/revenue surface (ADR-0054).
- [docs/agents/live-npc-run.md](agents/live-npc-run.md) — the `voice`-mode live NPC run guide.
