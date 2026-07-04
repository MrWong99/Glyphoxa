#!/usr/bin/env bash
# e2e-deploy-smoke.sh — issue #37 (extended by #118, #128): prove the whole
# deploy chain converges on the CURRENT kube context (an ephemeral k3d/kind
# cluster), WITHOUT live Discord credentials. It helm-installs the chart and
# asserts, in order:
#
#   Postgres Ready → migrate Job Completed → seed Job Completed
#     → voice Deployment Available → /readyz returns 200
#     → web Deployment Available → the web Service serves the REAL console shell
#       (not the placeholder) → the Discord OAuth login redirect + callback +
#       auth gate behave, end-to-end on the cluster, with NO call to Discord
#
# then tears the release down. The cluster lifecycle is the caller's concern (the
# CI workflow creates/deletes the k3d cluster and imports the image); this script
# owns the helm install + ordered assertions, so it is runnable locally too:
#
#   k3d cluster create gx-e2e
#   docker build -t glyphoxa:e2e . && k3d image import glyphoxa:e2e -c gx-e2e
#   ./scripts/e2e-deploy-smoke.sh
#   k3d cluster delete gx-e2e
#
# No live credentials: a placeholder Discord token never joins a channel, but the
# resilience fix (issue #44) keeps the pod serving /healthz + the DB-backed
# /readyz, so the Deployment reaches Available regardless. The OAuth assertions
# below exercise only the LOCAL half of the flow (the login redirect the server
# builds itself, the callback's state check, the auth gate) — none of which calls
# Discord. TWO checks stay manual, needing live credentials the unattended smoke
# has no way to supply:
#   - the voice bot's actual channel join (slice 6); and
#   - the FULL Discord login that lands a real glyphoxa_session cookie — it needs
#     a real Discord OAuth app (client id/secret/redirect the user consents to)
#     and an allowlisted operator snowflake (ADR-0041). Verify it by hand with
#     the NOTES.txt port-forward recipe against a real OAuth app.
# All knobs are env vars with defaults.
#
# The web HTTP assertions are factored into functions so scripts/
# e2e-deploy-smoke-test.sh can run them (SMOKE_ONLY=web-http) against a local
# fixture server — proving the gate isn't a silent no-op — without a cluster.
set -euo pipefail

RELEASE="${RELEASE:-glyphoxa-e2e}"
NAMESPACE="${NAMESPACE:-glyphoxa-e2e}"
CHART="${CHART:-deploy/charts/glyphoxa}"
IMAGE_REPO="${IMAGE_REPO:-glyphoxa}"
IMAGE_TAG="${IMAGE_TAG:-e2e}"
# One timeout for the helm install and every wait. Generous so a cold image pull
# (Postgres) or a slow runner does not flake the gate; a genuine non-convergence
# still fails within it.
TIMEOUT="${TIMEOUT:-300s}"
# Local port the /readyz check forwards the metrics listener to.
READYZ_LOCAL_PORT="${READYZ_LOCAL_PORT:-19090}"
# Local port the console-root check forwards the web Service to.
WEB_LOCAL_PORT="${WEB_LOCAL_PORT:-18080}"
# Base URL the web HTTP assertions probe. Defaults to the port-forward above;
# the self-test overrides it to a local fixture server (no cluster).
WEB_BASE_URL="${WEB_BASE_URL:-http://127.0.0.1:${WEB_LOCAL_PORT}}"

# The dummy-but-syntactically-valid OAuth credentials the install wires into the
# Web Instance AND the login-redirect assertion checks for. Single source so the
# installed value and the asserted value can never drift. requireWebEnv (ADR-0041,
# #112) only checks presence + parseability, not that Discord accepts them — the
# unattended smoke never performs a live login — so the pod boots to /readyz=200.
OAUTH_CLIENT_ID="${OAUTH_CLIENT_ID:-ci-not-a-real-oauth-client-id}"
OAUTH_CLIENT_SECRET="${OAUTH_CLIENT_SECRET:-ci-not-a-real-oauth-client-secret}"
OAUTH_REDIRECT_URL="${OAUTH_REDIRECT_URL:-http://localhost:8080/auth/discord/callback}"
OPERATOR_IDS="${OPERATOR_IDS:-111111111111111111}"

