package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// TestBargeIn_InstantYieldsActiveFloor pins the instant mode (confirm window 0):
// a speech_start while an Agent holds the floor cancels the turn and announces a
// BargeDetected.
func TestBargeIn_InstantYieldsActiveFloor(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	turnCtx, release := floor.Take(context.Background())
	defer release()

	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.VADSpeechStart{})

	if turnCtx.Err() == nil {
		t.Fatal("barge must cancel the held turn ctx")
	}
	if floor.Active() {
		t.Fatal("barge must free the floor")
	}
	voicetest.AssertEventCount[voiceevent.BargeDetected](t, h, 1)
}

// TestBargeIn_NoFloorNoBarge proves a speech_start with no Agent speaking is a
// normal utterance onset, not a barge: nothing is cancelled, nothing emitted.
func TestBargeIn_NoFloorNoBarge(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor() // never taken: no turn is speaking

	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.VADSpeechStart{})

	voicetest.AssertNoEvent[voiceevent.BargeDetected](t, h)
}

// TestBargeIn_SoftOverlapDoesNotCancel pins the confirm-window contract: speech
// that ends before the window elapses is a backchannel and never cancels.
func TestBargeIn_SoftOverlapDoesNotCancel(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	turnCtx, release := floor.Take(context.Background())
	defer release()

	t.Cleanup(orchestrator.NewBargeIn(floor, 200*time.Millisecond).Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.VADSpeechStart{})
	h.Bus.Publish(voiceevent.VADSpeechEnd{}) // ends well before the 200ms window

	voicetest.AssertNoEvent[voiceevent.BargeDetected](t, h)
	if turnCtx.Err() != nil {
		t.Fatal("a soft-overlap backchannel must not cancel the turn")
	}
}

// TestBargeIn_ConfirmWindowCrossingCancels proves speech that persists past the
// confirm window fires the barge.
func TestBargeIn_ConfirmWindowCrossingCancels(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	turnCtx, release := floor.Take(context.Background())
	defer release()

	t.Cleanup(orchestrator.NewBargeIn(floor, 20*time.Millisecond).Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.VADSpeechStart{}) // no speech_end → window elapses

	select {
	case <-turnCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("speech crossing the confirm window must cancel the turn")
	}
	if floor.Active() {
		t.Fatal("barge must free the floor")
	}
}
