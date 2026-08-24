# Microbenchmark baseline

`baseline.txt` is raw `go test -bench` output, committed so the `bench` CI job
(`.github/workflows/ci.yml`, issue #611) has something to compare each PR
against.

## What is load-bearing

**Only `allocs/op`.** It is deterministic, so an increase is a real allocation
regression and fails the job (`scripts/bench-gate.sh`, self-tested by
`scripts/bench-gate-test.sh`).

`ns/op` and `B/op` in this file are context for humans, **not** a gate: GitHub's
shared runners are far too noisy for wall time, and the machine that generated
this file is not the machine CI runs on. The job renders a `benchstat` ns/op
comparison into the step summary as a **warning-only** report.

Consequently the absolute times here mean nothing across machines, and a
regenerated baseline with different times but identical `allocs/op` is a no-op
change — don't churn the file for timing drift alone.

## Regenerating

Run from a clean checkout of `main`, on an otherwise idle machine, with the same
Go toolchain as `go.mod`:

```sh
go test -bench . -benchmem -count 10 -run '^$' \
  ./internal/tape/ ./internal/textnorm/ ./internal/storage/ ./pkg/voice/wire/codec/dsp/ \
  > bench/baseline.txt
```

That is byte-for-byte the command the CI job runs. Regenerate when:

- a benchmark is **added** — until then the job reports it as `NEW: … no
  baseline entry` (a warning, never a failure);
- `allocs/op` **improves** — the job reports `IMPROVED: …` and asks for a
  regeneration so the lower number becomes the new ceiling;
- an `allocs/op` increase is **deliberate and reviewed** — regenerate in the
  same PR, and say why in the PR body;
- a benchmark is **removed** — until then the job hard-fails with `MISSING: …`,
  which is deliberate: a benchmark that silently stops running (panic, deleted,
  or a truncated run) is indistinguishable from one that never regressed.

The committed baseline was generated on a 32-core Linux workstation
(`goarch: amd64`, Go 1.27.0). The machine class is recorded only so the times
can be read sanely; the gate itself is machine-independent, and the GOMAXPROCS
suffix in benchmark names (`-32` vs the runner's `-4`) is normalised away by
`scripts/bench-gate.sh`.
