#!/usr/bin/env bash
# Self-test for deploy/saas/install.sh — offline, no cluster, no root: runs the
# script's --dry-run path (which gathers parameters and writes values/state/
# backup files but touches nothing else) and asserts the properties the real
# install depends on. Same discipline as the scripts/ self-tests
# (helm-validate-test.sh &c): each case pins one property, so a lost check
# re-opens exactly one hole.
#
# Needs bash + openssl (like the script itself) and helm for the render cases
# (present wherever the chart gates run).
set -euo pipefail

cd "$(dirname "$0")/../.."

INSTALL=deploy/saas/install.sh
CHART=deploy/charts/glyphoxa

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

pass() { echo "install-test: PASS — $*"; }
fail() { echo "install-test: FAIL — $*" >&2; exit 1; }

# base_env emits the full non-interactive parameter set; callers override
# single vars per case. Dummy-but-shaped values, never deployed.
base_env() {
  cat <<EOF
GX_CONFIG_DIR=$1
GX_VERSION=v9.9.9-selftest
GX_HOST=glyphoxa.selftest.example
GX_TLS=letsencrypt
GX_ACME_EMAIL=selftest@example.com
GX_DISCORD_CLIENT_ID=000000000000000001
GX_DISCORD_CLIENT_SECRET=selftest-not-a-real-secret
GX_DISCORD_BOT_TOKEN=selftest-not-a-real-token
GX_OPERATOR_IDS=111111111111111111
GX_ADMISSION_MODE=allowlist
GX_OLLAMA_URL=
GX_GROQ_API_KEY=
GX_ELEVENLABS_API_KEY=
GX_GEMINI_API_KEY=
GX_BACKUP_DIR=
EOF
}

# run_dry CONFIG_DIR [VAR=VAL ...] — dry-run the installer non-interactively
# (stdin </dev/null, so prompts are impossible) with the base env + overrides.
run_dry() {
  local dir="$1"; shift
  # The unquoted expansion IS the point: base_env emits one KEY=value token
  # per line (no spaces in base values), overrides arrive quoted via "$@".
  # shellcheck disable=SC2046
  env -i PATH="$PATH" HOME="$workdir" $(base_env "$dir" | xargs) "$@" \
    bash "$INSTALL" --dry-run </dev/null
}

# --- 1. a run without a TTY and a missing required var must fail, naming it --
d="$workdir/missing-required"; mkdir -p "$d"
if out="$(env -i PATH="$PATH" HOME="$workdir" GX_CONFIG_DIR="$d" GX_VERSION=v9.9.9-selftest \
    bash "$INSTALL" --dry-run </dev/null 2>&1)"; then
  fail "dry-run with no parameters succeeded; it must refuse without GX_HOST"
fi
grep -q 'GX_HOST' <<<"$out" || fail "the missing-parameter error does not name GX_HOST: $out"
pass "non-interactive run without GX_HOST refuses and names the variable"

# --- 2. dry-run writes values/state with tight permissions and sane content --
d="$workdir/happy"; mkdir -p "$d"
run_dry "$d" >/dev/null 2>&1 || fail "happy-path dry-run failed"
[ -f "$d/values.yaml" ] || fail "no values.yaml written"
[ -f "$d/install.env" ] || fail "no install.env written"
perms="$(stat -c '%a' "$d/values.yaml")"
[ "$perms" = "600" ] || fail "values.yaml permissions are ${perms}, want 600"
grep -q 'host: "glyphoxa.selftest.example"' "$d/values.yaml" || fail "values.yaml lacks the ingress host"
grep -q 'operatorIds: "111111111111111111"' "$d/values.yaml" || fail "operatorIds not a quoted string (snowflake precision, values.yaml docs)"
grep -q '^GX_VERSION=v9.9.9-selftest$' "$d/install.env" || fail "install.env lacks the pinned version"
# The generated DB password must be generated, not the chart's static default.
grep -q 'password: "glyphoxa"' "$d/values.yaml" && fail "values.yaml carries the chart's default DB password"
# appSecret must decode to exactly 32 bytes (ADR-0004).
secret="$(sed -n 's/^appSecret: "\(.*\)"$/\1/p' "$d/values.yaml")"
[ "$(printf '%s' "$secret" | base64 -d 2>/dev/null | wc -c)" = "32" ] \
  || fail "generated appSecret is not base64 of 32 bytes"
