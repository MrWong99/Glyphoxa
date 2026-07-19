#!/usr/bin/env bash
# deploy/saas/update.sh — update a deploy/saas/install.sh deployment to the
# latest released Glyphoxa version (or a pinned one) and migrate the schema.
#
# What it does, in order:
#   1. loads the install state (/etc/glyphoxa/install.env, or --config-dir)
#      and targets the same cluster the install did;
#   2. resolves the latest published release (or --version vX.Y.Z);
#   3. refuses same-version re-runs and DOWNGRADES — rolling an open-admission
#      deployment back across the ADR-0055 boundary boots in allowlist posture
#      and evicts every signup (saas-operations.md's rollback caveat); --force
#      overrides both refusals once you have read that caveat;
#   4. when a backup CronJob is configured, takes a pre-upgrade pg_dump and
#      waits for it — an upgrade without a fresh dump is a bet, not an update;
#   5. downloads the target release's source tarball and `helm upgrade`s with
#      ITS chart and its image tag — chart and image move in lockstep, and the
#      chart's pre-upgrade migrate hook brings the schema current BEFORE the
#      new pod rolls (ADR-0031/0034; `all` mode uses a Recreate strategy, so
#      expect a brief outage — schedule around live Voice Sessions);
#   6. verifies the rollout and records the new version in the state file.
#
# --dry-run resolves + prints the plan without touching the cluster.
set -euo pipefail

GX_CONFIG_DIR="${GX_CONFIG_DIR:-/etc/glyphoxa}"
GX_TARGET_VERSION="${GX_TARGET_VERSION:-}"   # empty = latest published release
FORCE=""
SKIP_BACKUP=""
DRY_RUN=""

usage() {
  cat <<'EOF'
usage: update.sh [options]

Options:
  --version vX.Y.Z   update to this released version (default: latest release)
  --config-dir DIR   where install.sh put values/state (default: /etc/glyphoxa)
  --skip-backup      skip the pre-upgrade pg_dump (NOT recommended)
  --force            proceed on same-version re-runs and downgrades. A
                     downgrade across the ADR-0055 boundary boots an open
                     deployment in allowlist posture and evicts every signup —
                     read docs/deploy/saas-operations.md first.
  --dry-run          resolve versions + print the plan, touch nothing
  -h, --help         this text
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --version) GX_TARGET_VERSION="${2:?--version needs a tag}"; shift 2 ;;
    --config-dir) GX_CONFIG_DIR="${2:?--config-dir needs a path}"; shift 2 ;;
    --skip-backup) SKIP_BACKUP=1; shift ;;
    --force) FORCE=1; shift ;;
    --dry-run) DRY_RUN=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "update.sh: unknown option '$1' (see --help)" >&2; exit 2 ;;
  esac
done

log()  { printf '\n=== %s ===\n' "$*"; }
note() { printf '    %s\n' "$*"; }
die()  { echo "update.sh: ERROR: $*" >&2; exit 1; }
need_cmd() { command -v "$1" >/dev/null 2>&1 || die "required command '$1' not found"; }

need_cmd curl
need_cmd tar

# ------------------------------------------------------------------- state --
STATE_FILE="${GX_CONFIG_DIR}/install.env"
[ -f "$STATE_FILE" ] \
  || die "no install state at ${STATE_FILE} — run deploy/saas/install.sh first (or pass --config-dir)"

# Parsed line-wise (KEY=raw value), NOT sourced — values like a cron schedule
# contain spaces/globs that `source` would mangle. install.sh writes the file.
while IFS='=' read -r k v; do
  case "$k" in
    GX_*) printf -v "$k" '%s' "$v" ;;
  esac
done <"$STATE_FILE"

for req in GX_RELEASE GX_NAMESPACE GX_VERSION GX_VALUES_FILE; do
  [ -n "${!req-}" ] || die "${STATE_FILE} is missing ${req} — re-run deploy/saas/install.sh"
done
GX_FULLNAME="${GX_FULLNAME:-$GX_RELEASE}"
GX_REPO="${GX_REPO:-MrWong99/Glyphoxa}"
GX_TIMEOUT="${GX_TIMEOUT:-600s}"
[ -f "$GX_VALUES_FILE" ] || die "values file ${GX_VALUES_FILE} (from state) does not exist"

