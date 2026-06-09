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

// CheckSLO is the LIVE-tier gate: it returns the absolute budgets the report
// breaches, or nil if it meets every asserted SLO. This belongs to the live tier
// ONLY — cassette replay is instant, so cassette-tier spans are ~0 and an
// absolute SLO would pass trivially and MASK a 10x orchestration regression. Use
// [Report.CheckRegression] for the cassette tier.
//
// A stage with no samples (N==0) is skipped, not failed — a tier that
// legitimately doesn't exercise a stage must not fail the gate (e.g. a clip that
// drove no codec). Until a run actually feeds StageResponseLatency (needs the A3
// first-audio hook + a real reply), this is a no-op for the headline SLO — the
// intended seam, not a silent pass.
func (r Report) CheckSLO(slos ...SLO) []Violation {
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

// DefaultRegressionTolerance is the per-stage p95 slack the cassette tier allows
// over the committed baseline before flagging a regression (25%). Cassette spans
// are tiny and a little jittery, so a too-tight threshold would flap; 25% still
// catches the kind of multiplicative orchestration regression the tier exists to
// guard (an accidental O(n²), a dropped streaming path).
const DefaultRegressionTolerance = 0.25

// regressionNoiseFloorMs is the absolute p95 below which a stage is exempt from
// the relative gate. On instant cassette replay the orchestration spans are
// sub-millisecond, where a ±25% band is pure OS-scheduler jitter (the p95
// observed swing run-to-run is itself >2x), so a relative gate on them flaps
// without catching anything real. A stage only gates once its baseline p95
// clears this floor — i.e. once it's large enough that a 25% growth is a genuine
// multiplicative regression, not noise. The floor is two orders of magnitude
// below the 1200ms headline SLO, so nothing the SLO cares about is exempted: a
// real orchestration regression pushes a span well past 1ms before it matters.
const regressionNoiseFloorMs = 1.0

// CheckRegression is the CASSETTE-tier gate: it compares this report's per-stage
// p95 against a committed baseline and flags any stage whose p95 BOTH cleared the
// noise floor in the baseline AND grew by more than tol (fraction, e.g. 0.25 =
// +25%). It does NOT assert an absolute SLO — on instant cassette replay the
// numbers are orchestration-only and meaningless as an absolute budget; the point
// is "did OUR code get relatively slower." A stage absent from either side, or
// whose baseline p95 is below regressionNoiseFloorMs, is skipped (a newly-added
// stage isn't a regression; a sub-ms span can't be gated relatively without
// flapping on jitter). tol<=0 uses [DefaultRegressionTolerance].
func (r Report) CheckRegression(baseline Report, tol float64) []Violation {
	if tol <= 0 {
		tol = DefaultRegressionTolerance
	}
	var out []Violation
	for stage, cur := range r.Stages {
		base, ok := baseline.Stages[stage]
		if !ok || base.P95 < regressionNoiseFloorMs || cur.N == 0 || base.N == 0 {
			continue
		}
		ceiling := base.P95 * (1 + tol)
		if cur.P95 > ceiling {
			out = append(out, Violation{stage, "p95", cur.P95, ceiling})
		}
	}
	return out
}
