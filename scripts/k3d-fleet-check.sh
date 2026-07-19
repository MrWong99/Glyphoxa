#!/usr/bin/env bash
# k3d-fleet-check.sh — issue #492 (ADR-0057): MANUAL fleet check for the
# multi-replica voice pool. NOT wired into CI: it needs a LIVE Discord bot token
# and a human to type `/roll` and eyeball the reply, which an unattended runner
# cannot supply (the same reason the voice channel-join and the full OAuth login
# stay manual in scripts/e2e-deploy-smoke.sh). Run it by hand against a local k3d
# cluster to convince yourself of the four things the unit/helm tests can only
# prove in the small:
#
#   1. replicas=2 both reach Available (the shared pool boots N Voice Instances);
#   2. `/roll` is handled EXACTLY ONCE with two replicas up (ADR-0057 (c): every
#      gateway session on the shared token sees the interaction, but only the one
#      elected presence owner dispatches it — the non-owner drops its duplicate);
#   3. failover — kill the owner pod and a survivor wins the presence_owner
#      election within ~20s (default expiry 15s + one renew interval), so the
#      fleet keeps dispatching interactions with no operator action;
#   4. the IDENTIFY budget guard (#486) holds under fleet cold-start: disgo
#      serializes IDENTIFYs per client (max_concurrency 1, one per 5s), so two
#      replicas booting on the shared token must NOT blow the 1000/24h budget —
#      the glyphoxa_gateway_identify_total counters stay small and sane.
#
# The claim plane (#491) and presence-owner election (#492) are what make several
# voice pods coexist on one central token; this script exercises the whole shape
# end to end on a real cluster.
#
# Cluster lifecycle is the caller's (mirrors e2e-deploy-smoke.sh):
#
#   k3d cluster create gx-fleet
#   docker build -t glyphoxa:fleet . && k3d image import glyphoxa:fleet -c gx-fleet
#   DISCORD_BOT_TOKEN=... VOICE_GUILD=... VOICE_CHANNEL=... ./scripts/k3d-fleet-check.sh
#   k3d cluster delete gx-fleet
#
# All knobs are env vars with defaults. A LIVE DISCORD_BOT_TOKEN is REQUIRED
# (step 2/3 need the bot online to receive `/roll`); without it the script still
# runs steps 1 and 4 but SKIPS the interaction and failover-dispatch checks with a
# loud notice.
set -euo pipefail

RELEASE="${RELEASE:-glyphoxa-fleet}"
NAMESPACE="${NAMESPACE:-glyphoxa-fleet}"
CHART="${CHART:-deploy/charts/glyphoxa}"
IMAGE_REPO="${IMAGE_REPO:-glyphoxa}"
IMAGE_TAG="${IMAGE_TAG:-fleet}"
REPLICAS="${REPLICAS:-2}"
TIMEOUT="${TIMEOUT:-300s}"
# How long to allow for a survivor to win the election after the owner is killed.
# Default owner expiry is 15s (GLYPHOXA_PRESENCE_OWNER_EXPIRY) plus one 5s renew
# interval, so ~20s is the worst case; give headroom for pod scheduling.
FAILOVER_TIMEOUT="${FAILOVER_TIMEOUT:-45}"
# Local port the per-pod /metrics listener is forwarded to for the IDENTIFY scrape.
METRICS_LOCAL_PORT="${METRICS_LOCAL_PORT:-19091}"

# Live Discord credentials for the interaction checks (steps 2 and 3). Optional:
# absent, those steps are skipped with a notice.
DISCORD_BOT_TOKEN="${DISCORD_BOT_TOKEN:-}"
VOICE_GUILD="${VOICE_GUILD:-}"
VOICE_CHANNEL="${VOICE_CHANNEL:-}"

note() { printf '\n\033[1;36mk3d-fleet-check: %s\033[0m\n' "$*"; }
warn() { printf '\n\033[1;33mk3d-fleet-check: %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mk3d-fleet-check: %s\033[0m\n' "$*" >&2; exit 1; }