# The committed placeholder index.html, verbatim (internal/spa/dist/index.html) —
# what the web root serves when the real Vite build was NOT embedded (#114).
PLACEHOLDER_INDEX='<!doctype html><html><body><div id="root"></div></body></html>'
# The Connect API base the SPA calls; a management RPC is <base>/<Service>/<Method>.
API_BASE="/api/glyphoxa.management.v1"
# Discord's OAuth2 authorize endpoint the login redirect must target (ADR-0016).
DISCORD_AUTHORIZE_URL="https://discord.com/api/oauth2/authorize"

# Object names mirror the chart helpers: glyphoxa.<component>.fullname renders as
# <release>-<component> (deploy/charts/glyphoxa/templates/_helpers.tpl).
PG="${RELEASE}-postgres"
MIGRATE="${RELEASE}-migrate"
SEED="${RELEASE}-seed"
VOICE="${RELEASE}-voice"
WEB="${RELEASE}-web"

VALUES_FILE=""
PF_PID=""

log() { printf '\n=== %s ===\n' "$*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

# urldecode turns percent-escapes back into bytes so an assertion can compare the
# decoded redirect_uri against the plain configured URL (bash-only: %XX -> \xXX
# then let printf interpret the escapes; there are no '+'-encoded spaces here).
urldecode() { local s="${1//+/ }"; printf '%b' "${s//%/\\x}"; }

# http_status prints the HTTP status code of METHOD URL (default GET), following
# NO redirects so a 302 is observed as a 302. Extra args pass through to curl.
http_status() {
  local method="$1" url="$2"; shift 2
  curl -s -o /dev/null -w '%{http_code}' -X "$method" "$@" "$url" 2>/dev/null || true
}

# --- web HTTP assertions (each takes the base URL) --------------------------
# Run against the cluster-served web Service in the full smoke, and against a
# local fixture server in the self-test. Each fails the whole script on a miss.

# assert_console_served waits until the web root answers 200. This is the
# Service-reachability check: the Service selector + named http targetPort are
# exercised end to end and the SPA handler returns index.html.
assert_console_served() {
  local base="$1" ready=""
  for _ in $(seq 1 30); do
    if curl -fsS "${base}/" >/dev/null 2>&1; then ready=1; break; fi
    sleep 1
  done
  [ -n "$ready" ] || fail "the web Service did not serve the console root (200) within the window"
}

# assert_real_console is the #114 embedded-console gate, now over HTTP against
# the cluster-served page instead of grep'ing the binary: the served root must
# reference a content-hashed Vite bundle (/assets/index-<hash>.js) AND must NOT
# be the committed placeholder one-liner. Two-sided, so a real bundle served
# alongside a stale placeholder fails as loudly as a missing one.
assert_real_console() {
  local base="$1" body
  body="$(curl -fsS "${base}/" 2>/dev/null)" || fail "could not fetch the console root for the real-vs-placeholder check"
  printf '%s' "$body" | grep -Eq '/assets/index-[A-Za-z0-9_-]+\.js' \
    || fail "the served console root has no hashed /assets/index-*.js reference — it is the placeholder, not a real console build"
  if [ "$body" = "$PLACEHOLDER_INDEX" ]; then
    fail "the served console root is the committed placeholder index.html one-liner (a real build must overwrite it)"
  fi
}

# assert_login_redirect checks GET /auth/discord/login starts the OAuth flow
# (ADR-0016): a 302 to Discord's authorize URL carrying the CONFIGURED client id
# and redirect, plus the short-lived anti-forgery state cookie. It follows no
# redirect, so Discord is never contacted — the server builds the URL itself.
assert_login_redirect() {
  local base="$1" headers status location setcookie
  headers="$(curl -s -o /dev/null -D - "${base}/auth/discord/login" 2>/dev/null)" \
    || fail "could not reach the OAuth login endpoint"
  status="$(printf '%s' "$headers" | awk 'NR==1{print $2}')"
  [ "$status" = "302" ] || fail "OAuth login returned ${status:-<none>}, want a 302 redirect to Discord"
  location="$(printf '%s\n' "$headers" | tr -d '\r' | awk 'tolower($1)=="location:"{print $2; exit}')"
  setcookie="$(printf '%s\n' "$headers" | tr -d '\r' | grep -i '^set-cookie:' || true)"

  case "$location" in
    "${DISCORD_AUTHORIZE_URL}?"*) : ;;
    *) fail "OAuth login redirect target is '${location}', want the Discord authorize URL" ;;
  esac
  case "$location" in
    *"client_id=${OAUTH_CLIENT_ID}"*) : ;;
    *) fail "OAuth login redirect does not carry the configured client_id (${OAUTH_CLIENT_ID})" ;;
  esac
  case "$(urldecode "$location")" in
    *"redirect_uri=${OAUTH_REDIRECT_URL}"*) : ;;
    *) fail "OAuth login redirect does not carry the configured redirect_uri (${OAUTH_REDIRECT_URL})" ;;
  esac
  printf '%s\n' "$setcookie" | grep -q 'glyphoxa_oauth_state=[^;[:space:]]' \
    || fail "OAuth login did not set the glyphoxa_oauth_state login-state cookie"
}

