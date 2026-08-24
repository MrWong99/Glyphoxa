package wirenpc

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

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
