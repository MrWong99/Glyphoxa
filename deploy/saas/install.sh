#!/usr/bin/env bash
# deploy/saas/install.sh — stand up a complete single-box Glyphoxa SaaS server
# on k3s, from a bare Linux host (the Hetzner path of
# docs/deploy/cloud-providers.md; every step mirrors docs/deploy/k3s-proxmox.md
# §2–§8, which stays the reference for what each piece is and why).
#
# What it does, in order:
#   1. asks for the parameters it needs (DNS name, ACME email, Discord OAuth +
#      bot credentials, operator allowlist, Admission Mode, Ollama URL,
#      optional platform provider keys, optional on-disk backup) — every
#      prompt can be pre-answered with the GX_* env var it names, and a
#      non-interactive run (no TTY) requires exactly those vars;
#   2. installs k3s (skipped when already present, or with --skip-k3s to
#      target the current kubeconfig context instead) and helm if missing;
#   3. installs cert-manager + a Let's Encrypt ClusterIssuer (unless GX_TLS=none);
#   4. resolves the latest released version (or --version), downloads that
#      release's source tarball, and installs ITS chart with the image tag
#      pinned to the same release — chart and image can never drift;
#   5. writes the values file (0600) and an install-state file the companion
#      update script (deploy/saas/update.sh) reads;
#   6. optionally applies a nightly pg_dump CronJob writing to a path on the
#      node's disk, with retention.
#
# Re-running is safe: an existing values file is REUSED (never regenerated —
# rotating appSecret would make every sealed BYOK credential undecryptable,
# ADR-0004), k3s/helm/cert-manager installs are skipped when present, and the
# chart install is `helm upgrade --install`.
#
# --dry-run gathers parameters, writes the values/state/backup files and
# prints the plan, but touches neither the host nor any cluster — the
# self-test (install-test.sh) runs entirely through it.
set -euo pipefail

# --------------------------------------------------------------- defaults --
# Only what is needed to FIND the state file is defaulted here; everything
# else defaults after the state-file prefill below, so a re-run keeps a
# previous install's choices instead of silently resetting them.
GX_CONFIG_DIR="${GX_CONFIG_DIR:-/etc/glyphoxa}"
GX_ACME_SERVER="${GX_ACME_SERVER:-https://acme-v02.api.letsencrypt.org/directory}"

DRY_RUN=""
SKIP_K3S="${GX_SKIP_K3S:-}"
FRESH_VALUES=""

usage() {
  cat <<'EOF'
usage: install.sh [options]

Options:
  --version vX.Y.Z   install this released version (default: latest release on
                     a FIRST install; a re-run keeps the version recorded in
                     install.env — upgrading is deploy/saas/update.sh's job)
  --config-dir DIR   where values/state live (default: /etc/glyphoxa)
  --skip-k3s         do not install/use k3s; deploy to the CURRENT kubeconfig
                     context (for existing clusters and local testing)
  --fresh-values     regenerate the values file even if one exists. DANGER:
                     rotates appSecret — sealed BYOK provider credentials
                     become undecryptable (ADR-0004). Never use on a live box.
  --dry-run          gather parameters + write values/state, touch nothing else
  -h, --help         this text

Every prompt reads its GX_* env var first (GX_HOST, GX_ACME_EMAIL,
GX_DISCORD_CLIENT_ID, GX_DISCORD_CLIENT_SECRET, GX_DISCORD_BOT_TOKEN,
GX_OPERATOR_IDS, GX_ADMISSION_MODE, GX_SIGNUP_PLAN_SLUG, GX_OLLAMA_URL,
GX_GROQ_API_KEY, GX_ELEVENLABS_API_KEY, GX_GEMINI_API_KEY, GX_TLS,
GX_BACKUP_DIR, GX_BACKUP_SCHEDULE, GX_BACKUP_RETENTION_DAYS, GX_PG_DISK);
a run without a TTY takes ONLY the env vars and fails naming any missing
required one, so unattended installs are reproducible.
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --version) GX_VERSION="${2:?--version needs a tag}"; shift 2 ;;
    --config-dir) GX_CONFIG_DIR="${2:?--config-dir needs a path}"; shift 2 ;;
    --skip-k3s) SKIP_K3S=1; shift ;;
    --fresh-values) FRESH_VALUES=1; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "install.sh: unknown option '$1' (see --help)" >&2; exit 2 ;;
  esac
