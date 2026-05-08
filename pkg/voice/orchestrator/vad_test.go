package orchestrator_test

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad/silero"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// TestVAD_HelloTest_EmitsSpeechStart is TB1: the foundation tracer bullet
// for the orchestrator-first TDD voice pipeline (ADR-0019).
//
// It feeds the "hello-test" fixture (a GM addressing the Butler) through a
// real silero-VAD session driven by the orchestrator's VAD stage and asserts
// that exactly the speech-onset event reaches the shared event bus
// (ADR-0020). Subsequent tracer bullets layer speech_end, ordering, STT,
// address detection, etc. on top.
func TestVAD_HelloTest_EmitsSpeechStart(t *testing.T) {
	h := voicetest.New(t)

	engine, err := silero.New()
	if err != nil {
		t.Fatalf("silero.New: %v", err)
	}

	cfg := vad.Config{
		SampleRate:       16000,
		FrameSizeMs:      32, // 512 samples — silero v5 valid chunk size
		SpeechThreshold:  0.5,
		SilenceThreshold: 0.35,
	}
	sess, err := engine.NewSession(cfg)
	if err != nil {
		t.Fatalf("engine.NewSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	stage := orchestrator.NewVAD(h.Bus, sess)

	clip := voicetest.LoadClip(t, "hello-test")
	if clip.SampleRate != cfg.SampleRate {
		t.Fatalf("clip sample rate %d Hz, want %d Hz", clip.SampleRate, cfg.SampleRate)
	}
	if clip.Channels != 1 || clip.BitDepth != 16 {
		t.Fatalf("clip format %dch %d-bit, want 1ch 16-bit", clip.Channels, clip.BitDepth)
	}

	chunkSize := cfg.SampleRate * cfg.FrameSizeMs / 1000
	for i, frame := range clip.FramesOf(chunkSize) {
		if err := stage.Process(frame); err != nil {
			t.Fatalf("frame %d: stage.Process: %v", i, err)
		}
	}

	h.AssertEventOccurred(voiceevent.VADSpeechStart{})
}
