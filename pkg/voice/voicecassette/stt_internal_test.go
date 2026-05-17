package voicecassette

import (
	"context"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
)

// silenceFrame is a deterministic frame of zero samples, useful for
// hash-mismatch tests that want input distinct from a real clip's PCM.
func silenceFrame(t *testing.T, sampleRate, frameMs int) audio.Frame {
	t.Helper()
	n := sampleRate * frameMs / 1000
	f, err := audio.NewFrame(make([]int16, n), sampleRate, frameMs)
	if err != nil {
		t.Fatalf("audio.NewFrame: %v", err)
	}
	return f
}

// TestSTTRecognizer_HashMismatchPointsAtRecord pins the replay matcher's
// contract: when the incoming frames hash to something other than the
// cassette's recorded audio_sha256, the error must direct the caller at
// the re-record workflow.
//
// Whitebox so the test runs in both default and -tags=record builds —
// LoadSTT swaps implementations across build tags, but the replay matcher
// itself does not, and that's what's under test.
func TestSTTRecognizer_HashMismatchPointsAtRecord(t *testing.T) {
	t.Parallel()
	r := &STTRecognizer{
		name: "stt-fixture",
		cassette: STTCassette{
			AudioSHA256: "deadbeef",
			Transcript:  "ignored",
		},
	}
	_, err := r.Transcribe(context.Background(), []audio.Frame{silenceFrame(t, 16000, 32)})
	if err == nil {
		t.Fatal("Transcribe with wrong audio returned nil error")
	}
	if !strings.Contains(err.Error(), "-tags=record") {
		t.Errorf("error %q does not point at -tags=record", err)
	}
}
