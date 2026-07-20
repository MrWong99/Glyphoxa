#!/usr/bin/env bash
# k3d-fleet-check.sh — issue #492 (ADR-0057): MANUAL fleet check for the
# multi-replica voice pool. NOT wired into CI: it needs a LIVE Discord bot token
# and a human to type `/roll` and eyeball the reply, which an unattended runner
# cannot supply (the same reason the voice channel-join and the full OAuth login
# stay manual in scripts/e2e-deploy-smoke.sh). Run it by hand against a local k3d
# cluster to convince yourself of the four things the unit/helm tests can only
# prove in the small:
#
#   1. replicas=2 both reach Available AS CLAIM-PLANE WORKERS (the pods run
#      `glyphoxa -mode voice` with NO -guild/-channel — the shared pool takes each
#      session's target from the Tenant's saved config, #491);
#   2. `/roll` is handled EXACTLY ONCE with two replicas up (ADR-0057 (c): every
#      gateway session on the shared token sees the interaction, but only the one
#      elected presence owner dispatches it — the non-owner drops its duplicate);
#   3. failover — kill the owner pod and a survivor wins the presence_owner
#      election within ~20s (default expiry 15s + one renew interval), so the
#      fleet keeps dispatching interactions with no operator action;
#   5. cross-pod live controls (#503): with the session HOSTED by one pod and the
#      presence OWNER being the other, /glyphoxa mute lands a
#      voice_session_controls row the HOSTING worker's loop executes (row goes
#      'done' within the control budget) and the GM sees one "is muted." followup;
#   4. the IDENTIFY budget guard (#486) holds under fleet cold-start: disgo
#      serializes IDENTIFYs per client (max_concurrency 1, one per 5s), so two
#      replicas booting on the shared token must NOT blow the 1000/24h budget —
#      the glyphoxa_gateway_identify_total counters stay small and sane.
#
# WORKER TOPOLOGY, not standalone: `glyphoxa -mode voice` boots as the claim-plane
# worker (with the OwnerElector) ONLY when BOTH -guild and -channel are unset
# (main.go); setting either flips it to the LEGACY STANDALONE node, which has no
# elector and no presence_owner row. So this script leaves voice.guild/voice.channel
# EMPTY. The elector electing an owner (a presence_owner row appearing) is itself
# part of what step 3 verifies.
#
# The claim plane (#491) and presence-owner election (#492) are what make several
# voice pods coexist on one central token; this script exercises the whole shape
# end to end on a real cluster.
#
# Cluster lifecycle is the caller's (mirrors e2e-deploy-smoke.sh):
#
#   k3d cluster create gx-fleet
#   docker build -t glyphoxa:fleet . && k3d image import glyphoxa:fleet -c gx-fleet
#   DISCORD_BOT_TOKEN=... ./scripts/k3d-fleet-check.sh
#   k3d cluster delete gx-fleet
#
# All knobs are env vars with defaults. A LIVE DISCORD_BOT_TOKEN is REQUIRED for
# steps 2 and 3 (the bot must be online to receive `/roll` and to elect an owner);
# without it the script still runs steps 1 and 4 but SKIPS the interaction and
# failover checks with a loud notice.
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

# Resource names. The chart's fullname == RELEASE when RELEASE contains the chart
# name "glyphoxa" (the default does); the StatefulSet/Deployment suffixes mirror
# scripts/e2e-deploy-smoke.sh.
PG="${RELEASE}-postgres"     # Postgres StatefulSet
VOICE="${RELEASE}-voice"     # voice Deployment

# Live Discord credentials for the interaction/election checks (steps 2 and 3).
# Optional: absent, those steps are skipped with a notice. NOTE: no VOICE_GUILD/
# VOICE_CHANNEL — worker mode requires them UNSET (see the header).
DISCORD_BOT_TOKEN="${DISCORD_BOT_TOKEN:-}"

note() { printf '\n\033[1;36mk3d-fleet-check: %s\033[0m\n' "$*"; }
warn() { printf '\n\033[1;33mk3d-fleet-check: %s\033[0m\n' "$*"; }
fail() { printf '\n\033[1;31mk3d-fleet-check: %s\033[0m\n' "$*" >&2; exit 1; }

command -v kubectl >/dev/null || fail "kubectl not found"
command -v helm >/dev/null || fail "helm not found"

# psql against the in-cluster Postgres. Postgres is a StatefulSet (not a Deployment),
# so exec into statefulset/<pg> (mirrors e2e-deploy-smoke.sh's statefulset/ handle).
psql_query() {
  local sql="$1"
  kubectl -n "$NAMESPACE" exec "statefulset/${PG}" -- \
    psql -qtAX -U glyphoxa -d glyphoxa -c "$sql"
}