pass "dry-run writes 0600 values.yaml + install.env with generated secrets"

# --- 3. the generated values must actually render against the local chart ---
if command -v helm >/dev/null 2>&1; then
  helm template gx-selftest "$CHART" --values "$d/values.yaml" \
    --set-string image.tag=v9.9.9-selftest >"$workdir/render.yaml" \
    || fail "the chart does not render with the generated values"
  grep -q 'kind: Deployment' "$workdir/render.yaml" || fail "render has no Deployment"
  grep -q 'kind: Ingress' "$workdir/render.yaml" || fail "render has no Ingress (ingress.enabled expected)"
  pass "generated values render against the local chart (allowlist mode)"

  # open-admission variant: plans catalog + signup slug must render too
  d2="$workdir/open"; mkdir -p "$d2"
  run_dry "$d2" GX_ADMISSION_MODE=open GX_SIGNUP_PLAN_SLUG=byok-free GX_OPERATOR_IDS= \
    >/dev/null 2>&1 || fail "open-admission dry-run failed"
  helm template gx-selftest "$CHART" --values "$d2/values.yaml" \
    --set-string image.tag=v9.9.9-selftest >"$workdir/render-open.yaml" \
    || fail "the chart does not render with the open-admission values"
  grep -q 'byok-free' "$workdir/render-open.yaml" || fail "open-admission render lacks the signup plan slug"
  pass "open-admission values (plans catalog + signup slug) render against the local chart"
else
  echo "install-test: SKIP — helm not found; render cases not run" >&2
fi

# --- 4. a re-run must NOT rotate appSecret (sealed creds die with it) -------
before="$(grep '^appSecret:' "$d/values.yaml")"
run_dry "$d" >/dev/null 2>&1 || fail "re-run dry-run failed"
after="$(grep '^appSecret:' "$d/values.yaml")"
[ "$before" = "$after" ] || fail "re-run regenerated appSecret — sealed BYOK credentials would be undecryptable (ADR-0004)"
pass "re-run reuses the existing values file (appSecret stable)"

# --- 5. the backup option must emit a correct CronJob manifest --------------
# A NON-default schedule, so the round-trip assertion below cannot pass by
# falling back to the script's default.
d3="$workdir/backup"; mkdir -p "$d3"
run_dry "$d3" GX_BACKUP_DIR=/var/lib/glyphoxa-backups \
  GX_BACKUP_SCHEDULE='30 2 * * 1' GX_BACKUP_RETENTION_DAYS=21 \
  >/dev/null 2>&1 || fail "backup dry-run failed"
m="$d3/backup-cronjob.yaml"
[ -f "$m" ] || fail "no backup-cronjob.yaml written"
grep -q 'schedule: "30 2 \* \* 1"' "$m" || fail "manifest lacks the schedule"
grep -q 'path: /var/lib/glyphoxa-backups' "$m" || fail "manifest lacks the hostPath"
# The chart's app Secret is <fullname>-db (glyphoxa.secretName) — the doc's
# old example used the bare release name and dangled.
grep -q 'name: glyphoxa-db' "$m" || fail "manifest does not reference the chart Secret glyphoxa-db"
grep -q 'mtime +21' "$m" || fail "manifest lacks the retention sweep"
if command -v kubeconform >/dev/null 2>&1; then
  kubeconform -strict "$m" || fail "kubeconform rejects the backup CronJob"
fi
# The custom schedule must survive a state-file round-trip (spaces + globs):
# a re-run WITHOUT the env override must regenerate the same manifest, not
# fall back to the default or mangle the globs.
grep -q '^GX_BACKUP_SCHEDULE=30 2 \* \* 1$' "$d3/install.env" || fail "install.env mangles the cron schedule"
run_dry "$d3" GX_BACKUP_DIR=/var/lib/glyphoxa-backups >/dev/null 2>&1 \
  || fail "backup re-run dry-run failed"
