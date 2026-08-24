#!/usr/bin/env bash
# Microbenchmark allocation gate (issue #611).
#
# usage: scripts/bench-gate.sh <baseline.txt> <new.txt>
#
# Both arguments are RAW `go test -bench . -benchmem` output. Only allocs/op is
# load-bearing: it is deterministic, so an increase is a real regression and
# fails this script. ns/op is left to benchstat's warn-only report (shared CI
# runners are far too noisy to gate on wall time).
set -euo pipefail

if [ $# -ne 2 ]; then
  echo "usage: $0 <baseline.txt> <new.txt>" >&2
  exit 2
fi

baseline=$1
current=$2

if [ ! -f "$baseline" ]; then
  echo "FAIL: baseline '$baseline' not found — the committed baseline must exist (see bench/README.md)." >&2
  exit 1
fi

if [ ! -f "$current" ]; then
  echo "FAIL: benchmark output '$current' not found." >&2
  exit 1
fi

# A file with no `Benchmark…` result lines would compare "clean" against
# anything, i.e. turn the gate into a green no-op — the failure mode #140
# taught this repo to pin explicitly. Both sides must carry results.
if ! grep -q '^Benchmark' "$baseline"; then
  echo "FAIL: baseline '$baseline' holds no benchmark results — regenerate it (see bench/README.md)." >&2
  exit 1
fi
if ! grep -q '^Benchmark' "$current"; then
  echo "FAIL: benchmark output '$current' holds no benchmark results — the run produced nothing to compare." >&2
  exit 1
fi

# Both files hold one line per (benchmark, -count repetition). The GOMAXPROCS
# suffix (`-8`) differs between the machine that produced the baseline and the
# CI runner, so names are normalised by stripping it; repetitions are collapsed
# to their WORST (max) allocs/op, which is what a gate must judge.
report=$(awk '
  function key(n) { sub(/-[0-9]+$/, "", n); return n }
  # First pass = baseline, second = current. FNR==NR (not a FILENAME compare)
  # so the two arguments may legitimately be the SAME path — which is how the
  # self-test proves the committed baseline parses at all.
  /^Benchmark/ {
    allocs = ""
    for (i = 2; i <= NF; i++) if ($i == "allocs/op") allocs = $(i - 1)
    if (allocs == "") next
    name = key($1)
    a = allocs + 0
    if (FNR == NR) {
      if (!(name in base) || a > base[name]) base[name] = a
    } else {
      if (!(name in cur)) { order[++n] = name; cur[name] = a }
      else if (a > cur[name]) cur[name] = a
    }
  }
  END {
    for (i = 1; i <= n; i++) {
      name = order[i]
      if (!(name in base)) {
        print "NEW: " name " has no baseline entry (allocs/op=" cur[name] ") — add it on the next baseline regeneration"
        continue
      }
      if (cur[name] > base[name]) {
        print "REGRESSION: " name " allocs/op " base[name] " -> " cur[name]
        failed = 1
      } else if (cur[name] < base[name]) {
        print "IMPROVED: " name " allocs/op " base[name] " -> " cur[name] " — regenerate bench/baseline.txt"
      } else {
        print "ok: " name " allocs/op " cur[name]
      }
    }
    exit failed ? 1 : 0
  }
' "$baseline" "$current") && status=0 || status=$?

echo "$report"

# Under GitHub Actions the same report is the job's step summary, so a reviewer
# reads the verdict on the PR's Checks tab without opening the raw log.
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    echo "### Microbenchmark allocs/op gate"
    echo
    echo '```'
    echo "$report"
    echo '```'
  } >>"$GITHUB_STEP_SUMMARY"
fi

if [ "$status" -ne 0 ]; then
  echo "FAIL: allocs/op regressed — see the REGRESSION lines above. allocs/op is deterministic, so this is a real allocation regression, not runner noise." >&2
  exit 1
fi

exit 0