# assert_callback_refuses_forged_state checks GET /auth/discord/callback rejects
# a request whose state does not match a state cookie, and one with no state at
# all — the CSRF guard on the OAuth round trip (ADR-0016). Both must 400 BEFORE
# any code exchange, so Discord is never contacted on a forgery.
assert_callback_refuses_forged_state() {
  local base="$1" code
  # Forged: a state param that does not match the presented state cookie.
  code="$(http_status GET "${base}/auth/discord/callback?state=forged&code=x" \
    -H 'Cookie: glyphoxa_oauth_state=different')"
  [ "$code" = "400" ] || fail "OAuth callback accepted a forged state (got ${code}, want 400)"
  # Missing: no state param and no state cookie.
  code="$(http_status GET "${base}/auth/discord/callback?code=x")"
  [ "$code" = "400" ] || fail "OAuth callback accepted a missing state (got ${code}, want 400)"
}

# assert_unauth_current_user checks the console's boot probe,
# AuthService.GetCurrentUser, returns 401 unauthenticated. It is the one PUBLIC
# procedure (reachable without a session) but self-handles the missing session
# with CodeUnauthenticated → HTTP 401 — the SPA's "-> /login" signal. A 200 here
# would be an auth-bypass smell.
assert_unauth_current_user() {
  local base="$1" code
  code="$(http_status POST "${base}${API_BASE}.AuthService/GetCurrentUser" \
    -H 'Content-Type: application/json' --data '{}')"
  [ "$code" = "401" ] || fail "unauthenticated GetCurrentUser returned ${code}, want 401 (auth gate)"
}

# assert_protected_rpc_refused checks a PROTECTED RPC (not in the public set) is
# refused for a request with no session cookie. Unlike GetCurrentUser, the auth
# interceptor itself rejects GetActiveCampaign with CodeUnauthenticated → 401
# before the handler runs — so this proves the gate closes the whole API, not
# just that the public probe self-reports.
assert_protected_rpc_refused() {
  local base="$1" code
  code="$(http_status POST "${base}${API_BASE}.CampaignService/GetActiveCampaign" \
    -H 'Content-Type: application/json' --data '{}')"
  [ "$code" = "401" ] || fail "a protected RPC without a session cookie returned ${code}, want 401 (auth gate)"
}

