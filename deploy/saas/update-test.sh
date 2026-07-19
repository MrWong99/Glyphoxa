#!/usr/bin/env bash
# Self-test for deploy/saas/update.sh — offline, no cluster: exercises the
# version-decision logic through --dry-run against a synthetic install state,
# pinning the target with GX_TARGET_VERSION so no network is touched. Each
# case pins one property (the scripts/ self-test discipline).
set -euo pipefail

cd "$(dirname "$0")/../.."

UPDATE=deploy/saas/update.sh

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

pass() { echo "update-test: PASS — $*"; }
fail() { echo "update-test: FAIL — $*" >&2; exit 1; }

# make_state DIR VERSION [extra lines...] — a minimal plausible install state.
make_state() {
  local dir="$1" version="$2"; shift 2
  mkdir -p "$dir"
  : >"$dir/values.yaml"
  {
    echo "GX_RELEASE=glyphoxa"
    echo "GX_FULLNAME=glyphoxa"
    echo "GX_NAMESPACE=glyphoxa"
    echo "GX_VERSION=${version}"
    echo "GX_VALUES_FILE=${dir}/values.yaml"
    printf '%s\n' "$@"
  } >"$dir/install.env"
}

run_update() { # DIR [args...]
  local dir="$1"; shift
  env -i PATH="$PATH" HOME="$workdir" bash "$UPDATE" --config-dir "$dir" "$@" </dev/null
}

# --- 1. no install state must fail, pointing at install.sh -----------------
if out="$(run_update "$workdir/nowhere" --dry-run 2>&1)"; then
  fail "update without install state succeeded"
fi
grep -q 'install.sh' <<<"$out" || fail "the no-state error does not point at install.sh: $out"
pass "missing install state refuses and points at install.sh"

# --- 2. same version is a no-op ---------------------------------------------
d="$workdir/uptodate"; make_state "$d" v1.2.3
out="$(run_update "$d" --dry-run --version v1.2.3 2>&1)" \
  || fail "same-version dry-run exited non-zero: $out"
grep -qi 'up to date' <<<"$out" || fail "same-version run does not say 'up to date': $out"
pass "same-version target is a clean no-op"

# --- 3. downgrades are refused, citing the ADR-0055 caveat ------------------
d="$workdir/downgrade"; make_state "$d" v1.2.3
if out="$(run_update "$d" --dry-run --version v1.1.0 2>&1)"; then
  fail "downgrade v1.2.3 -> v1.1.0 was accepted"
fi
grep -q 'ADR-0055' <<<"$out" || fail "the downgrade refusal does not cite ADR-0055: $out"
pass "downgrade is refused with the ADR-0055 caveat"

# --- 4. --force overrides the downgrade refusal -----------------------------
out="$(run_update "$d" --dry-run --version v1.1.0 --force 2>&1)" \
  || fail "forced downgrade dry-run failed: $out"
grep -q 'v1.2.3 -> v1.1.0' <<<"$out" || fail "forced downgrade plan does not show the version change: $out"
pass "--force allows the downgrade and the plan names both versions"

# --- 5. an upgrade prints a plan naming both versions and the backup posture -
d="$workdir/upgrade"; make_state "$d" v1.2.3 "GX_BACKUP_CRONJOB=glyphoxa-pgdump"
out="$(run_update "$d" --dry-run --version v1.3.0 2>&1)" \
  || fail "upgrade dry-run failed: $out"
grep -q 'v1.2.3 -> v1.3.0' <<<"$out" || fail "plan does not show the version change: $out"
grep -q 'glyphoxa-pgdump' <<<"$out" || fail "plan does not mention the pre-upgrade backup CronJob: $out"
out="$(run_update "$d" --dry-run --version v1.3.0 --skip-backup 2>&1)" \
  || fail "upgrade --skip-backup dry-run failed: $out"
grep -q 'NO pre-upgrade backup' <<<"$out" || fail "--skip-backup plan does not admit skipping the backup: $out"
pass "upgrade plan names both versions and the backup posture"

# --- 6. a pre-release-style tag sorts correctly (no false downgrade) --------
d="$workdir/prerelease"; make_state "$d" v1.2.3
out="$(run_update "$d" --dry-run --version v1.2.10 2>&1)" \
  || fail "v1.2.3 -> v1.2.10 was treated as a downgrade (string compare instead of version sort): $out"
pass "version comparison is numeric (v1.2.10 > v1.2.3)"

# --- 7. semver prerelease ordering: rc -> GA is an UPGRADE, GA -> rc is not --
# GNU sort -V alone gets this backwards (v1.3.0 < v1.3.0-rc1); the guard maps
# '-' to '~' to restore semver order (.goreleaser.yml cuts prerelease tags).
d="$workdir/rc-to-ga"; make_state "$d" v1.3.0-rc1
out="$(run_update "$d" --dry-run --version v1.3.0 2>&1)" \
  || fail "the routine rc -> GA update (v1.3.0-rc1 -> v1.3.0) was refused as a downgrade: $out"
grep -q 'v1.3.0-rc1 -> v1.3.0' <<<"$out" || fail "rc -> GA plan does not show the version change: $out"
d="$workdir/ga-to-rc"; make_state "$d" v1.3.0
if out="$(run_update "$d" --dry-run --version v1.3.0-rc1 2>&1)"; then
  fail "the GA -> rc DOWNGRADE (v1.3.0 -> v1.3.0-rc1) was waved through"
fi
grep -q 'ADR-0055' <<<"$out" || fail "the GA -> rc refusal does not cite ADR-0055: $out"
pass "prerelease ordering is semver-correct in both directions"

echo "update-test: OK"
