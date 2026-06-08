package voicebench_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voicebench"
)

// TestSummarize_Empty pins the no-samples seam: a stage that never fired in a
// tier reduces to the zero Distribution (N=0), not a panic — the bench must
// report a missing stage cleanly (e.g. codec on the PCM cassette path).
func TestSummarize_Empty(t *testing.T) {
	got := voicebench.Summarize(nil)
	if got.N != 0 || got.P50 != 0 || got.P95 != 0 {
		t.Errorf("Summarize(nil) = %+v, want zero Distribution", got)
	}
}

// TestSummarize_Distribution pins the percentile reduction and the ms
// conversion. With 1..100 ms samples the nearest-rank index p*(n-1) gives
// deterministic quantiles, and the tail (p95/p99/max) is reported separately
// from the mean — the tail is the SLO's concern, not the average.
func TestSummarize_Distribution(t *testing.T) {
	samples := make([]time.Duration, 100)
	for i := range samples {
		samples[i] = time.Duration(i+1) * time.Millisecond // 1ms..100ms
	}
	d := voicebench.Summarize(samples)
	if d.N != 100 {
		t.Errorf("N = %d, want 100", d.N)
	}
	// index = p*(n-1) = p*99: p50→idx49→50ms, p95→idx94→95ms, p99→idx98→99ms.
	for _, tc := range []struct {
		name string
		got  float64
		want float64
	}{
		{"p50", d.P50, 50}, {"p95", d.P95, 95}, {"p99", d.P99, 99},
		{"max", d.Max, 100}, {"mean", d.Mean, 50.5},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v ms, want %v ms", tc.name, tc.got, tc.want)
		}
	}
}

// TestSummarize_Unsorted confirms the reducer sorts its input — callers feed the
// raw per-replay sample slice in arrival order, not sorted.
func TestSummarize_Unsorted(t *testing.T) {
	d := voicebench.Summarize([]time.Duration{
		30 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond,
	})
	if d.P50 != 20 || d.Max != 30 {
		t.Errorf("unsorted reduce = p50 %v / max %v, want 20 / 30", d.P50, d.Max)
	}
}

// TestReport_Check_Passes pins that a report under the locked engineering budget
// (≤1.2s p50 / ≤2.5s p95 on response_latency) reports no violations.
func TestReport_Check_Passes(t *testing.T) {
	r := voicebench.Report{Stages: map[voicebench.Stage]voicebench.Distribution{
		voicebench.StageResponseLatency: {N: 30, P50: 900, P95: 2100},
	}}
	if v := r.CheckSLO(); len(v) != 0 {
		t.Errorf("Check under budget returned violations: %v", v)
	}
}

// TestReport_Check_FlagsBreach pins both quantile breaches independently against
// the locked numbers: a p95 over 2500ms must surface even when p50 is fine.
func TestReport_Check_FlagsBreach(t *testing.T) {
	r := voicebench.Report{Stages: map[voicebench.Stage]voicebench.Distribution{
		voicebench.StageResponseLatency: {N: 30, P50: 1100, P95: 3200},
	}}
	v := r.CheckSLO()
	if len(v) != 1 || v[0].Quantile != "p95" {
		t.Fatalf("Check = %v, want exactly one p95 violation", v)
	}
	if v[0].Got != 3200 || v[0].Budget != 2500 {
		t.Errorf("violation = %+v, want got 3200 budget 2500", v[0])
	}
}

// TestReport_Check_SkipsMissingStage pins the seam that keeps the headline SLO a
// no-op until A3 lands the first-audio hook: a stage with no samples (N=0) is
// skipped, not failed — otherwise every pre-#4 run would falsely fail the gate.
func TestReport_Check_SkipsMissingStage(t *testing.T) {
	r := voicebench.Report{Stages: map[voicebench.Stage]voicebench.Distribution{
		voicebench.StageResponseLatency: {N: 0},
	}}
	if v := r.CheckSLO(); len(v) != 0 {
		t.Errorf("Check over an unmeasured stage returned %v, want none", v)
	}
	// Also: a stage entirely absent from the map is skipped.
	if v := (voicebench.Report{Stages: map[voicebench.Stage]voicebench.Distribution{}}).CheckSLO(); len(v) != 0 {
		t.Errorf("Check over an empty report returned %v, want none", v)
	}
}

// TestReport_JSON pins the artifact shape C2 reads: ms-suffixed quantile fields
// and a stage map keyed by the locked stage names, round-tripping cleanly.
func TestReport_JSON(t *testing.T) {
	r := voicebench.Report{
		Tier:   "cassette",
		Corpus: []string{"trivial", "dice"},
		N:      30,
		Stages: map[voicebench.Stage]voicebench.Distribution{
			voicebench.StageLLMRound: {N: 30, P50: 420, P95: 980, P99: 1500, Max: 1600, Mean: 500},
		},
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back voicebench.Report
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	d := back.Stages[voicebench.StageLLMRound]
	if back.Tier != "cassette" || d.P95 != 980 || d.N != 30 {
		t.Errorf("round-trip = %s / %+v, want cassette tier + llm_round p95 980", back.Tier, d)
	}
}

// TestReport_CheckRegression pins the cassette-tier gate: a stage whose p95 grew
// past the tolerance over the committed baseline flags; one within tolerance
// passes; a stage absent from the baseline is not a regression. This is the
// guard the cassette tier uses INSTEAD of the absolute SLO (which would pass
// trivially on instant-replay numbers).
func TestReport_CheckRegression(t *testing.T) {
	base := voicebench.Report{Stages: map[voicebench.Stage]voicebench.Distribution{
		voicebench.StageResponseLatency: {N: 30, P95: 8}, // 8ms orchestration baseline
		voicebench.StageAddressDetect:   {N: 30, P95: 2},
	}}

	// +50% on response_latency (8 → 12) breaches the 25% tolerance; address_detect
	// flat passes.
	regressed := voicebench.Report{Stages: map[voicebench.Stage]voicebench.Distribution{
		voicebench.StageResponseLatency: {N: 30, P95: 12},
		voicebench.StageAddressDetect:   {N: 30, P95: 2},
		voicebench.StageLLMRound:        {N: 30, P95: 999}, // absent from baseline → not a regression
	}}
	v := regressed.CheckRegression(base, voicebench.DefaultRegressionTolerance)
	if len(v) != 1 || v[0].Stage != voicebench.StageResponseLatency {
		t.Fatalf("CheckRegression = %v, want exactly one response_latency breach", v)
	}

	// Within tolerance (8 → 9, +12.5%) passes.
	ok := voicebench.Report{Stages: map[voicebench.Stage]voicebench.Distribution{
		voicebench.StageResponseLatency: {N: 30, P95: 9},
	}}
	if v := ok.CheckRegression(base, 0); len(v) != 0 { // tol<=0 → default 25%
		t.Errorf("CheckRegression within tolerance flagged: %v", v)
	}
}
