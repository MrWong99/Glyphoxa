package voicetest_test

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

func TestHarness_AssertEventOccurred_FindsPublishedEvent(t *testing.T) {
	t.Parallel()
	h := voicetest.New(t)

	h.Bus.Publish(voiceevent.VADSpeechStart{Probability: 0.92})

	h.AssertEventOccurred(voiceevent.VADSpeechStart{})
}

func TestHarness_Events_ReturnsObservedEventsInOrder(t *testing.T) {
	t.Parallel()
	h := voicetest.New(t)

	h.Bus.Publish(voiceevent.VADSpeechStart{Probability: 0.5})
	h.Bus.Publish(voiceevent.VADSpeechStart{Probability: 0.9})

	got := h.Events()
	if len(got) != 2 {
		t.Fatalf("Events len = %d, want 2", len(got))
	}
	for i, e := range got {
		if e.EventName() != "vad.speech_start" {
			t.Errorf("event[%d] name = %q, want vad.speech_start", i, e.EventName())
		}
	}
}

