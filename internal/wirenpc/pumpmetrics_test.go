package wirenpc

import (
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

// nopUsageSink stands in for the per-session spend meter the Manager tees usage to.
type nopUsageSink struct{}

func (nopUsageSink) LLMTokens(observe.Provider, string, int, int)    {}
func (nopUsageSink) TTSCharacters(observe.Provider, int)             {}
func (nopUsageSink) STTAudioSeconds(observe.Provider, time.Duration) {}

// TestPumpRecorderOptions_OnlyWhenRecording pins the #606 wiring: the pump gets a
// recorder option exactly when the configured StageRecorder can actually record the
// playback-gap series. A no-op recorder (env-only / keyless config) adds no option,
// so playback stays untouched.
func TestPumpRecorderOptions_OnlyWhenRecording(t *testing.T) {
	if got := len(pumpRecorderOptions(observe.NewPrometheusRecorder())); got != 1 {
		t.Fatalf("options for the Prometheus adapter = %d, want 1 (the gap/lane recorder)", got)
	}
	if got := len(pumpRecorderOptions(observe.Discard{})); got != 0 {
		t.Fatalf("options for the no-op recorder = %d, want 0", got)
	}
	if got := len(pumpRecorderOptions(nil)); got != 0 {
		t.Fatalf("options for a nil recorder = %d, want 0", got)
	}
}

// TestPumpRecorderOptions_ThroughTheSpendTee is the production-shape pin: the
// session Manager never hands the bare adapter down — it wraps it in
// observe.TeeUsage for the per-session spend meter. The capability must survive
// that wrap, or the #606 series read zero live while every bare-adapter test
// passes.
func TestPumpRecorderOptions_ThroughTheSpendTee(t *testing.T) {
	teed := observe.TeeUsage(observe.NewPrometheusRecorder(), nopUsageSink{})
	if got := len(pumpRecorderOptions(teed)); got != 1 {
		t.Fatalf("options behind the spend tee = %d, want 1 (the tee must not hide the pump recorder)", got)
	}
}
