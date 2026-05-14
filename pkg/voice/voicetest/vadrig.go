package voicetest

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad/silero"
)

// NewVADRig wires the shared VAD tracer-bullet setup: a real silero VAD
// session driving an [orchestrator.VAD] stage that publishes onto a fresh
// [Harness]'s bus, plus the named clip pre-framed at the session's chunk size.
//
// The configuration is fixed and matches every VAD tracer bullet:
// 16 kHz mono 16-bit PCM, 32 ms frames (silero v5's 512-sample chunk), and
// the default 0.5 / 0.35 speech / silence hysteresis thresholds. Tests that
// need a different configuration must wire the stage by hand.
//
// The silero session and harness subscription are torn down at the end of t.
// A clip whose format does not match the fixed configuration fails the test;
// a trailing partial frame is logged and dropped.
//
// Returned frames retain their declared SampleRate and FrameMs, so tests that
// need to append synthetic silence (e.g. forcing a speech_end transition) can
// size new frames from frames[0] without re-deriving the chunk size.
func NewVADRig(t *testing.T, clipName string) (*orchestrator.VAD, *Harness, []audio.Frame) {
	t.Helper()

	h := New(t)

	engine, err := silero.New()
	if err != nil {
		t.Fatalf("voicetest.NewVADRig(%q): silero.New: %v", clipName, err)
	}

	cfg := vad.Config{
		SampleRate:       16000,
		FrameSizeMs:      32,
		SpeechThreshold:  0.5,
		SilenceThreshold: 0.35,
	}
	sess, err := engine.NewSession(cfg)
	if err != nil {
		t.Fatalf("voicetest.NewVADRig(%q): engine.NewSession: %v", clipName, err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	stage := orchestrator.NewVAD(h.Bus, sess)

	clip := LoadClip(t, clipName)
	if clip.SampleRate != cfg.SampleRate {
		t.Fatalf("voicetest.NewVADRig(%q): clip sample rate %d Hz, want %d Hz",
			clipName, clip.SampleRate, cfg.SampleRate)
	}
	if clip.Channels != 1 || clip.BitDepth != 16 {
		t.Fatalf("voicetest.NewVADRig(%q): clip format %dch %d-bit, want 1ch 16-bit",
			clipName, clip.Channels, clip.BitDepth)
	}

	chunkSize := cfg.SampleRate * cfg.FrameSizeMs / 1000
	frames, tail := clip.FramesOf(t, chunkSize)
	if tail != 0 {
		t.Logf("voicetest.NewVADRig(%q): trailing %d samples (%d ms) not frame-aligned; discarded",
			clipName, tail, tail*1000/cfg.SampleRate)
	}

	return stage, h, frames
}
