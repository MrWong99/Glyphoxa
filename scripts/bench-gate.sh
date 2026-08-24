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
      if (!(name in base)) { border[++nb] = name; base[name] = a }
      else if (a > base[name]) base[name] = a
    } else {
      if (!(name in cur)) { order[++n] = name; cur[name] = a }
      else if (a > cur[name]) cur[name] = a
    }
  }
  END {
    # A run that parsed no allocs/op at all (e.g. -benchmem dropped from the
    # command) must not read as "no regressions" — the whole gate would be a
    # no-op with every line still looking like a benchmark result.
    if (n == 0) {
      print "FAIL: no allocs/op figures parsed from the benchmark run — was -benchmem dropped?"
      exit 1
    }
    # Walk the BASELINE first: a benchmark that ran last time and is missing
    # now (panicked, was deleted, or the run was truncated by a failure) is a
    # hard failure. Iterating only the run would let it disappear silently.
    for (i = 1; i <= nb; i++) {
      name = border[i]
      if (!(name in cur)) {
        print "MISSING: " name " is in the baseline but produced no result in this run (panicked, deleted, or truncated run)"
        failed = 1
      }
    }
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
  echo "FAIL: see the REGRESSION / MISSING lines above. allocs/op is deterministic, so an increase is a real allocation regression, not runner noise; a missing benchmark means the run did not measure what the baseline covers." >&2
  exit 1
fi

exit 0