done

log()  { printf '\n=== %s ===\n' "$*"; }
note() { printf '    %s\n' "$*"; }
die()  { echo "install.sh: ERROR: $*" >&2; exit 1; }

need_cmd() { command -v "$1" >/dev/null 2>&1 || die "required command '$1' not found"; }

# yesc escapes a value for a double-quoted YAML scalar (backslash, then quote).
yesc() { local s="${1-}"; s="${s//\\/\\\\}"; s="${s//\"/\\\"}"; printf '%s' "$s"; }

# ask VAR "prompt" ["default"]: keep an env-provided value; otherwise prompt
# (with default), or fail actionably when there is no TTY. Three-arg form is
# optional (empty default allowed); two-arg form is required (loops until
# non-empty).
ask() {
  local var="$1" text="$2" def="${3-__REQUIRED__}" val
  if [ -n "${!var-}" ]; then return 0; fi
  if [ ! -t 0 ]; then
    if [ "$def" != "__REQUIRED__" ]; then printf -v "$var" '%s' "$def"; return 0; fi
    die "no TTY and \$$var is unset — set $var (see --help)"
  fi
  while :; do
    if [ "$def" != "__REQUIRED__" ]; then
      read -r -p "$text [${def}]: " val || die "input aborted"
      val="${val:-$def}"
    else
      read -r -p "$text: " val || die "input aborted"
    fi
    if [ -n "$val" ] || [ "$def" != "__REQUIRED__" ]; then break; fi
    echo "  (required)"
  done
  printf -v "$var" '%s' "$val"
}

# ask_secret VAR "prompt": like required ask, but no echo.
ask_secret() {
  local var="$1" text="$2" val
  if [ -n "${!var-}" ]; then return 0; fi
  [ -t 0 ] || die "no TTY and \$$var is unset — set $var (see --help)"
  while :; do
    read -r -s -p "$text: " val || die "input aborted"
    echo
    [ -n "$val" ] && break
    echo "  (required)"
  done
  printf -v "$var" '%s' "$val"
}

# ask_secret_optional VAR "prompt": no echo, empty allowed (Enter to skip).
ask_secret_optional() {
  local var="$1" text="$2" val
  if [ -n "${!var-}" ]; then return 0; fi
  if [ ! -t 0 ]; then printf -v "$var" '%s' ""; return 0; fi
  read -r -s -p "$text: " val || die "input aborted"
  echo
  printf -v "$var" '%s' "$val"
}

# ----------------------------------------------------------------- preflight --
need_cmd curl
need_cmd tar
need_cmd openssl

if [ -z "$DRY_RUN" ] && [ -z "$SKIP_K3S" ] && [ "$(id -u)" -ne 0 ]; then
  die "installing k3s needs root — re-run with sudo (or use --skip-k3s to target an existing cluster)"
fi

VALUES_FILE="${GX_CONFIG_DIR}/values.yaml"
STATE_FILE="${GX_CONFIG_DIR}/install.env"
BACKUP_MANIFEST="${GX_CONFIG_DIR}/backup-cronjob.yaml"

# Remember which of the two deploy-shaping parameters arrived via the REAL
# environment (before the state prefill), so a reuse-run can tell an explicit
# override (a conflict to refuse) from a state echo (nothing to do) below.
ENV_GX_HOST="${GX_HOST-}"
ENV_GX_TLS="${GX_TLS-}"

# A previous install's state prefills GX_* the caller did not set, so a re-run
# only prompts for what it cannot know. Set-but-EMPTY counts as set (an
# explicit `GX_BACKUP_DIR=` must be able to clear a recorded choice), hence
# the +x test. Parsed line-wise, NOT sourced: values like a cron schedule
# contain spaces/globs that `source` would mangle.
if [ -f "$STATE_FILE" ]; then
  while IFS='=' read -r k v; do
    case "$k" in
      GX_*) [ -n "${!k+x}" ] || printf -v "$k" '%s' "$v" ;;
    esac
  done <"$STATE_FILE"
fi