command -v kubectl >/dev/null || fail "kubectl not found"
command -v helm >/dev/null || fail "helm not found"

# psql against the in-cluster Postgres (the migrate/seed hooks' DB). Reads the DSN
# the chart wired into the app Secret, then runs the query inside the postgres pod.
psql_query() {
  local sql="$1"
  kubectl -n "$NAMESPACE" exec deploy/"$RELEASE"-glyphoxa-postgres -- \
    psql -qtAX -U glyphoxa -d glyphoxa -c "$sql"
}

# The elected presence owner's instance_id is the presence_owner singleton row;
# its prefix is the owning pod's hostname (== pod name), so it maps a claim back
# to a pod (newVoiceInstanceID: hostname-uuid8).
current_owner_instance() { psql_query "SELECT instance_id FROM presence_owner;"; }

install_release() {
  note "installing $RELEASE with voice.replicas=$REPLICAS (image $IMAGE_REPO:$IMAGE_TAG)"
  local args=(
    --namespace "$NAMESPACE" --create-namespace
    --set image.repository="$IMAGE_REPO" --set image.tag="$IMAGE_TAG"
    --set voice.replicas="$REPLICAS"
    # A voice-only fleet: the web tier is not needed for this check, and disabling
    # it drops the OAuth-credential requirements.
    --set web.enabled=false
    --wait --timeout "$TIMEOUT"
  )
  if [[ -n "$DISCORD_BOT_TOKEN" ]]; then
    [[ -n "$VOICE_GUILD" && -n "$VOICE_CHANNEL" ]] || fail "with DISCORD_BOT_TOKEN set, VOICE_GUILD and VOICE_CHANNEL are required"
    args+=(--set-string discordBotToken="$DISCORD_BOT_TOKEN"
           --set-string voice.guild="$VOICE_GUILD"
           --set-string voice.channel="$VOICE_CHANNEL")
  else
    # No live token: a placeholder still lets the pods reach Available (#44
    # resilience — /readyz pings the DB, not Discord), enough for steps 1 and 4.
    args+=(--set-string discordBotToken=placeholder-fleet-token
           --set-string voice.guild="111111111111111111"
           --set-string voice.channel="222222222222222222")
  fi
  # The provider keys are `required` when voice.enabled; dummies suffice (no
  # provider is called in this check).
  args+=(--set-string elevenLabsApiKey=placeholder
         --set-string geminiApiKey=placeholder
         --set-string groqApiKey=placeholder)
  helm upgrade --install "$RELEASE" "$CHART" "${args[@]}"
}

# --- Step 1: both replicas Available -----------------------------------------
step_replicas_available() {
  note "step 1: waiting for $REPLICAS voice replicas to be Available"
  kubectl -n "$NAMESPACE" rollout status deploy/"$RELEASE"-glyphoxa-voice --timeout "$TIMEOUT"
  local ready
  ready=$(kubectl -n "$NAMESPACE" get deploy/"$RELEASE"-glyphoxa-voice -o jsonpath='{.status.readyReplicas}')
  [[ "$ready" == "$REPLICAS" ]] || fail "step 1: readyReplicas=$ready, want $REPLICAS"
  note "step 1 OK: $ready/$REPLICAS voice pods Ready"
}

