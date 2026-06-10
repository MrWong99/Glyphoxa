package voicebench

import (
	"sync"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

// recorderTap is the [observe.StageRecorder] the bench injects as the
// orchestrator's recorder. It captures the spans the agenttool provider adapter
// records onto the injected recorder — llm_round (the B2 signal) and the
// provider call/error tallies — into per-stage sample lists. It is the bench's
// tap on the REAL prod emit path: the adapter records through this exact
// interface, so a captured number is definitionally the same value the
// Prometheus adapter would observe (bench == series), with no re-derivation.
//
// It ALSO captures the bus-owned stages (response_latency / address_detect /
// tts_ttfb) — but only because the bench installs observe's own
// [observe.StageSubscriber] onto the bus pointing at THIS tap (rig.go). That
// subscriber is the single bus emitter (the same one prod runs), so there is no
// double-count: the tap no longer derives those stages itself. See the
// StageRecorder methods below for the full ownership split.
//
// Safe for concurrent use: the orchestrator records from multiple goroutines.
type recorderTap struct {
	mu      sync.Mutex
	byStage map[Stage][]time.Duration
	// provider call/error tallies, for the provider-health report section.
	calls  map[observe.Outcome]int
	errors int
}

// newRecorderTap returns an empty tap ready to be passed as the orchestrator's
// StageRecorder for one benchmark run.
func newRecorderTap() *recorderTap {
	return &recorderTap{
		byStage: map[Stage][]time.Duration{},
		calls:   map[observe.Outcome]int{},
	}
}

func (t *recorderTap) add(stage Stage, d time.Duration) {
	t.mu.Lock()
	t.byStage[stage] = append(t.byStage[stage], d)
	t.mu.Unlock()
}

// samples returns a snapshot copy of the captured durations for stage.
func (t *recorderTap) samples(stage Stage) []time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]time.Duration, len(t.byStage[stage]))
	copy(out, t.byStage[stage])
	return out
}

// drain returns all captured stage samples and resets the tap, so the next
// turn's recorder spans start clean. The Driver calls it once per clip to
// attribute each turn's recorder-emitted stages to that turn. The orchestrator
// must be quiescent for this clip when drain is called (the Driver calls it
// after Flush, when no stage goroutine is still recording).
func (t *recorderTap) drain() map[Stage][]time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.byStage
	t.byStage = map[Stage][]time.Duration{}
	return out
}

// observe.StageRecorder implementation.
//
// OWNERSHIP (reconciled with observe, the metrics reviewer): the tap is the SOLE
// recorder for the whole run. Two emit paths record onto it, with no overlap:
//   - the agenttool provider adapter: `LLMRound`, `ProviderCall`, `ProviderError`
//     (per `Provider.Complete`);
//   - observe's `StageSubscriber` (installed on the bus by the rig): the
//     bus-derived spans `ResponseLatency` / `AddressDetect` / `TTSTimeToFirstByte`.
//
// Because the subscriber is the single bus emitter — the exact one prod runs —
// the captured numbers are definitionally the Prometheus series (bench == series)
// with no re-derivation and no double-count.
//
// vad_hangover / stt_request / tts_total / codec_* / llm_turn are interface-
// present but UNWIRED (no caller anywhere yet — carry-over #11); they would land
// here if/when their emit site records onto the injected recorder, but the bench
// must not assert non-zero on them until then.
func (t *recorderTap) LLMRound(_ observe.Provider, _ int, _ bool, d time.Duration) {
	t.add(StageLLMRound, d)
}
func (t *recorderTap) ProviderCall(_ observe.Stage, _ observe.Provider, outcome observe.Outcome) {
	t.mu.Lock()
	t.calls[outcome]++
	t.mu.Unlock()
}
func (t *recorderTap) ProviderError(_ observe.Stage, _ observe.Provider) {
	t.mu.Lock()
	t.errors++
	t.mu.Unlock()
}

// Bus-derived spans, recorded by observe's StageSubscriber onto this tap.
func (t *recorderTap) ResponseLatency(_ observe.AgentRole, d time.Duration) {
	t.add(StageResponseLatency, d)
}
func (t *recorderTap) AddressDetect(d time.Duration) {
	t.add(StageAddressDetect, d)
}
func (t *recorderTap) TTSTimeToFirstByte(_ observe.Provider, d time.Duration) {
	t.add(StageTTSTTFB, d)
}

// Unwired (carry-over #11): no caller records these yet. No-ops so the tap
// satisfies the interface; the bench must not assert non-zero on them.
func (t *recorderTap) VADHangover(time.Duration)                  {}
func (t *recorderTap) CodecDecode(time.Duration)                  {}
func (t *recorderTap) CodecEncode(time.Duration)                  {}
func (t *recorderTap) STTRequest(observe.Provider, time.Duration) {}
func (t *recorderTap) TTSTotal(observe.Provider, time.Duration)   {}
func (t *recorderTap) LLMTurn(observe.Provider, time.Duration)    {}

// TurnOutcome is the turn-lifecycle counter; the bench measures latency on
// surviving turns, not the outcome mix, so it is a no-op here.
func (t *recorderTap) TurnOutcome(observe.TurnOutcome, observe.TurnReason) {}

// compile-time assertion: the tap satisfies the recorder the orchestrator drives.
var _ observe.StageRecorder = (*recorderTap)(nil)