# Hard defaults for whatever neither the env nor a previous state provided.
GX_REPO="${GX_REPO:-MrWong99/Glyphoxa}"
GX_RELEASE="${GX_RELEASE:-glyphoxa}"
GX_NAMESPACE="${GX_NAMESPACE:-glyphoxa}"
GX_VERSION="${GX_VERSION:-}"                 # empty = latest published release
GX_TLS="${GX_TLS:-}"                         # letsencrypt | none (prompted)
GX_PG_DISK="${GX_PG_DISK:-20Gi}"
GX_TIMEOUT="${GX_TIMEOUT:-600s}"
GX_BACKUP_SCHEDULE="${GX_BACKUP_SCHEDULE:-15 4 * * *}"
GX_BACKUP_RETENTION_DAYS="${GX_BACKUP_RETENTION_DAYS:-14}"

REUSE_VALUES=""
[ -f "$VALUES_FILE" ] && [ -z "$FRESH_VALUES" ] && REUSE_VALUES=1

# ---------------------------------------------------------------- parameters --
log "Glyphoxa SaaS install — parameters"

ask GX_HOST "Public DNS name the console will be served on (e.g. glyphoxa.example.com)"
case "$GX_HOST" in
  *[!a-zA-Z0-9.-]*|"") die "GX_HOST '$GX_HOST' is not a plausible DNS name" ;;
esac

ask GX_TLS "TLS: 'letsencrypt' (cert-manager + HTTP-01, needs the DNS name pointing here) or 'none' (plain HTTP / TLS terminated elsewhere)" "letsencrypt"
case "$GX_TLS" in
  letsencrypt)
    ask GX_ACME_EMAIL "Email for Let's Encrypt expiry notices"
    # Shape check before the value is interpolated into the ClusterIssuer
    # manifest (same discipline as the GX_HOST check above).
    case "$GX_ACME_EMAIL" in
      *[!A-Za-z0-9._%+@-]*|*@*@*|*@|@*|"") die "GX_ACME_EMAIL '$GX_ACME_EMAIL' is not a plausible email" ;;
      *@*) : ;;
      *) die "GX_ACME_EMAIL '$GX_ACME_EMAIL' is not a plausible email" ;;
    esac
    ;;
  none) GX_ACME_EMAIL="" ;;
  *) die "GX_TLS must be 'letsencrypt' or 'none', got '$GX_TLS'" ;;
esac

if [ -n "$REUSE_VALUES" ]; then
  note "existing ${VALUES_FILE} found — credentials, admission mode and provider"
  note "keys are taken from it (edit that file to change them; --fresh-values regenerates)."
else
  note "Discord application (docs/configuration.md §5: OAuth2 client + bot token)."
  ask GX_DISCORD_CLIENT_ID "Discord OAuth client id"
  ask_secret GX_DISCORD_CLIENT_SECRET "Discord OAuth client secret"
  ask_secret GX_DISCORD_BOT_TOKEN "Discord bot token"

  ask GX_ADMISSION_MODE "Admission Mode (ADR-0055): 'allowlist' (only the operators below may log in) or 'open' (any Discord user founds a Tenant)" "allowlist"
  case "$GX_ADMISSION_MODE" in
    allowlist)
      ask GX_OPERATOR_IDS "Operator Discord snowflake(s), comma-separated (Discord: Settings > Advanced > Developer Mode, then Copy ID)"
      GX_SIGNUP_PLAN_SLUG=""
      ;;
    open)
      ask GX_OPERATOR_IDS "Platform-admin Discord snowflake(s), comma-separated (may be empty; the pod then warns 'no platform admins')" ""
      ask GX_SIGNUP_PLAN_SLUG "Plan slug every signup is bound to (synced into the DB as a \$0 BYOK tier; edit the values file later for real tiers — docs/deploy/saas-operations.md §1)" "byok-free"
      case "$GX_SIGNUP_PLAN_SLUG" in
        *[!a-z0-9-]*|"") die "GX_SIGNUP_PLAN_SLUG must be lowercase alphanumerics + hyphens" ;;
      esac
      ;;
    *) die "GX_ADMISSION_MODE must be 'allowlist' or 'open', got '$GX_ADMISSION_MODE'" ;;
  esac
  case "${GX_OPERATOR_IDS//[0-9, ]/}" in
    "") : ;;
    *) die "GX_OPERATOR_IDS must be numeric Discord snowflakes (comma/space separated)" ;;
  esac

  ask GX_OLLAMA_URL "Ollama URL for embeddings (serving nomic-embed-text; empty disables semantic memory L2 with a WARN loop — docs/configuration.md)" ""

  note "Platform provider keys (ADR-0054): leave empty for a pure-BYOK deployment;"
  note "fill to sell 'usage included' tiers on YOUR keys (saas-operations.md §2)."
  ask_secret_optional GX_GROQ_API_KEY "Groq API key (optional, hidden; Enter to skip)"
  ask_secret_optional GX_ELEVENLABS_API_KEY "ElevenLabs API key (optional, hidden; Enter to skip)"
  ask_secret_optional GX_GEMINI_API_KEY "Gemini API key (optional, hidden; Enter to skip)"