grep -q 'schedule: "30 2 \* \* 1"' "$m" || fail "re-run lost the custom schedule (state prefill must beat the default)"
pass "backup CronJob manifest is correct and the schedule round-trips the state file"

# --- 6. quoting: a secret with YAML-hostile characters must still render ----
if command -v helm >/dev/null 2>&1; then
  d4="$workdir/quoting"; mkdir -p "$d4"
  # Literal $ and backtick are the case under test — no expansion wanted.
  # shellcheck disable=SC2016
  run_dry "$d4" 'GX_DISCORD_CLIENT_SECRET=we"ird\pass$word`x' >/dev/null 2>&1 \
    || fail "dry-run with a YAML-hostile secret failed"
  helm template gx-selftest "$CHART" --values "$d4/values.yaml" \
    --set-string image.tag=v9.9.9-selftest >/dev/null \
    || fail "the chart does not render when a secret contains quotes/backslashes/dollars"
  pass "YAML-hostile characters in secrets survive values generation"
fi

# --- 7. invalid parameter values must be refused ----------------------------
d5="$workdir/invalid"; mkdir -p "$d5"
run_dry "$d5" GX_ADMISSION_MODE=wide-open >/dev/null 2>&1 \
  && fail "GX_ADMISSION_MODE=wide-open was accepted"
run_dry "$d5" GX_OPERATOR_IDS=not-a-snowflake >/dev/null 2>&1 \
  && fail "non-numeric GX_OPERATOR_IDS was accepted"
run_dry "$d5" GX_BACKUP_DIR=relative/path >/dev/null 2>&1 \
  && fail "relative GX_BACKUP_DIR was accepted"
run_dry "$d5" GX_ACME_EMAIL=not-an-email >/dev/null 2>&1 \
  && fail "implausible GX_ACME_EMAIL was accepted"
run_dry "$d5" GX_BACKUP_DIR=/var/lib/x GX_BACKUP_SCHEDULE='not a schedule' >/dev/null 2>&1 \
  && fail "non-cron GX_BACKUP_SCHEDULE was accepted"
pass "invalid admission mode / operator ids / backup dir / email / schedule are refused"

# --- 8. an explicitly EMPTY env var clears a recorded choice on re-run ------
# d3's state records a backup dir; base_env passes GX_BACKUP_DIR= (set but
# empty), which must win over the state (set-but-empty counts as set).
run_dry "$d3" >/dev/null 2>&1 || fail "explicit-empty backup re-run failed"
grep -q '^GX_BACKUP_CRONJOB=$' "$d3/install.env" \
  || fail "GX_BACKUP_DIR= did not clear the recorded backup (state prefill beat an explicit empty env var)"
pass "an explicitly empty env var clears the recorded backup choice"

# --- 9. reuse-run host handling: env conflict refused, hand-edit adopted ----
# d holds the happy-path install (host glyphoxa.selftest.example). An env
# override that contradicts the reused values file must be refused…
if run_dry "$d" GX_HOST=other.selftest.example >/dev/null 2>&1; then
  fail "a GX_HOST env override conflicting with the reused values file was silently accepted"
fi
# …while a hand-EDITED values file (no env override) is adopted as authority.
sed -i 's/^  host: "glyphoxa.selftest.example"$/  host: "edited.selftest.example"/' "$d/values.yaml"
# shellcheck disable=SC2046
env -i PATH="$PATH" HOME="$workdir" $(base_env "$d" | grep -v '^GX_HOST=' | xargs) \
  bash "$INSTALL" --dry-run </dev/null >/dev/null 2>&1 \
  || fail "reuse-run with a hand-edited values host failed"
grep -q '^GX_HOST=edited.selftest.example$' "$d/install.env" \
  || fail "hand-edited values host was not adopted into the state"
pass "reuse-run refuses a conflicting GX_HOST env override and adopts a hand-edited values host"

echo "install-test: OK"
