package silero

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
)

// TestEngine_SmokeTest exercises the real path: embedded-model parsing, graph
// compilation for both supported sample rates, and a short silence run
// through the actual Silero VAD v5 forward pass. Unlike earlier revisions
// this needs no ONNX Runtime download — the pure-Go engine is self-contained.
func TestEngine_SmokeTest(t *testing.T) {
	eng, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	configs := []vad.Config{
		{SampleRate: 16000, FrameSizeMs: 32, SpeechThreshold: 0.5, SilenceThreshold: 0.35}, // 512 samples
		{SampleRate: 8000, FrameSizeMs: 32, SpeechThreshold: 0.5, SilenceThreshold: 0.35},  // 256 samples
	}
	for _, cfg := range configs {
		sess, err := eng.NewSession(cfg)
		if err != nil {
			t.Fatalf("NewSession(%d Hz): %v", cfg.SampleRate, err)
		}
		t.Cleanup(func() { _ = sess.Close() })

		// Feed 20 silent frames; expect every event to be VADSilence.
		chunkSize := cfg.SampleRate * cfg.FrameSizeMs / 1000
		frame, err := audio.NewFrame(make([]int16, chunkSize), cfg.SampleRate, cfg.FrameSizeMs)
		if err != nil {
			t.Fatalf("audio.NewFrame: %v", err)
		}
		for i := range 20 {
			evt, err := sess.ProcessFrame(frame)
			if err != nil {
				t.Fatalf("%d Hz frame %d: ProcessFrame: %v", cfg.SampleRate, i, err)
			}
			if evt.Type != vad.VADSilence {
				t.Errorf("%d Hz frame %d: silence input produced %v (prob=%.3f), want VADSilence",
					cfg.SampleRate, i, evt.Type, evt.Probability)
			}
		}
	}
}

// BenchmarkProcessFrame measures one 32 ms / 16 kHz frame through the real
// pure-Go forward pass plus the session state machine — the per-frame cost
// every live speaker pays.
func BenchmarkProcessFrame(b *testing.B) {
	eng, err := New()
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	cfg := vad.Config{SampleRate: 16000, FrameSizeMs: 32, SpeechThreshold: 0.5, SilenceThreshold: 0.35}
	sess, err := eng.NewSession(cfg)
	if err != nil {
		b.Fatalf("NewSession: %v", err)
	}
	b.Cleanup(func() { _ = sess.Close() })
	pcm := make([]int16, 512)
	for i := range pcm {
		pcm[i] = int16((i * 7919) % 2000) // deterministic non-silent signal
	}
	frame, err := audio.NewFrame(pcm, cfg.SampleRate, cfg.FrameSizeMs)
	if err != nil {
		b.Fatalf("audio.NewFrame: %v", err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := sess.ProcessFrame(frame); err != nil {
			b.Fatal(err)
		}
	}
}