# assert_web_tier runs every web HTTP assertion in order against a base URL.
assert_web_tier() {
  local base="$1"
  log "assert: the web Service serves the console root (200)"
  assert_console_served "$base"
  log "assert: the served console is the REAL build, not the placeholder (#114)"
  assert_real_console "$base"
  log "assert: OAuth login 302s to Discord with the client id + redirect + state cookie"
  assert_login_redirect "$base"
  log "assert: OAuth callback refuses a forged or missing state"
  assert_callback_refuses_forged_state "$base"
  log "assert: unauthenticated GetCurrentUser returns 401 (the auth gate stands)"
  assert_unauth_current_user "$base"
  log "assert: a protected RPC without a session cookie is refused (401)"
  assert_protected_rpc_refused "$base"
}

# SMOKE_ONLY=web-http runs ONLY the web HTTP assertions against $WEB_BASE_URL and
# exits — no helm, no kubectl. scripts/e2e-deploy-smoke-test.sh uses this to point
# the assertions at a local fixture server and prove the gate isn't a no-op.
if [ "${SMOKE_ONLY:-}" = "web-http" ]; then
  assert_web_tier "$WEB_BASE_URL"
  log "PASS: web HTTP assertions passed against ${WEB_BASE_URL}"
  exit 0
fi

VALUES_FILE="$(mktemp)"

dump_diagnostics() {
  log "DIAGNOSTICS (namespace ${NAMESPACE})"
  kubectl -n "$NAMESPACE" get all,jobs -o wide 2>/dev/null || true
  kubectl -n "$NAMESPACE" get events --sort-by=.lastTimestamp 2>/dev/null | tail -40 || true
  for c in postgres migrate seed voice web; do
    log "describe + logs: component=${c}"
    kubectl -n "$NAMESPACE" describe pod -l "app.kubernetes.io/component=${c}" 2>/dev/null || true
    kubectl -n "$NAMESPACE" logs -l "app.kubernetes.io/component=${c}" --all-containers --tail=80 2>/dev/null || true
  done
}

cleanup() {
  local rc=$?
  [ -n "$PF_PID" ] && kill "$PF_PID" 2>/dev/null || true
  if [ "$rc" -ne 0 ]; then dump_diagnostics; fi
  # Best-effort release teardown. Helm leaves the chart's HOOK resources (the
  # Postgres StatefulSet + PVC, the Secret, and the migrate/seed Jobs — all
  # hook-delete-policy before-hook-creation) in place on uninstall; on a reused
  # cluster they linger until the next install, while CI deletes the whole
  # cluster (always()) which reclaims everything. No --wait: teardown readiness
  # is not asserted and a wedged uninstall must not delay the real exit code.
  log "helm uninstall ${RELEASE}"
  helm uninstall "$RELEASE" -n "$NAMESPACE" --timeout 60s >/dev/null 2>&1 || true
  rm -f "$VALUES_FILE"
  exit "$rc"
}
trap cleanup EXIT

# Generate the install values. The provider keys + Discord token are obviously
# fake (the bot never authenticates — that is the point); appSecret is a throwaway
# credential-cipher key generated here (ADR-0004: real keys live in the OS keyring,
# never the DB), so no secret is committed. image.pullPolicy=Never forces the pods
# to use the k3d-imported image instead of pulling from a registry. The snowflakes
# are quoted strings (a 64-bit ID loses precision as a YAML number). The OAuth
# creds + operator snowflake come from the shared $OAUTH_* / $OPERATOR_IDS vars so
# the login-redirect assertion checks exactly what was installed.
cat >"$VALUES_FILE" <<EOF
image:
  repository: "${IMAGE_REPO}"
  tag: "${IMAGE_TAG}"
  pullPolicy: Never
appSecret: "$(openssl rand -base64 32)"
discordBotToken: "ci-not-a-real-discord-token"
elevenLabsApiKey: "ci-fake-elevenlabs-key"
geminiApiKey: "ci-fake-gemini-key"
groqApiKey: "ci-fake-groq-key"
voice:
  enabled: true
  guild: "111111111111111111"
  channel: "222222222222222222"
