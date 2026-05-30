package voicetest

import (
	"strings"
	"testing"
)

// TestFramesOf_WholeMsConfig pins the happy-path framing math: a whole-ms frame
// size splits the PCM into the expected number of frames and reports the
// leftover partial frame as tailSamples rather than dropping it.
func TestFramesOf_WholeMsConfig(t *testing.T) {
	// 16 kHz / 32 ms = 512 samples = 1024 bytes per frame. 2.5 frames of data.
	c := &Clip{Name: "synthetic", SampleRate: 16000, Channels: 1, BitDepth: 16,
		PCM: make([]byte, 1024*2+512)}

	frames, tail, err := c.framesOf(512)
	if err != nil {
		t.Fatalf("framesOf: %v", err)
	}
	if len(frames) != 2 {
		t.Errorf("got %d full frames, want 2", len(frames))
	}
	if tail != 256 { // 512 leftover bytes / 2 bytes per sample
		t.Errorf("tailSamples = %d, want 256", tail)
	}
	for i, f := range frames {
		if got := len(f.Samples()); got != 512 {
			t.Errorf("frame %d has %d samples, want 512", i, got)
		}
		if f.FrameMs() != 32 {
			t.Errorf("frame %d FrameMs = %d, want 32", i, f.FrameMs())
		}
	}
}

// TestFramesOf_RejectsNonWholeMs is the regression test for the lossy frameMs
// derivation: a frame size that is not a whole number of milliseconds at the
// clip's rate must be rejected up front with a clear message, not surface as a
// confusing per-frame decode failure.
func TestFramesOf_RejectsNonWholeMs(t *testing.T) {
	// 44100 Hz / 512 samples ≈ 11.61 ms — not whole.
	c := &Clip{Name: "synthetic", SampleRate: 44100, Channels: 1, BitDepth: 16,
		PCM: make([]byte, 1024*4)}

	_, _, err := c.framesOf(512)
	if err == nil {
		t.Fatal("expected an error for a non-whole-ms frame size, got nil")
	}
	if !strings.Contains(err.Error(), "whole number of milliseconds") {
		t.Errorf("error %q does not explain the whole-ms requirement", err)
	}
}

func TestFramesOf_RejectsBadClip(t *testing.T) {
	cases := []struct {
		name string
		clip *Clip
		spf  int
		want string
	}{
		{"stereo", &Clip{SampleRate: 16000, Channels: 2, BitDepth: 16}, 512, "channels"},
		{"8-bit", &Clip{SampleRate: 16000, Channels: 1, BitDepth: 8}, 512, "16-bit"},
		{"zero frame size", &Clip{SampleRate: 16000, Channels: 1, BitDepth: 16}, 0, "must be > 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := tc.clip.framesOf(tc.spf)
			if err == nil {
				t.Fatalf("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}
