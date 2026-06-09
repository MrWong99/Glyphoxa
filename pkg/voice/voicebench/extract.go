package voicebench

import (
	"time"
)

// Accumulator collects per-turn stage spans across replays and reduces them to a
// [Report]. The driver calls [Accumulator.AddTurn] once per replayed turn, then
// Build to produce the JSON artifact. Splitting collection from reduction keeps
// the driving loop (clip → Conversation → tap) independent of the percentile
// math.
//
// All spans now come through the single [recorderTap]: the agenttool adapter
// records llm_round/provider_*, and observe's [observe.StageSubscriber]
// (installed on the bus by the rig) records the bus-derived headline stages
// (response_latency / address_detect / tts_ttfb) onto the SAME tap. The bench no
// longer hand-derives any span from the raw event log — reusing observe's
// per-TurnID subscriber is the can't-drift guarantee (a bench number is the
// exact value prod's Prometheus subscriber would observe).
type Accumulator struct {
	tier    string
	corpus  []string
	byStage map[Stage][]time.Duration
	turns   int
}

// NewAccumulator starts a collector tagged with the tier ("cassette"/"live") and
// the corpus tiers that fed the run (for the report header).
func NewAccumulator(tier string, corpus []string) *Accumulator {
	return &Accumulator{tier: tier, corpus: corpus, byStage: map[Stage][]time.Duration{}}
}

// AddTurns folds a clip's tap-captured spans into the accumulator by draining
// tap, and advances the turn count by turns (a clip may segment into more than
// one turn). The orchestrator must be quiescent for this clip when called — the
// driver calls it after Flush AND after the barrier has confirmed every eligible
// turn's headline sample landed on the tap, so the drain captures all of them.
// turns is the real turn count from the driver's barrier so the report's N
// matches the sample count, not the clip count. tap may be nil (then the clip
// contributes no spans).
func (a *Accumulator) AddTurns(tap *recorderTap, turns int) {
	a.turns += turns
	if tap == nil {
		return
	}
	for stage, samples := range tap.drain() {
		a.byStage[stage] = append(a.byStage[stage], samples...)
	}
}

// AddTurn folds a single turn's tap-captured spans (the rig-test convenience for
// a one-utterance clip). It is [Accumulator.AddTurns] with turns=1.
func (a *Accumulator) AddTurn(tap *recorderTap) {
	a.AddTurns(tap, 1)
}

// Build reduces every collected stage to its distribution and returns the
// report. Stages with no samples are simply absent from the map (Check skips
// them), so a tier that doesn't exercise a stage produces a clean report rather
// than a false zero.
func (a *Accumulator) Build() Report {
	stages := make(map[Stage]Distribution, len(a.byStage))
	for stage, samples := range a.byStage {
		stages[stage] = Summarize(samples)
	}
	return Report{Tier: a.tier, Corpus: a.corpus, N: a.turns, Stages: stages}
}
