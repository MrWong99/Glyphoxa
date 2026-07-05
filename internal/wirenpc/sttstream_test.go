package wirenpc

import (
	"context"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
)

// batchOnlyRecognizer implements only the batch [stt.Recognizer] — no streaming.
type batchOnlyRecognizer struct{}

func (batchOnlyRecognizer) Transcribe(context.Context, []audio.Frame) (stt.Transcript, error) {
	return stt.Transcript{}, nil
}

// streamingRecognizer implements both the batch and streaming interfaces, like the
// real ElevenLabs Client (ADR-0042).
type streamingRecognizer struct{ batchOnlyRecognizer }

func (streamingRecognizer) OpenStream(context.Context, stt.StreamConfig) (stt.Stream, error) {
	return nil, nil
}

// TestBuildStreamManager_Gating pins the selection seam (issue #180, C6): a manager
// is wired only when streaming is enabled AND the recognizer supports streaming;
// otherwise the byte-for-byte batch path (nil manager) is kept.
func TestBuildStreamManager_Gating(t *testing.T) {
	if got := buildStreamManager(streamingRecognizer{}, true, observe.Discard{}); got == nil {
		t.Error("streaming enabled + a streaming recognizer must wire a manager")
	}
	if got := buildStreamManager(streamingRecognizer{}, false, observe.Discard{}); got != nil {
		t.Error("streaming disabled must not wire a manager, even for a streaming recognizer")
	}
	if got := buildStreamManager(batchOnlyRecognizer{}, true, observe.Discard{}); got != nil {
		t.Error("a batch-only recognizer must not wire a manager, even with streaming enabled")
	}
}