fi

ask GX_BACKUP_DIR "Nightly pg_dump backup directory on the node's disk (empty to skip backups)" ""
if [ -n "$GX_BACKUP_DIR" ]; then
  case "$GX_BACKUP_DIR" in
    /*) : ;;
    *) die "GX_BACKUP_DIR must be an absolute path" ;;
  esac
  ask GX_BACKUP_SCHEDULE "Backup cron schedule (UTC)" "$GX_BACKUP_SCHEDULE"
  case "$GX_BACKUP_SCHEDULE" in
    *[!0-9\*/,\ -]*|"") die "GX_BACKUP_SCHEDULE '$GX_BACKUP_SCHEDULE' is not a plausible cron schedule" ;;
  esac
  ask GX_BACKUP_RETENTION_DAYS "Days of dumps to keep" "$GX_BACKUP_RETENTION_DAYS"
  case "$GX_BACKUP_RETENTION_DAYS" in
    *[!0-9]*|"") die "GX_BACKUP_RETENTION_DAYS must be a number" ;;
  esac
fi

# On a reuse-run the values FILE is what deploys (only image.tag is passed on
# top), so the effective host/TLS must agree with it. An explicit env override
# is a conflict to refuse (the file wins, silently ignoring the operator would
# be worse); a drift the other way — the operator hand-edited the values file —
# is adopted, since the file is authoritative.
if [ -n "$REUSE_VALUES" ]; then
  FILE_HOST="$(sed -n 's/^  host: "\(.*\)"$/\1/p' "$VALUES_FILE" | head -n1)"
  if [ -n "$FILE_HOST" ] && [ "$FILE_HOST" != "$GX_HOST" ]; then
    if [ -n "$ENV_GX_HOST" ]; then
      die "GX_HOST '$GX_HOST' conflicts with the reused ${VALUES_FILE} (ingress.host '$FILE_HOST') — edit that file, or --fresh-values (DANGER: rotates appSecret)"
    fi
    note "adopting host '$FILE_HOST' from the values file (it is what deploys)"
    GX_HOST="$FILE_HOST"
  fi
  # The only 4-space-indented enabled: in the generated file is
  # ingress.certManager.enabled (the ingress block closes the file).
  FILE_CM="$(grep -E '^    enabled: (true|false)$' "$VALUES_FILE" | tail -n1 | awk '{print $2}')"
  FILE_TLS=none; [ "$FILE_CM" = "true" ] && FILE_TLS=letsencrypt
  if [ -n "$FILE_CM" ] && [ "$FILE_TLS" != "$GX_TLS" ]; then
    if [ -n "$ENV_GX_TLS" ]; then
      die "GX_TLS '$GX_TLS' conflicts with the reused ${VALUES_FILE} (certManager.enabled ${FILE_CM}) — edit that file, or --fresh-values (DANGER: rotates appSecret)"
    fi
    note "adopting TLS mode '$FILE_TLS' from the values file (it is what deploys)"
    GX_TLS="$FILE_TLS"
    [ "$GX_TLS" = "letsencrypt" ] && [ -z "$GX_ACME_EMAIL" ] \
      && ask GX_ACME_EMAIL "Email for Let's Encrypt expiry notices"
  fi
fi

# ------------------------------------------------------------ resolve version --
log "Resolving version"
if [ -z "$GX_VERSION" ]; then
  # `|| true` so a curl failure reaches the actionable die below instead of
  # errexit killing the script with only curl's terse stderr.
  GX_VERSION="$(curl -fsSL -H 'Accept: application/vnd.github+json' \
      "https://api.github.com/repos/${GX_REPO}/releases/latest" \
    | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1 || true)"
  [ -n "$GX_VERSION" ] || die "could not resolve the latest release of ${GX_REPO} (network? rate limit?) — pin one with --version"
fi
note "installing Glyphoxa ${GX_VERSION}"

