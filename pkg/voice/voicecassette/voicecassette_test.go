package voicecassette_test

import (
	"context"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicecassette"
)

// silenceFrame produces a deterministic frame of zero samples for hash tests.
func silenceFrame(t *testing.T, sampleRate, frameMs int) audio.Frame {
	t.Helper()
	n := sampleRate * frameMs / 1000
	f, err := audio.NewFrame(make([]int16, n), sampleRate, frameMs)
	if err != nil {
		t.Fatalf("audio.NewFrame: %v", err)
	}
	return f
}

func TestHashFrames_StableAcrossFraming(t *testing.T) {
	t.Parallel()

	// Two framings of the same underlying PCM (one 32ms frame vs two 16ms
	// frames at 16 kHz) must produce the same hash — HashFrames is over
	// the sample stream, not the frame boundaries.
	one := silenceFrame(t, 16000, 32)
	two := []audio.Frame{
		silenceFrame(t, 16000, 16),
		silenceFrame(t, 16000, 16),
	}

	if a, b := voicecassette.HashFrames([]audio.Frame{one}), voicecassette.HashFrames(two); a != b {
		t.Errorf("hash differs across reframings:\n  32ms x1: %s\n  16ms x2: %s", a, b)
	}
}

func TestSTTRecognizer_HashMismatchPointsAtRecord(t *testing.T) {
	t.Parallel()

	// hello-test is loaded by name but we feed *silence* instead of the
	// clip's PCM. The cassette must refuse to replay and the error must
	// nudge the caller to re-record.
	r := voicecassette.LoadSTT(t, "stt-hello-test")
	_, err := r.Transcribe(context.Background(), []audio.Frame{silenceFrame(t, 16000, 32)})
	if err == nil {
		t.Fatal("Transcribe with wrong audio returned nil error")
	}
	if !strings.Contains(err.Error(), "-tags=record") {
		t.Errorf("error %q does not point at -tags=record", err)
	}
}
