package voicebench

import "fmt"

// SLO is one latency budget: the p50/p95 ceilings (milliseconds) for a stage.
// The numbers are LOCKED by the Sprint-2 plan's SLO table (one source of truth
// shared by the A2 Prometheus alerts and this benchmark's C2 assertion).
type SLO struct {
	Stage Stage
	P50ms float64
	P95ms float64
}

// EngineeringSLO is the headline budget the C2 job asserts against: the
// speech-end→first-audio span (StageResponseLatency), ≤1.2s p50 / ≤2.5s p95
// (the "Engineering" row of the plan's SLO table — the boundary prod dashboards
// and alerts use). The "User-perceived" row (≤1.7s/≤3.0s, true end-of-speech →
// audible in Discord) is the same span plus the VAD hangover and Discord tail;
// the benchmark measures the engineering span directly, so it asserts this one.
var EngineeringSLO = SLO{
	Stage: StageResponseLatency,
	P50ms: 1200,
	P95ms: 2500,
}

// Violation describes one breached budget for the report.
type Violation struct {
	Stage    Stage
	Quantile string  // "p50" or "p95"
	Got      float64 // observed ms
	Budget   float64 // ceiling ms
}

func (v Violation) String() string {
	return fmt.Sprintf("%s %s = %.0fms > %.0fms budget", v.Stage, v.Quantile, v.Got, v.Budget)
}

// Check returns the budgets the report breaches, or nil if it meets every
// asserted SLO. A stage with no samples (N==0) is skipped, not failed — a tier
// that legitimately doesn't exercise a stage must not fail the gate (e.g. the
// PCM cassette tier doesn't drive the opus codec). C2 turns a non-empty result
// into a failing assertion; the default cassette tier flags *our* orchestration
// regressions, the live tier feeds vendor SLOs.
//
// NOTE: until A3 (#4) lands the first-audio hook, StageResponseLatency has no
// samples on any tier, so Check is a no-op for the headline SLO — it starts
// asserting the moment the hook feeds real spans. That is the intended seam, not
// a silent pass.
func (r Report) Check(slos ...SLO) []Violation {
	if len(slos) == 0 {
		slos = []SLO{EngineeringSLO}
	}
	var out []Violation
	for _, s := range slos {
		d, ok := r.Stages[s.Stage]
		if !ok || d.N == 0 {
			continue
		}
		if s.P50ms > 0 && d.P50 > s.P50ms {
			out = append(out, Violation{s.Stage, "p50", d.P50, s.P50ms})
		}
		if s.P95ms > 0 && d.P95 > s.P95ms {
			out = append(out, Violation{s.Stage, "p95", d.P95, s.P95ms})
		}
	}
	return out
}