# The chart names objects <fullname>-<component>, where fullname is the
# release name when it already contains 'glyphoxa' and <release>-glyphoxa
# otherwise (templates/_helpers.tpl). The web Deployment and the app Secret
# (-db) are addressed by that name below and in update.sh.
FULLNAME="$GX_RELEASE"
case "$GX_RELEASE" in
  *glyphoxa*) : ;;
  *) FULLNAME="${GX_RELEASE}-glyphoxa" ;;
esac

# --------------------------------------------------------------- write config --
log "Config in ${GX_CONFIG_DIR}"
mkdir -p "$GX_CONFIG_DIR"
chmod 700 "$GX_CONFIG_DIR" 2>/dev/null || true

if [ -n "$REUSE_VALUES" ]; then
  note "reusing existing ${VALUES_FILE} (secrets kept; --fresh-values to regenerate)"
else
  [ -f "$VALUES_FILE" ] && note "REGENERATING ${VALUES_FILE} — appSecret rotates, sealed BYOK credentials become undecryptable (ADR-0004)"
  APP_SECRET="$(openssl rand -base64 32)"
  DB_PASSWORD="$(openssl rand -hex 24)"   # hex: URL-safe by construction

  PLANS_BLOCK=""
  ADMISSION_BLOCK=""
  if [ "$GX_ADMISSION_MODE" = "open" ]; then
    ADMISSION_BLOCK="
  admissionMode: open
  signupPlanSlug: \"$(yesc "$GX_SIGNUP_PLAN_SLUG")\""
    PLANS_BLOCK="
# Signup tier catalog (ADR-0054/0055): the slug open-mode signups bind to.
# Add real tiers here and re-run install.sh (or a 'helm upgrade' with
# --set-string image.tag pinned) — see docs/deploy/saas-operations.md §1.
plans:
  enabled: true
  catalog:
    plans:
      - slug: \"$(yesc "$GX_SIGNUP_PLAN_SLUG")\"
        display_name: \"BYOK Free\"
        description: \"Bring your own provider keys.\"
        monthly_price_usd: 0"
  fi

  CERTMANAGER_ENABLED=false
  [ "$GX_TLS" = "letsencrypt" ] && CERTMANAGER_ENABLED=true

  umask 077
  cat >"$VALUES_FILE" <<EOF
# Generated by deploy/saas/install.sh — keep 0600, NEVER commit.
# Edit + re-run install.sh (it reuses this file), or run deploy/saas/update.sh.
# The image tag is NOT pinned here: install.sh/update.sh pass it via
# --set-string image.tag=vX.Y.Z (recorded in ${STATE_FILE}). A direct
# 'helm upgrade' MUST do the same — without it the chart falls back to its
# appVersion, which does not track releases.

appSecret: "$(yesc "$APP_SECRET")"

discordBotToken: "$(yesc "$GX_DISCORD_BOT_TOKEN")"
# Platform provider keys (ADR-0054): empty = pure BYOK.
elevenLabsApiKey: "$(yesc "$GX_ELEVENLABS_API_KEY")"
geminiApiKey: "$(yesc "$GX_GEMINI_API_KEY")"
groqApiKey: "$(yesc "$GX_GROQ_API_KEY")"

ollamaUrl: "$(yesc "$GX_OLLAMA_URL")"

database:
  password: "$(yesc "$DB_PASSWORD")"

postgres:
  persistence:
    size: ${GX_PG_DISK}

seed:
  enabled: false             # no demo data on a real deployment

voice:
  enabled: false             # the web pod drives the voice loop in 'all' mode

web:
  enabled: true
  mode: all
  oauth:
    clientId: "$(yesc "$GX_DISCORD_CLIENT_ID")"
    clientSecret: "$(yesc "$GX_DISCORD_CLIENT_SECRET")"
    # redirectUrl stays empty: it is DERIVED from ingress.host so it can
    # never drift (docs/deploy/k3s-proxmox.md §5).
  operatorIds: "$(yesc "$GX_OPERATOR_IDS")"${ADMISSION_BLOCK}
  resources:                 # 'all' mode runs the voice loop in-process
    requests:
      cpu: 500m
      memory: 512Mi
    limits:
      cpu: "2"
      memory: 1Gi
${PLANS_BLOCK}
ingress:
  enabled: true
  host: "$(yesc "$GX_HOST")"
  className: traefik
  certManager:
    enabled: ${CERTMANAGER_ENABLED}
    clusterIssuer: letsencrypt-prod