# Target the cluster the install recorded, unless the caller already exported
# a KUBECONFIG of their own.
if [ -z "${KUBECONFIG-}" ] && [ -n "${GX_KUBECONFIG-}" ] && [ -f "$GX_KUBECONFIG" ]; then
  export KUBECONFIG="$GX_KUBECONFIG"
fi
export PATH="/usr/local/bin:$PATH"   # k3s symlinks kubectl here

# ---------------------------------------------------------------- versions --
log "Resolving versions"

# Deployed version: what the web Deployment actually runs beats the state
# record (someone may have helm-upgraded by hand); fall back to the state.
CURRENT="$GX_VERSION"
if [ -z "$DRY_RUN" ]; then
  need_cmd kubectl
  live="$(kubectl -n "$GX_NAMESPACE" get "deployment/${GX_FULLNAME}-web" \
      -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || true)"
  case "$live" in
    *:*) CURRENT="${live##*:}" ;;
  esac
fi

TARGET="$GX_TARGET_VERSION"
if [ -z "$TARGET" ]; then
  # `|| true` so a curl failure reaches the actionable die below instead of
  # errexit killing the script with only curl's terse stderr.
  TARGET="$(curl -fsSL -H 'Accept: application/vnd.github+json' \
      "https://api.github.com/repos/${GX_REPO}/releases/latest" \
    | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1 || true)"
  [ -n "$TARGET" ] || die "could not resolve the latest release of ${GX_REPO} (network? rate limit?) — pin one with --version"
fi

note "deployed: ${CURRENT}"
note "target:   ${TARGET}"

if [ "$TARGET" = "$CURRENT" ] && [ -z "$FORCE" ]; then
  log "Already up to date (${CURRENT}) — nothing to do"
  exit 0
fi

# Refuse when the target is the LOWER version. Plain `sort -V` alone is wrong
# for prerelease tags (GNU version sort puts v1.3.0 BEFORE v1.3.0-rc1 — the
# inverse of semver, so an rc→GA update would be refused and a GA→rc downgrade
# waved through); mapping the semver '-' to dpkg-style '~' (which sorts before
# the bare version) restores semver order for the v1.2.3[-suffix] tags this
# repo cuts (.goreleaser.yml: prerelease auto). Downgrades are not a symmetric
# operation here: an open-admission deployment rolled back across the ADR-0055
# boundary boots in allowlist posture and evicts every signup (see
# saas-operations.md, rollback caveat).
LOWER="$(printf '%s\n%s\n' "$CURRENT" "$TARGET" | sed 's/-/~/' | sort -V | head -n1 | tr '~' '-')"
if [ "$TARGET" != "$CURRENT" ] \
  && [ "$LOWER" = "$TARGET" ] \
  && [ -z "$FORCE" ]; then
  die "refusing to DOWNGRADE ${CURRENT} -> ${TARGET}: across the ADR-0055 boundary this \
boots an open deployment in allowlist posture and evicts every signup \
(docs/deploy/saas-operations.md). --force if you have read that and mean it."
fi

if [ -n "$DRY_RUN" ]; then
  log "DRY RUN — plan"
  note "would update ${CURRENT} -> ${TARGET} (release '${GX_RELEASE}', namespace '${GX_NAMESPACE}')"
  if [ -n "${GX_BACKUP_CRONJOB-}" ] && [ -z "$SKIP_BACKUP" ]; then
    note "would take a pre-upgrade pg_dump via CronJob '${GX_BACKUP_CRONJOB}'"
  else
    note "would take NO pre-upgrade backup ($([ -n "$SKIP_BACKUP" ] && echo '--skip-backup' || echo 'none configured'))"
  fi
  note "would helm upgrade with the ${TARGET} chart + image (migrate hook runs first)"
  exit 0
fi

need_cmd helm
need_cmd kubectl