# The elected presence owner's instance_id is the presence_owner singleton row; its
# prefix is the owning pod's hostname (== pod name), so it maps a claim back to a pod
# (newVoiceInstanceID: hostname-uuid8).
current_owner_instance() { psql_query "SELECT instance_id FROM presence_owner;"; }

install_release() {
  note "installing $RELEASE with voice.replicas=$REPLICAS (image $IMAGE_REPO:$IMAGE_TAG), voice-only WORKER mode"
  # A dummy 32-byte base64 app secret: voice pods now MOUNT GLYPHOXA_SECRET (#492,
  # ADR-0057 (d)) so the chart requires appSecret even with the web tier off. No real
  # BYOK decryption happens in this check.
  local app_secret
  app_secret="$(head -c 32 /dev/zero | base64)"
  local args=(
    --namespace "$NAMESPACE" --create-namespace
    --set image.repository="$IMAGE_REPO" --set image.tag="$IMAGE_TAG"
    --set voice.replicas="$REPLICAS"
    # Voice-only fleet: web off drops the OAuth-credential requirements.
    --set web.enabled=false
    # WORKER MODE: guild/channel stay EMPTY so the pods run the claim-plane worker
    # with the OwnerElector, NOT the legacy standalone node (see the header).
    --set-string voice.guild=""
    --set-string voice.channel=""
    --set-string appSecret="$app_secret"
    --wait --timeout "$TIMEOUT"
  )
  # The bot token (required by the chart) plus dummy provider keys (no provider is
  # called here). A live token lets the pods actually open the gateway and elect an
  # owner; a placeholder still boots them to Ready (#44 resilience — /readyz pings the
  # DB, not Discord) for steps 1 and 4.
  if [[ -n "$DISCORD_BOT_TOKEN" ]]; then
    args+=(--set-string discordBotToken="$DISCORD_BOT_TOKEN")
  else
    args+=(--set-string discordBotToken=placeholder-fleet-token)
  fi
  args+=(--set-string elevenLabsApiKey=placeholder
         --set-string geminiApiKey=placeholder
         --set-string groqApiKey=placeholder)
  helm upgrade --install "$RELEASE" "$CHART" "${args[@]}"
}

# --- Step 1: both replicas Available as workers -------------------------------
step_replicas_available() {
  note "step 1: waiting for $REPLICAS voice replicas (claim-plane workers) to be Available"
  kubectl -n "$NAMESPACE" rollout status "deployment/${VOICE}" --timeout "$TIMEOUT"
  local ready
  ready=$(kubectl -n "$NAMESPACE" get "deployment/${VOICE}" -o jsonpath='{.status.readyReplicas}')
  [[ "$ready" == "$REPLICAS" ]] || fail "step 1: readyReplicas=$ready, want $REPLICAS"
  # Confirm the pods really are workers (no -guild/-channel in the arg vector).
  local args
  args=$(kubectl -n "$NAMESPACE" get "deployment/${VOICE}" -o jsonpath='{.spec.template.spec.containers[0].args}')
  if grep -q -- '-guild' <<<"$args"; then
    fail "step 1: voice args carry -guild → pods run LEGACY STANDALONE, not workers: $args"
  fi
  note "step 1 OK: $ready/$REPLICAS worker pods Ready (no -guild/-channel)"
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
  note "step 4: fleet identify_total=$total"
  if (( total > 50 )); then
    fail "step 4: identify_total=$total looks like a budget blowout — is IDENTIFY serialization (#486) intact?"
  fi
  note "step 4 OK: IDENTIFY budget guard holds under cold-start"
}

# --- Step 3: failover — kill owner, survivor re-elected ----------------------
step_failover() {
  note "step 3: presence-owner failover"
  # An elected owner presupposes the pods opened the gateway — only a LIVE token does
  # that. Wait for the row to appear (the elector's first successful acquire).
  local owner_instance owner_pod deadline
  deadline=$(( SECONDS + FAILOVER_TIMEOUT ))
  while (( SECONDS < deadline )); do
    owner_instance=$(current_owner_instance || true)
    [[ -n "$owner_instance" ]] && break
    sleep 2
  done
  [[ -n "$owner_instance" ]] || fail "step 3: no presence_owner row appeared — did an owner get elected? (needs a live token so the gateway opens)"
  # instance_id is <pod-name>-<uuid8>; strip the uuid8 suffix to get the pod name.
  owner_pod="${owner_instance%-*}"
  note "step 3: current owner instance=$owner_instance (pod $owner_pod)"
  note "step 3: deleting the owner pod"
  kubectl -n "$NAMESPACE" delete pod "$owner_pod" --wait=false

  deadline=$(( SECONDS + FAILOVER_TIMEOUT ))
  local new_owner=""
  while (( SECONDS < deadline )); do
    new_owner=$(current_owner_instance || true)
    if [[ -n "$new_owner" && "$new_owner" != "$owner_instance" ]]; then
      note "step 3 OK: survivor elected new owner=$new_owner (was $owner_instance)"
      return
    fi
    sleep 2
  done
  fail "step 3: no new owner elected within ${FAILOVER_TIMEOUT}s (still $new_owner)"
}