EOF
fi

BACKUP_CRONJOB=""
if [ -n "$GX_BACKUP_DIR" ]; then
  BACKUP_CRONJOB="${FULLNAME}-pgdump"
  # The chart's app Secret is <release>-db (glyphoxa.secretName); its
  # database-url key is the same DSN every workload uses (ADR-0031).
  umask 077
  cat >"$BACKUP_MANIFEST" <<EOF
# Generated by deploy/saas/install.sh — nightly logical pg_dump to a path on
# the NODE's disk (single-box deployment). Restore:
#   pg_restore -d "\$DSN" --clean --if-exists <file>.dump
# Copy dumps off the box regularly — a backup on the same disk is not a
# backup (docs/deploy/k3s-proxmox.md §8).
apiVersion: batch/v1
kind: CronJob
metadata:
  name: ${BACKUP_CRONJOB}
  namespace: ${GX_NAMESPACE}
spec:
  schedule: "${GX_BACKUP_SCHEDULE}"
  concurrencyPolicy: Forbid
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 3
  jobTemplate:
    spec:
      backoffLimit: 2
      template:
        spec:
          restartPolicy: OnFailure
          containers:
            - name: pgdump
              image: pgvector/pgvector:pg17
              command: ["/bin/sh", "-c"]
              # umask + chmod: dumps carry the DB, so neither the directory
              # nor the files may be world-readable regardless of who created
              # the hostPath (DirectoryOrCreate makes it 0755).
              args:
                - umask 077 && chmod 700 /backup
                  && pg_dump "\$GLYPHOXA_DATABASE_URL" -Fc
                  -f "/backup/glyphoxa-\$(date +%F-%H%M%S).dump"
                  && find /backup -name 'glyphoxa-*.dump'
                  -mtime +${GX_BACKUP_RETENTION_DAYS} -delete
              env:
                - name: GLYPHOXA_DATABASE_URL
                  valueFrom:
                    secretKeyRef:
                      name: ${FULLNAME}-db
                      key: database-url
              volumeMounts:
                - name: backup
                  mountPath: /backup
          volumes:
            - name: backup
              hostPath:
                path: ${GX_BACKUP_DIR}
                type: DirectoryOrCreate
EOF
fi

# State the companion update script (and a re-run of this one) reads.
# KUBECONFIG is recorded so update runs against the same cluster the install
# targeted. Parsed line-wise (KEY=raw value, no quoting) — never `source`d.
# On the REAL path this is written only after the install converged, so a
# failed install never leaves state claiming a version that never deployed
# (--dry-run writes it immediately: the files ARE its output).
write_state() {
  local kubeconfig_record="${KUBECONFIG-}"
  [ -z "$SKIP_K3S" ] && kubeconfig_record="/etc/rancher/k3s/k3s.yaml"
  umask 077
  cat >"$STATE_FILE" <<EOF
# Generated by deploy/saas/install.sh — read by deploy/saas/update.sh.
# KEY=raw-value per line; parsed line-wise, never source'd.
GX_REPO=${GX_REPO}
GX_RELEASE=${GX_RELEASE}
GX_FULLNAME=${FULLNAME}
GX_NAMESPACE=${GX_NAMESPACE}
GX_VERSION=${GX_VERSION}
GX_HOST=${GX_HOST}
GX_TLS=${GX_TLS}
GX_ACME_EMAIL=${GX_ACME_EMAIL}
GX_VALUES_FILE=${VALUES_FILE}
GX_BACKUP_DIR=${GX_BACKUP_DIR}
GX_BACKUP_CRONJOB=${BACKUP_CRONJOB}
GX_BACKUP_SCHEDULE=${GX_BACKUP_SCHEDULE}
GX_BACKUP_RETENTION_DAYS=${GX_BACKUP_RETENTION_DAYS}
GX_KUBECONFIG=${kubeconfig_record}
GX_TIMEOUT=${GX_TIMEOUT}
EOF
}

