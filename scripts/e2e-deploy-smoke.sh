#!/usr/bin/env bash
# e2e-deploy-smoke.sh — issue #37 (extended by #118): prove the whole deploy chain
# converges on the CURRENT kube context (an ephemeral k3d/kind cluster), WITHOUT
# live Discord credentials. It helm-installs the chart and asserts, in order:
#
#   Postgres Ready → migrate Job Completed → seed Job Completed
#     → voice Deployment Available → /readyz returns 200
#     → web Deployment Available → the web Service serves the console root (200)
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
# /readyz, so the Deployment reaches Available regardless. The actual channel join
# stays a manual check (slice 6). All knobs are env vars with defaults.
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

# Object names mirror the chart helpers: glyphoxa.<component>.fullname renders as
# <release>-<component> (deploy/charts/glyphoxa/templates/_helpers.tpl).
PG="${RELEASE}-postgres"
MIGRATE="${RELEASE}-migrate"
SEED="${RELEASE}-seed"
VOICE="${RELEASE}-voice"
WEB="${RELEASE}-web"

VALUES_FILE="$(mktemp)"
PF_PID=""

log() { printf '\n=== %s ===\n' "$*"; }

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
# are quoted strings (a 64-bit ID loses precision as a YAML number).
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
  # token. A live OAuth login round-trip is issue #128's smoke.
  oauth:
    clientId: "ci-not-a-real-oauth-client-id"
    clientSecret: "ci-not-a-real-oauth-client-secret"
    redirectUrl: "http://localhost:8080/auth/discord/callback"
  operatorIds: "111111111111111111"
EOF

log "helm install ${RELEASE} (chart ${CHART}, image ${IMAGE_REPO}:${IMAGE_TAG})"
# --wait blocks until the ORDERED pre-install hook chain succeeds — Postgres
# (weight -10) up, migrate Job (-5) Completed, seed Job (-4) Completed — and then
# BOTH the voice and web Deployments reach Available. So a zero exit here already
# proves the whole chain converged; the explicit asserts below name the exact
# link on a regression and double as the #37/#118 acceptance checklist.
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

log "assert: the web Service serves the console root (200)"
# Port-forward the web SERVICE (not the pod) so the Service's selector + the
# named http targetPort are exercised end to end, then curl the console root: the
# SPA handler returns index.html (200) for GET /. This proves the public Connect
# API port is reachable through the Service. The authenticated RPC surface + a
# live OAuth round-trip are issue #128's smoke.
kubectl -n "$NAMESPACE" port-forward "service/${WEB}" "${WEB_LOCAL_PORT}:80" >/dev/null 2>&1 &
PF_PID=$!
served=""
for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:${WEB_LOCAL_PORT}/" >/dev/null 2>&1; then
    served=1
    break
  fi
  sleep 1
done
if [ -z "$served" ]; then
  echo "FAIL: the web Service did not serve the console root (200) within the window"
  exit 1
fi

log "PASS: deploy chain converged — Postgres → migrate → seed → voice Available → /readyz 200 → web Available → console root 200"
