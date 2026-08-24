#!/usr/bin/env bash
# Self-test for scripts/bench-gate.sh (issue #611): the microbenchmark gate must
# actually gate. Same discipline as helm-validate-test.sh — every case pins one
# property whose loss turns the CI job into a green no-op.
set -euo pipefail

cd "$(dirname "$0")/.."

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Every case runs the gate with an EMPTY GITHUB_STEP_SUMMARY: under CI this
# script inherits the job's real summary file, and the fixtures below
# deliberately produce REGRESSION reports — appending those to the job summary
# would fake failures a reader has no way to tell from real ones. Case 10 opts
# back in with a temp file of its own.
gate() { GITHUB_STEP_SUMMARY= scripts/bench-gate.sh "$@"; }

cat >"$tmp/base.txt" <<'EOF'
BenchmarkNormalize-8	 1600000	       700.1 ns/op	      64 B/op	       1 allocs/op
EOF

echo "bench-gate-test: [1/12] gate passes when allocs/op are unchanged"
cp "$tmp/base.txt" "$tmp/new.txt"
gate "$tmp/base.txt" "$tmp/new.txt"

echo "bench-gate-test: [2/12] gate fails on an allocs/op increase, naming the benchmark and both values"
cat >"$tmp/regressed.txt" <<'EOF'
BenchmarkNormalize-8	 1600000	       700.1 ns/op	      64 B/op	       3 allocs/op
EOF
if out=$(gate "$tmp/base.txt" "$tmp/regressed.txt" 2>&1); then
  echo "bench-gate-test: FAIL — gate exited 0 although allocs/op went 1 -> 3" >&2
  exit 1
fi
case "$out" in
*"REGRESSION: BenchmarkNormalize allocs/op 1 -> 3"*) ;;
*)
  echo "bench-gate-test: FAIL — regression report does not read 'REGRESSION: BenchmarkNormalize allocs/op 1 -> 3': $out" >&2
  exit 1
  ;;
esac

echo "bench-gate-test: [3/12] GOMAXPROCS suffix and -count repetitions do not confuse the comparison"
cat >"$tmp/counted.txt" <<'EOF'
BenchmarkNormalize-2	 1600000	       690.4 ns/op	      64 B/op	       1 allocs/op
BenchmarkNormalize-2	 1600000	       701.9 ns/op	      64 B/op	       1 allocs/op
BenchmarkNormalize-2	 1600000	       712.2 ns/op	      64 B/op	       1 allocs/op
EOF
gate "$tmp/base.txt" "$tmp/counted.txt"

echo "bench-gate-test: [4/12] the WORST repetition decides (one bad run out of many still fails)"
cat >"$tmp/flaky.txt" <<'EOF'
BenchmarkNormalize-2	 1600000	       690.4 ns/op	      64 B/op	       1 allocs/op
BenchmarkNormalize-2	 1600000	       701.9 ns/op	     128 B/op	       2 allocs/op
EOF
if gate "$tmp/base.txt" "$tmp/flaky.txt" >/dev/null 2>&1; then
  echo "bench-gate-test: FAIL — gate exited 0 although one repetition allocated more" >&2
  exit 1
fi

echo "bench-gate-test: [5/12] a benchmark absent from the baseline warns but does not fail"
cat >"$tmp/added.txt" <<'EOF'
BenchmarkNormalize-8	 1600000	       700.1 ns/op	      64 B/op	       1 allocs/op
BenchmarkBrandNew-8	  300000	      4200 ns/op	    1024 B/op	      17 allocs/op
EOF
out=$(gate "$tmp/base.txt" "$tmp/added.txt")
case "$out" in
*NEW*BenchmarkBrandNew*) ;;
*)
  echo "bench-gate-test: FAIL — a baseline-less benchmark was not reported as NEW: $out" >&2
  exit 1
  ;;
esac

echo "bench-gate-test: [6/12] gate fails when the baseline file is missing"
if gate "$tmp/absent.txt" "$tmp/new.txt" >/dev/null 2>&1; then
  echo "bench-gate-test: FAIL — gate exited 0 although the committed baseline does not exist" >&2
  exit 1
fi

echo "bench-gate-test: [7/12] a baseline with no benchmark results fails (empty file is not 'no regressions')"
: >"$tmp/empty.txt"
if gate "$tmp/empty.txt" "$tmp/new.txt" >/dev/null 2>&1; then
  echo "bench-gate-test: FAIL — gate exited 0 although the baseline held no benchmark results" >&2
  exit 1
fi

echo "bench-gate-test: [8/12] a benchmark run that produced no results fails (an empty run is not a pass)"
if gate "$tmp/base.txt" "$tmp/empty.txt" >/dev/null 2>&1; then
  echo "bench-gate-test: FAIL — gate exited 0 although the benchmark run produced no results" >&2
  exit 1
fi

echo "bench-gate-test: [9/12] the real committed baseline compares clean against itself"
out=$(gate bench/baseline.txt bench/baseline.txt)
case "$out" in
*"ok: BenchmarkEncodeVector768"*) ;;
*)
  echo "bench-gate-test: FAIL — comparing the committed baseline with itself parsed nothing: $out" >&2
  exit 1
  ;;
esac

echo "bench-gate-test: [10/12] the report is appended to the GitHub step summary when one is configured"
GITHUB_STEP_SUMMARY="$tmp/summary.md" scripts/bench-gate.sh "$tmp/base.txt" "$tmp/new.txt" >/dev/null
case "$(cat "$tmp/summary.md")" in
*"BenchmarkNormalize"*) ;;
*)
  echo "bench-gate-test: FAIL — nothing about the comparison reached the step summary" >&2
  exit 1
  ;;
esac

echo "bench-gate-test: [11/12] a baseline benchmark missing from the run fails (a panicking bench must not vanish silently)"
cat >"$tmp/base2.txt" <<'EOF'
BenchmarkNormalize-8	 1600000	       700.1 ns/op	      64 B/op	       1 allocs/op
BenchmarkEncodeVector768-8	   50000	     24000 ns/op	   23040 B/op	       2 allocs/op
EOF
if out=$(gate "$tmp/base2.txt" "$tmp/base.txt" 2>&1); then
  echo "bench-gate-test: FAIL — gate exited 0 although BenchmarkEncodeVector768 never ran" >&2
  exit 1
fi
case "$out" in
*"BenchmarkEncodeVector768"*) ;;
*)
  echo "bench-gate-test: FAIL — the disappeared benchmark was not named: $out" >&2
  exit 1
  ;;
esac

echo "bench-gate-test: [12/12] a run without allocs/op columns fails (dropping -benchmem must not silence the gate)"
cat >"$tmp/no-benchmem.txt" <<'EOF'
BenchmarkNormalize-8	 1600000	       700.1 ns/op
EOF
if gate "$tmp/base.txt" "$tmp/no-benchmem.txt" >/dev/null 2>&1; then
  echo "bench-gate-test: FAIL — gate exited 0 although the run carried no allocs/op figures" >&2
  exit 1
fi

echo "bench-gate-test: OK"
