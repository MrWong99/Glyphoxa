#!/usr/bin/env bash
# Self-test for the web-tier HTTP assertions in scripts/e2e-deploy-smoke.sh
# (issue #128): the gate must actually gate. The real e2e runs those assertions
# against a live cluster-served console (a multi-minute image build + k3d spin
# up); this proves the SAME assertions FAIL when the served page or the OAuth
# handlers regress — without a cluster — the way container-smoke-test.sh (#114)
# and helm-validate-test.sh (#140) keep their gates from silently no-opping.
#
# e2e-deploy-smoke.sh SMOKE_ONLY=web-http runs ONLY the web HTTP assertions
# against $WEB_BASE_URL, skipping helm/kubectl. We point it at a tiny local
# fixture server (python http.server) that serves a REAL console + correct OAuth
# behaviour by default, and can BREAK exactly one property via $FIXTURE_BREAK.
# Each break must make the run fail, so a lost assertion re-opens exactly one
# hole (the same one-property-per-case discipline as the sibling self-tests).
set -euo pipefail

cd "$(dirname "$0")/.."

SERVER_PY="scripts/testdata/web-smoke-fixture.py"
PORT="${E2E_SELFTEST_PORT:-18099}"
BASE="http://127.0.0.1:${PORT}"
SERVER_PID=""

cleanup() {
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
}
trap cleanup EXIT

# start_fixture BREAK starts the fixture server with the named property broken
# ("" = fully correct) and waits until it answers, replacing any prior server.
start_fixture() {
  local brk="$1" old="$SERVER_PID"
  if [ -n "$old" ]; then
    kill "$old" 2>/dev/null || true
    # Reap the old server before rebinding so the replacement never races it to
    # the port (EADDRINUSE) while it is slow to die.
    wait "$old" 2>/dev/null || true
  fi
  SERVER_PID=""
  FIXTURE_BREAK="$brk" python3 "$SERVER_PY" "$PORT" &
  SERVER_PID=$!
  for _ in $(seq 1 50); do
    if curl -fsS "${BASE}/" >/dev/null 2>&1; then return 0; fi
    sleep 0.1
  done
  echo "e2e-deploy-smoke-test: fixture server did not come up (break=${brk:-none})" >&2
  exit 1
}

# run_gate runs the web-http assertion subset against the fixture server.
run_gate() {
  WEB_BASE_URL="$BASE" SMOKE_ONLY=web-http ./scripts/e2e-deploy-smoke.sh
}

# expect_pass / expect_fail assert the gate's exit status for the current fixture.
expect_pass() {
  local label="$1"
  if run_gate >/dev/null 2>&1; then
    echo "e2e-deploy-smoke-test: PASS — ${label}"
  else
    echo "e2e-deploy-smoke-test: FAIL — ${label} (gate errored on a correct fixture)" >&2
    exit 1
  fi
}
expect_fail() {
  local label="$1"
  if run_gate >/dev/null 2>&1; then
    echo "e2e-deploy-smoke-test: FAIL — ${label} (gate passed on a broken fixture)" >&2
    exit 1
  else
    echo "e2e-deploy-smoke-test: PASS — ${label}"
  fi
}

# One case per assertion property: each fixture breaks exactly ONE thing, so a
# case flips red<->green iff its dedicated assertion is present. This defeats the
# mutation blind spot where a break trips several properties at once and any one
# of the overlapping assertions could be deleted unnoticed.

echo "e2e-deploy-smoke-test: [1/11] gate passes on a fully correct web tier"
start_fixture ""
expect_pass "correct console + OAuth handlers accepted"

echo "e2e-deploy-smoke-test: [2/11] real-vs-placeholder: served root is the placeholder"
start_fixture "placeholder"
expect_fail "placeholder console rejected (hashed /assets ref required)"

echo "e2e-deploy-smoke-test: [3/11] login redirect: answers 200 instead of a 302"
start_fixture "login_status"
expect_fail "login must be a 302 redirect"

echo "e2e-deploy-smoke-test: [4/11] login redirect: target host is not Discord authorize"
start_fixture "login_wrong_host"
expect_fail "login must 302 to the Discord authorize URL"

echo "e2e-deploy-smoke-test: [5/11] login redirect: wrong client_id"
start_fixture "login_wrong_client_id"
expect_fail "login redirect must carry the configured client_id"

echo "e2e-deploy-smoke-test: [6/11] login redirect: wrong redirect_uri"
start_fixture "login_wrong_redirect_uri"
expect_fail "login redirect must carry the configured redirect_uri"

echo "e2e-deploy-smoke-test: [7/11] login redirect: no state cookie"
start_fixture "login_cookie"
expect_fail "login must set the anti-forgery state cookie"

echo "e2e-deploy-smoke-test: [8/11] callback: accepts a forged (mismatched) state"
start_fixture "callback_accepts_forged"
expect_fail "callback must refuse a forged OAuth state"

echo "e2e-deploy-smoke-test: [9/11] callback: accepts a missing state"
start_fixture "callback_accepts_missing"
expect_fail "callback must refuse a missing OAuth state"

echo "e2e-deploy-smoke-test: [10/11] auth gate: unauthenticated GetCurrentUser is 200"
start_fixture "getcurrentuser_open"
expect_fail "unauthenticated GetCurrentUser must be 401"

echo "e2e-deploy-smoke-test: [11/11] auth gate: a protected RPC answers without a session cookie"
start_fixture "protected_rpc_open"
expect_fail "a protected RPC without a session cookie must be refused (401)"

echo "e2e-deploy-smoke-test: OK"
