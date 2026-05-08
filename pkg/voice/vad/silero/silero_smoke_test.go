package silero

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
)

// TestEngine_SmokeTest_RealONNX exercises the real path: ensureRuntime
// (cache-or-download), embedded-model loading, and a short silence run through
// the actual Silero VAD v5 model. It validates that the embed + runtime wiring
// in commit A2 actually works end-to-end on this platform.
//
// First run on a fresh machine downloads ~8 MB into the user cache; subsequent
// runs hit the cache and complete in well under a second.
func TestEngine_SmokeTest_RealONNX(t *testing.T) {
	eng, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cfg := vad.Config{
		SampleRate:       16000,
		FrameSizeMs:      32, // 512 samples — Silero v5 valid chunk size
		SpeechThreshold:  0.5,
		SilenceThreshold: 0.35,
	}
	sess, err := eng.NewSession(cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	// Feed 20 silent frames; expect every event to be VADSilence.
	chunkSize := cfg.SampleRate * cfg.FrameSizeMs / 1000
	frame := make([]byte, chunkSize*2)
	for i := range 20 {
		evt, err := sess.ProcessFrame(frame)
		if err != nil {
			t.Fatalf("frame %d: ProcessFrame: %v", i, err)
		}
		if evt.Type != vad.VADSilence {
			t.Errorf("frame %d: silence input produced %v (prob=%.3f), want VADSilence", i, evt.Type, evt.Probability)
		}
	}
}