if [ -n "$DRY_RUN" ]; then
  write_state
  log "DRY RUN — plan"
  note "would install: k3s $([ -n "$SKIP_K3S" ] && echo '(skipped — current context)' || echo '(if missing)'), helm (if missing)"
  [ "$GX_TLS" = "letsencrypt" ] && note "would install: cert-manager + ClusterIssuer letsencrypt-prod (${GX_ACME_SERVER})"
  note "would deploy: Glyphoxa ${GX_VERSION} as release '${GX_RELEASE}' in namespace '${GX_NAMESPACE}', host ${GX_HOST}"
  [ -n "$GX_BACKUP_DIR" ] && note "would apply: backup CronJob '${BACKUP_CRONJOB}' -> ${GX_BACKUP_DIR} (schedule '${GX_BACKUP_SCHEDULE}', keep ${GX_BACKUP_RETENTION_DAYS}d)"
  note "wrote: ${VALUES_FILE}, ${STATE_FILE}$([ -n "$GX_BACKUP_DIR" ] && echo ", ${BACKUP_MANIFEST}")"
  exit 0
fi

# ------------------------------------------------------------------- k3s --
if [ -n "$SKIP_K3S" ]; then
  log "Using the current kubeconfig context (--skip-k3s)"
  need_cmd kubectl
  note "context: $(kubectl config current-context 2>/dev/null || echo '<none>')"
  if [ -t 0 ]; then
    read -r -p "Deploy to this context? [y/N] " yn
    case "$yn" in [yY]*) : ;; *) die "aborted" ;; esac
  fi
else
  log "k3s"
  if command -v k3s >/dev/null 2>&1 && systemctl is-active --quiet k3s 2>/dev/null; then
    note "k3s already installed and running — skipping install"
  else
    note "installing k3s (https://get.k3s.io)"
    curl -sfL https://get.k3s.io | sh -
  fi
  export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
  export PATH="/usr/local/bin:$PATH"   # k3s symlinks kubectl here
  need_cmd kubectl
  # The k3s unit reports ready before the kubelet registers the Node object,
  # and `kubectl wait` errors immediately on zero matching resources — so wait
  # for the node to EXIST before waiting for Ready.
  note "waiting for the node to register"
  for _ in $(seq 1 60); do
    kubectl get nodes --no-headers 2>/dev/null | grep -q . && break
    sleep 2
  done
  kubectl wait --for=condition=Ready node --all --timeout=180s
fi

# ------------------------------------------------------------------- helm --
if ! command -v helm >/dev/null 2>&1; then
  log "Installing helm"
  curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
fi
need_cmd helm

# ------------------------------------------------------------------ chart --
log "Fetching the ${GX_VERSION} chart"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT
curl -fsSL "https://github.com/${GX_REPO}/archive/refs/tags/${GX_VERSION}.tar.gz" \
  | tar -xz -C "$WORKDIR"
CHART_DIR="$(echo "$WORKDIR"/*/deploy/charts/glyphoxa)"
[ -d "$CHART_DIR" ] || die "release ${GX_VERSION} has no deploy/charts/glyphoxa — too old for a chart install?"

# ----------------------------------------------------------- cert-manager --
if [ "$GX_TLS" = "letsencrypt" ]; then
  log "cert-manager + Let's Encrypt ClusterIssuer"
  # Three cases: (a) a release WE manage exists (even failed/half-installed
  # after an aborted --wait) — converge it with upgrade --install; (b) no
  # release but the CRDs exist — cert-manager is managed by someone else,
  # leave it alone; (c) neither — first install.
  if helm status cert-manager -n cert-manager >/dev/null 2>&1; then
    note "converging the cert-manager release (repairs a half-install; no-op when healthy)"
    helm repo add jetstack https://charts.jetstack.io --force-update >/dev/null
    helm upgrade --install cert-manager jetstack/cert-manager \
      --namespace cert-manager --create-namespace \
      --set crds.enabled=true --wait --timeout "$GX_TIMEOUT"
  elif kubectl get crd clusterissuers.cert-manager.io >/dev/null 2>&1; then
    note "cert-manager CRDs present but not helm-managed by this script — leaving it alone"
  else
    helm repo add jetstack https://charts.jetstack.io --force-update >/dev/null
    helm upgrade --install cert-manager jetstack/cert-manager \
      --namespace cert-manager --create-namespace \
      --set crds.enabled=true --wait --timeout "$GX_TIMEOUT"
  fi
  kubectl apply -f - <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    server: ${GX_ACME_SERVER}
    email: "${GX_ACME_EMAIL}"
    privateKeySecretRef:
      name: letsencrypt-prod-account-key
    solvers:
      - http01:
          ingress:
            class: traefik