# ------------------------------------------------------------------ backup --
if [ -n "${GX_BACKUP_CRONJOB-}" ] && [ -z "$SKIP_BACKUP" ]; then
  log "Pre-upgrade backup (CronJob ${GX_BACKUP_CRONJOB})"
  if cj_err="$(kubectl -n "$GX_NAMESPACE" get "cronjob/${GX_BACKUP_CRONJOB}" -o name 2>&1)"; then
    JOB="${GX_BACKUP_CRONJOB}-pre-$(date +%Y%m%d%H%M%S)"
    kubectl -n "$GX_NAMESPACE" create job --from="cronjob/${GX_BACKUP_CRONJOB}" "$JOB"
    # A failed dump ABORTS the update: upgrading without the backup you
    # configured is exactly the surprise this step exists to prevent. Poll
    # both terminal conditions so a deterministic pg_dump failure surfaces in
    # seconds instead of eating the whole timeout.
    t="${GX_TIMEOUT%s}"; case "$t" in *[!0-9]*|"") t=600 ;; esac
    deadline=$((SECONDS + t))
    while :; do
      cond="$(kubectl -n "$GX_NAMESPACE" get "job/${JOB}" \
        -o jsonpath='{range .status.conditions[?(@.status=="True")]}{.type}{end}' 2>/dev/null || true)"
      case "$cond" in
        *Complete*) break ;;
        *Failed*) die "pre-upgrade backup Job ${JOB} FAILED — fix the backup (kubectl -n ${GX_NAMESPACE} logs job/${JOB}), or --skip-backup at your own risk" ;;
      esac
      [ "$SECONDS" -lt "$deadline" ] \
        || die "pre-upgrade backup Job ${JOB} did not complete within ${GX_TIMEOUT} — fix the backup (or --skip-backup at your own risk)"
      sleep 5
    done
    note "dump written under ${GX_BACKUP_DIR:-<backup dir>} on the node"
  elif grep -qi 'notfound' <<<"$cj_err"; then
    # The state promises a backup that is not there — that is a broken backup,
    # not a missing feature: refuse rather than silently update without one.
    die "state names backup CronJob '${GX_BACKUP_CRONJOB}' but namespace '${GX_NAMESPACE}' has none — \
restore it (kubectl apply -f ${GX_CONFIG_DIR}/backup-cronjob.yaml) or pass --skip-backup to update without a backup"
  else
    die "cannot check the backup CronJob: ${cj_err}"
  fi
elif [ -n "$SKIP_BACKUP" ]; then
  note "skipping the pre-upgrade backup (--skip-backup)"
else
  note "WARNING: no backup configured (install.sh's backup option was declined) — updating without one"
fi

# ------------------------------------------------------------------- chart --
log "Fetching the ${TARGET} chart"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT
curl -fsSL "https://github.com/${GX_REPO}/archive/refs/tags/${TARGET}.tar.gz" \
  | tar -xz -C "$WORKDIR"
CHART_DIR="$(echo "$WORKDIR"/*/deploy/charts/glyphoxa)"
[ -d "$CHART_DIR" ] || die "release ${TARGET} has no deploy/charts/glyphoxa"

# ----------------------------------------------------------------- upgrade --
log "Upgrading ${CURRENT} -> ${TARGET}"
if ! helm upgrade "$GX_RELEASE" "$CHART_DIR" \
  --namespace "$GX_NAMESPACE" \
  --values "$GX_VALUES_FILE" \
  --set-string "image.tag=${TARGET}" \
  --wait --timeout "$GX_TIMEOUT"; then
  log "UPGRADE FAILED — diagnostics"
  kubectl -n "$GX_NAMESPACE" get pods -o wide 2>/dev/null || true
  kubectl -n "$GX_NAMESPACE" logs "job/${GX_FULLNAME}-migrate" --tail=40 2>/dev/null || true
  note "roll back with:  helm -n ${GX_NAMESPACE} rollback ${GX_RELEASE}"
  note "                 (then restore the pre-upgrade dump if the migrate hook already ran:"
  note "                  pg_restore -d \"\$DSN\" --clean --if-exists <dump>)"
  note "NOTE the ADR-0055 rollback caveat before rolling an open deployment back."
  die "helm upgrade to ${TARGET} failed"
fi

kubectl -n "$GX_NAMESPACE" rollout status "deployment/${GX_FULLNAME}-web" --timeout "$GX_TIMEOUT"

# ------------------------------------------------------------------- state --
# Record the deployed version (line-rewrite, keeping the rest of the file).
tmp="$(mktemp)"
sed "s/^GX_VERSION=.*/GX_VERSION=${TARGET}/" "$STATE_FILE" >"$tmp"
chmod 600 "$tmp"
mv "$tmp" "$STATE_FILE"

log "Done — ${GX_RELEASE} is on ${TARGET}"
note "deployed image: $(kubectl -n "$GX_NAMESPACE" get "deployment/${GX_FULLNAME}-web" -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || echo '<unknown>')"
note "state updated:  ${STATE_FILE}"
