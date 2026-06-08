package voicebench

import (
	"sync"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

// recorderTap is an [observe.StageRecorder] that captures every stage duration
// the pipeline emits into per-stage sample lists. It is the bench's tap on the
// REAL prod emit path: the orchestrator records through this exact interface, so
// a bench number captured here is definitionally the same value the Prometheus
// adapter would observe (bench == series), with no re-derivation to drift.
//
// It is the source of truth for the recorder-only stages — llm_round (the B2
// signal), vad_hangover, stt_request, tts_ttfb/total, codec_* — which are NOT
// bus events and so cannot come from [voicetest.Harness.Events]. The
// event-derived stages (response_latency, address_detect, llm_turn) are also
// emitted here; the driving loop picks one source per stage to avoid
// double-counting (see Harness wiring).
//
// Safe for concurrent use: the orchestrator records from multiple goroutines
// (the TTS tee publishes first-audio off its own goroutine).
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

// observe.StageRecorder implementation. Each method maps to its locked Stage.
func (t *recorderTap) ResponseLatency(_ observe.AgentRole, d time.Duration) {
	t.add(StageResponseLatency, d)
}
func (t *recorderTap) VADHangover(d time.Duration)                            { t.add(StageVADHangover, d) }
func (t *recorderTap) AddressDetect(d time.Duration)                          { t.add(StageAddressDetect, d) }
func (t *recorderTap) CodecDecode(d time.Duration)                            { t.add(StageCodecDecode, d) }
func (t *recorderTap) CodecEncode(d time.Duration)                            { t.add(StageCodecEncode, d) }
func (t *recorderTap) STTRequest(_ observe.Provider, d time.Duration)         { t.add(StageSTTRequest, d) }
func (t *recorderTap) TTSTimeToFirstByte(_ observe.Provider, d time.Duration) { t.add(StageTTSTTFB, d) }
func (t *recorderTap) TTSTotal(_ observe.Provider, d time.Duration)           { t.add(StageTTSTotal, d) }
func (t *recorderTap) LLMRound(_ observe.Provider, _ int, _ bool, d time.Duration) {
	t.add(StageLLMRound, d)
}
func (t *recorderTap) LLMTurn(_ observe.Provider, d time.Duration) { t.add(StageLLMTurn, d) }

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

// compile-time assertion: the tap satisfies the recorder the orchestrator drives.
var _ observe.StageRecorder = (*recorderTap)(nil)