EOF
  # Best-effort DNS sanity: HTTP-01 issuance can only succeed once GX_HOST
  # resolves to this box. A mismatch is a warning, not a failure — DNS may
  # still be propagating, and the Certificate retries on its own.
  PUB_IP="$(curl -fsS -4 --max-time 5 https://ifconfig.me 2>/dev/null || true)"
  DNS_IP="$(getent ahostsv4 "$GX_HOST" 2>/dev/null | awk '{print $1; exit}' || true)"
  if [ -n "$PUB_IP" ] && [ "$DNS_IP" != "$PUB_IP" ]; then
    note "WARNING: ${GX_HOST} resolves to '${DNS_IP:-<nothing>}' but this box's public IP looks like ${PUB_IP}."
    note "Point an A record at ${PUB_IP}; the certificate will issue once it propagates."
  fi
fi

# ---------------------------------------------------------------- install --
# Freshly generated secrets against a cluster that already holds release data
# would rotate appSecret (stranding every sealed BYOK credential, ADR-0004)
# AND mismatch the Postgres password persisted on its PVC. The values file is
# the only key to that data — refuse to overwrite what we cannot decrypt.
if [ -z "$REUSE_VALUES" ] \
  && kubectl -n "$GX_NAMESPACE" get secret "${FULLNAME}-db" >/dev/null 2>&1; then
  die "the values file was (re)generated with FRESH secrets, but namespace '${GX_NAMESPACE}' \
already holds release data (Secret ${FULLNAME}-db). Installing would strand the sealed BYOK \
credentials (ADR-0004) and mismatch the Postgres password on its PVC. Restore the previous \
values.yaml into ${GX_CONFIG_DIR}, or for a TRUE fresh start remove the old release first: \
helm -n ${GX_NAMESPACE} uninstall ${GX_RELEASE} && kubectl -n ${GX_NAMESPACE} delete pvc,secret,job --all"
fi

log "Installing Glyphoxa ${GX_VERSION} (release '${GX_RELEASE}', namespace '${GX_NAMESPACE}')"
kubectl get namespace "$GX_NAMESPACE" >/dev/null 2>&1 \
  || kubectl create namespace "$GX_NAMESPACE"
helm upgrade --install "$GX_RELEASE" "$CHART_DIR" \
  --namespace "$GX_NAMESPACE" \
  --values "$VALUES_FILE" \
  --set-string "image.tag=${GX_VERSION}" \
  --wait --timeout "$GX_TIMEOUT"

log "Verifying"
kubectl -n "$GX_NAMESPACE" rollout status "deployment/${FULLNAME}-web" --timeout "$GX_TIMEOUT"

# ----------------------------------------------------------------- backup --
if [ -n "$GX_BACKUP_DIR" ]; then
  log "Backup CronJob (${BACKUP_CRONJOB} -> ${GX_BACKUP_DIR}, '${GX_BACKUP_SCHEDULE}' UTC, keep ${GX_BACKUP_RETENTION_DAYS}d)"
  [ -z "$SKIP_K3S" ] && { mkdir -p "$GX_BACKUP_DIR"; chmod 700 "$GX_BACKUP_DIR"; }
  kubectl apply -f "$BACKUP_MANIFEST"
  note "first dump runs on schedule; run one now with:"
  note "  kubectl -n ${GX_NAMESPACE} create job --from=cronjob/${BACKUP_CRONJOB} ${BACKUP_CRONJOB}-manual-\$(date +%s)"
  note "copy dumps OFF this box regularly (rsync/restic) — same-disk backups don't count."
fi

# ---------------------------------------------------------------- summary --
write_state
SCHEME=https; [ "$GX_TLS" = "none" ] && SCHEME=http
log "Done — Glyphoxa ${GX_VERSION} is up"
note "console:        ${SCHEME}://${GX_HOST}/"
note "OAuth redirect: register ${SCHEME}://${GX_HOST}/auth/discord/callback on the Discord application (exactly)"
[ "$GX_TLS" = "letsencrypt" ] && note "certificate:    kubectl -n ${GX_NAMESPACE} get certificate   (READY True once DNS points here)"
note "values:         ${VALUES_FILE} (0600 — edit + re-run install.sh; a direct helm upgrade needs --set-string image.tag=${GX_VERSION})"
note "state:          ${STATE_FILE}"
note "update later:   deploy/saas/update.sh   (docs/deploy/cloud-providers.md)"
note "going paid:     docs/deploy/saas-operations.md (plans, platform keys, cost report)"