# --- Step 5: cross-pod mute via the control queue (#503, human in the loop) --
# Exercises the requested-controls relay: the presence OWNER pod dispatches
# /glyphoxa mute while the session is HOSTED by the OTHER pod. The human drives
# Discord (start a session, split host from owner, run the mute); the script
# verifies the plane — the voice_session_controls row landing status='done'
# within the control budget proves the hosting worker's loop executed it.
CONTROL_BUDGET="${CONTROL_BUDGET:-15}"
step_cross_pod_mute() {
  note "step 5: cross-pod mute via voice_session_controls (#503 — MANUAL steps + psql checks)"
  cat <<EOF
  With BOTH replicas up and an owner elected:
    1. In Discord, run  /glyphoxa start  (your Tenant needs a saved guild/channel
       and an Active Campaign with at least one voiced NPC).
EOF
  note "step 5: waiting for a live intent row"
  local host_instance="" owner_instance="" deadline
  deadline=$(( SECONDS + FAILOVER_TIMEOUT ))
  while (( SECONDS < deadline )); do
    host_instance=$(psql_query "SELECT instance_id FROM voice_session_intents WHERE status='live' ORDER BY created_at DESC LIMIT 1;" || true)
    [[ -n "$host_instance" ]] && break
    sleep 2
  done
  [[ -n "$host_instance" ]] || fail "step 5: no live voice_session_intents row appeared — did /glyphoxa start succeed?"
  owner_instance=$(current_owner_instance || true)
  note "step 5: session host=$host_instance, presence owner=$owner_instance"
  if [[ "${host_instance%-*}" == "${owner_instance%-*}" ]]; then
    warn "step 5: host and owner are the SAME pod — the mute would take the local path."
    cat <<EOF
    Run  /glyphoxa end  then  /glyphoxa start  again until the claiming pod
    differs from the presence owner (with 2 replicas the claim is a race; a few
    tries suffice), then re-run this script step.
EOF
    fail "step 5: host == owner; split them and re-run"
  fi
  cat <<EOF
    2. Host and owner are DIFFERENT pods — now run  /glyphoxa mute npc:<name>
       in Discord. You must see the deferred "thinking…" then EXACTLY ONE
       ephemeral "<name> is muted." followup. An error message = the relay
       surfaced a real failure (also a #503 behavior, but the mute did not land).
EOF
  note "step 5: polling voice_session_controls for a done mute_agent row (${CONTROL_BUDGET}s + slack)"
  local status="" ; deadline=$(( SECONDS + CONTROL_BUDGET + 30 ))
  while (( SECONDS < deadline )); do
    status=$(psql_query "SELECT status FROM voice_session_controls WHERE kind='mute_agent' ORDER BY created_at DESC LIMIT 1;" || true)
    [[ "$status" == "done" ]] && break
    sleep 2
  done
  [[ "$status" == "done" ]] || fail "step 5: newest mute_agent control status='${status:-<none>}', want done within the control budget — did the hosting worker's loop dispatch it?"
  note "step 5 OK: cross-pod mute executed by the hosting worker (control row done); confirm the single 'is muted.' followup in Discord"
  note "step 5 cleanup: run /glyphoxa end when finished"
}

# --- Step 2: /roll handled exactly once (human in the loop) ------------------
step_roll_once() {
  note "step 2: exactly-once /roll (MANUAL — needs you at a Discord client)"
  cat <<EOF
  With BOTH voice worker replicas up and one elected presence owner:
    1. In a guild the bot is in, run  /roll 1d20  (or any /roll).
    2. You must see EXACTLY ONE reply. Two replies = the SetActive gate failed and
       both replicas dispatched (ADR-0057 (c) regression). Zero replies = the
       owner is not registering commands (election or client-registry problem).
    3. Cross-check the logs: exactly one pod should log the dispatch. Run:
         kubectl -n ${NAMESPACE} logs -l app.kubernetes.io/component=voice \\
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
  step_cross_pod_mute
  step_failover
else
  warn "DISCORD_BOT_TOKEN unset: skipping step 2 (/roll), step 5 (cross-pod mute) and step 3 (failover) — set it to run them (guild/channel MUST stay unset for worker mode)"
fi

note "done. Tear the release down with: helm -n $NAMESPACE uninstall $RELEASE"