web:
  enabled: true
  # Dummy-but-syntactically-valid OAuth credentials + a numeric operator snowflake.
  # requireWebEnv (ADR-0041, #112) only checks they are present + parseable, not
  # that Discord accepts them — OAuth is exercised on an actual login, which this
  # unattended smoke never performs — so the Web Instance boots to /readyz=200 on
  # the DB-backed readiness alone, exactly like the voice pod with a placeholder
  # token. The FULL live OAuth login round-trip stays a manual check (see header).
  oauth:
    clientId: "${OAUTH_CLIENT_ID}"
    clientSecret: "${OAUTH_CLIENT_SECRET}"
    redirectUrl: "${OAUTH_REDIRECT_URL}"
  operatorIds: "${OPERATOR_IDS}"
EOF

log "helm install ${RELEASE} (chart ${CHART}, image ${IMAGE_REPO}:${IMAGE_TAG})"
# --wait blocks until the ORDERED pre-install hook chain succeeds — Postgres
# (weight -10) up, migrate Job (-5) Completed, seed Job (-4) Completed — and then
# BOTH the voice and web Deployments reach Available. So a zero exit here already
# proves the whole chain converged; the explicit asserts below name the exact
# link on a regression and double as the #37/#118/#128 acceptance checklist.
helm install "$RELEASE" "$CHART" \
  --namespace "$NAMESPACE" --create-namespace \
  --values "$VALUES_FILE" \
  --wait --timeout "$TIMEOUT"

# The migrate/seed Jobs persist after success (hook-delete-policy
# before-hook-creation), so they are still inspectable here.
log "assert: Postgres StatefulSet Ready"
kubectl -n "$NAMESPACE" rollout status "statefulset/${PG}" --timeout "$TIMEOUT"

log "assert: migrate Job Completed (schema current)"
kubectl -n "$NAMESPACE" wait --for=condition=Complete "job/${MIGRATE}" --timeout "$TIMEOUT"

log "assert: seed Job Completed (NPC present)"
kubectl -n "$NAMESPACE" wait --for=condition=Complete "job/${SEED}" --timeout "$TIMEOUT"

log "assert: voice Deployment Available"
kubectl -n "$NAMESPACE" wait --for=condition=Available "deployment/${VOICE}" --timeout "$TIMEOUT"

log "assert: /readyz returns 200"
# Port-forward the metrics listener and curl /readyz directly: the slim runtime
# image ships no curl, but the runner has one. A 200 here is the same DB-backed
# readiness the kubelet probe gates Available on, asserted end-to-end and without
# live Discord. Retry briefly so a freshly established forward does not flake.
kubectl -n "$NAMESPACE" port-forward "deployment/${VOICE}" "${READYZ_LOCAL_PORT}:9090" >/dev/null 2>&1 &
PF_PID=$!
ready=""
for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:${READYZ_LOCAL_PORT}/readyz" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
if [ -z "$ready" ]; then
  echo "FAIL: /readyz did not return 200 within the window"
  exit 1
fi
kill "$PF_PID" 2>/dev/null || true
PF_PID=""

log "assert: web Deployment Available"
# The Web Instance (#118) boots with the dummy OAuth creds + operator snowflake
# above; requireWebEnv (ADR-0041) is satisfied by their presence, and readiness
# is the same DB-backed /readyz on the internal metrics port, so it reaches
# Available without a live Discord login.
kubectl -n "$NAMESPACE" wait --for=condition=Available "deployment/${WEB}" --timeout "$TIMEOUT"

# Port-forward the web SERVICE (not the pod) so the Service's selector + the
# named http targetPort are exercised end to end, then run the web HTTP
# assertions (#118 console-served + 401 gate, #128 real-console + OAuth flow)
# through it. The forward stays up for every assertion; cleanup kills it.
kubectl -n "$NAMESPACE" port-forward "service/${WEB}" "${WEB_LOCAL_PORT}:80" >/dev/null 2>&1 &
PF_PID=$!
assert_web_tier "$WEB_BASE_URL"

log "PASS: deploy chain converged — Postgres → migrate → seed → voice Available → /readyz 200 → web Available → real console served → OAuth login redirect + state cookie → callback refuses forged state → GetCurrentUser 401 → protected RPC 401"
