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
  local brk="$1"
  [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
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

echo "e2e-deploy-smoke-test: [1/7] gate passes on a fully correct web tier"
start_fixture ""
expect_pass "correct console + OAuth handlers accepted"

echo "e2e-deploy-smoke-test: [2/7] gate fails when the served root is the placeholder"
start_fixture "placeholder"
expect_fail "placeholder console rejected (real-vs-placeholder assertion)"

echo "e2e-deploy-smoke-test: [3/7] gate fails when login does not redirect to Discord"
start_fixture "login_location"
expect_fail "login must 302 to the Discord authorize URL with the client id + redirect"

echo "e2e-deploy-smoke-test: [4/7] gate fails when login sets no state cookie"
start_fixture "login_cookie"
expect_fail "login must set the anti-forgery state cookie"

echo "e2e-deploy-smoke-test: [5/7] gate fails when the callback accepts a forged state"
start_fixture "callback_accepts"
expect_fail "callback must refuse a forged/missing OAuth state"

echo "e2e-deploy-smoke-test: [6/7] gate fails when the unauthenticated current-user probe is 200"
start_fixture "getcurrentuser_open"
expect_fail "unauthenticated GetCurrentUser must be 401"

echo "e2e-deploy-smoke-test: [7/7] gate fails when a protected RPC answers without a session cookie"
start_fixture "protected_rpc_open"
expect_fail "a protected RPC without a session cookie must be refused (401)"

echo "e2e-deploy-smoke-test: OK"