# --- Step 4: IDENTIFY counters sane under cold-start -------------------------
# Scrapes each voice pod's /metrics and sums glyphoxa_gateway_identify_total. With
# two pods cold-starting on the shared token the total must be small (a handful),
# never a budget-threatening burst — disgo's max_concurrency 1 serializes the
# IDENTIFYs one per 5s per token (#486, ADR-0057 P5). RESUME is free; a churny
# reconnect shows up in glyphoxa_gateway_resume_total, not identify.
step_identify_budget() {
  note "step 4: scraping IDENTIFY counters across the fleet (budget guard, #486)"
  local pods total=0
  pods=$(kubectl -n "$NAMESPACE" get pods -l app.kubernetes.io/component=voice -o name)
  [[ -n "$pods" ]] || fail "step 4: no voice pods found"
  for pod in $pods; do
    kubectl -n "$NAMESPACE" port-forward "$pod" "${METRICS_LOCAL_PORT}:9090" >/dev/null 2>&1 &
    local pf=$!
    sleep 2
    local n
    n=$(curl -fsS "http://127.0.0.1:${METRICS_LOCAL_PORT}/metrics" 2>/dev/null \
          | awk '/^glyphoxa_gateway_identify_total/ { s += $NF } END { printf "%d", s }') || n=0
    kill "$pf" >/dev/null 2>&1 || true
    wait "$pf" 2>/dev/null || true
    note "  $pod: identify_total=$n"
    total=$((total + n))
  done
  # A sane cold-start is a few IDENTIFYs per pod. Flag a blowout (an obvious
  # threshold well under the 1000/24h budget) so a serialization regression is
  # caught even without live traffic.
  note "step 4: fleet identify_total=$total"
  if (( total > 50 )); then
    fail "step 4: identify_total=$total looks like a budget blowout — is IDENTIFY serialization (#486) intact?"
  fi
  note "step 4 OK: IDENTIFY budget guard holds under cold-start"
}

# --- Step 3: failover — kill owner, survivor re-elected ----------------------
step_failover() {
  note "step 3: presence-owner failover"
  local owner_instance owner_pod
  owner_instance=$(current_owner_instance)
  [[ -n "$owner_instance" ]] || fail "step 3: no presence_owner row — did an owner ever get elected? (needs a live token)"
  # instance_id is <pod-name>-<uuid8>; strip the uuid8 suffix to get the pod name.
  owner_pod="${owner_instance%-*}"
  note "step 3: current owner instance=$owner_instance (pod $owner_pod)"
  note "step 3: deleting the owner pod"
  kubectl -n "$NAMESPACE" delete pod "$owner_pod" --wait=false

  local deadline=$(( SECONDS + FAILOVER_TIMEOUT )) new_owner=""
  while (( SECONDS < deadline )); do
    new_owner=$(current_owner_instance || true)
    if [[ -n "$new_owner" && "$new_owner" != "$owner_instance" ]]; then
      note "step 3 OK: survivor elected new owner=$new_owner within $(( SECONDS - (deadline - FAILOVER_TIMEOUT) ))s"
      return
    fi
    sleep 2
  done
  fail "step 3: no new owner elected within ${FAILOVER_TIMEOUT}s (still $new_owner)"
}

# --- Step 2: /roll handled exactly once (human in the loop) ------------------
step_roll_once() {
  note "step 2: exactly-once /roll (MANUAL — needs you at a Discord client)"
  cat <<'EOF'
  With BOTH voice replicas up and one elected presence owner:
    1. In the configured guild, run  /roll 1d20  (or any /roll).
    2. You must see EXACTLY ONE reply. Two replies = the SetActive gate failed and
       both replicas dispatched (ADR-0057 (c) regression). Zero replies = the
       owner is not registering commands (election or client-registry problem).
    3. Cross-check the logs: exactly one pod should log the dispatch. Run:
         kubectl -n NAMESPACE logs -l app.kubernetes.io/component=voice \
           --prefix --tail=200 | grep -i 'slash command'
       and confirm a single pod handled it.
EOF
  warn "step 2 is a human observation; the script cannot type /roll for you"
}

install_release
step_replicas_available
step_identify_budget
if [[ -n "$DISCORD_BOT_TOKEN" ]]; then
  step_roll_once
  step_failover
else
  warn "DISCORD_BOT_TOKEN unset: skipping step 2 (/roll) and step 3 (failover dispatch) — set it plus VOICE_GUILD/VOICE_CHANNEL to run them"
fi

note "done. Tear the release down with: helm -n $NAMESPACE uninstall $RELEASE"
