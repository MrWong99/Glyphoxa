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
// a speech_start while an Agent is AUDIBLY speaking (its FirstOpus is on the
// wire) cancels the turn and announces a BargeDetected.
func TestBargeIn_InstantYieldsActiveFloor(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "T1")
	turnCtx, release, _ := floor.Take(parent)
	defer release()

	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "T1"}) // the Agent is now audible
	h.Bus.Publish(voiceevent.VADSpeechStart{})

	if turnCtx.Err() == nil {
		t.Fatal("barge must cancel the held turn ctx")
	}
	if floor.Active() {
		t.Fatal("barge must free the floor")
	}
	voicetest.AssertEventCount[voiceevent.BargeDetected](t, h, 1)
}

// TestBargeIn_PublishesTurnEndedWithTurnID pins the barge attribution path
// (task #4): a confirmed barge publishes a TurnEnded carrying the cut turn's
// TurnID and the barge reason, so the metrics subscriber records why the turn
// died instead of the coarse no-first-audio catch-all.
func TestBargeIn_PublishesTurnEndedWithTurnID(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	// The turn holds the floor with its TurnID in ctx, as the reply reactor wires it.
	parent := voiceevent.WithTurnID(context.Background(), "T42")
	_, release, _ := floor.Take(parent)
	defer release()

	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "T42"}) // the Agent is now audible
	h.Bus.Publish(voiceevent.VADSpeechStart{})

	voicetest.AssertEvent(t, h,
		func(e voiceevent.TurnEnded) bool { return e.TurnID == "T42" && e.Reason == voiceevent.TurnEndBarge },
		"turn.ended (barge) carrying the cut turn's TurnID",
	)
}

// TestBargeIn_NoBargeWhileHolderNotYetSpeaking is the regression test for the
// no-NPC-response bug: a turn holds the floor but has NOT yet produced audio (no
// FirstOpus) — it is still in its pre-audio LLM "thinking" phase. A fresh
// speech_start in that window (the addressing user's OWN continued speech, or a
// VAD over-split of one utterance, under the single shared VAD session) must NOT
// barge: the agent is not audibly speaking, so there is nothing to interrupt.
// Before the fix the gate was floor.Active() (true from Take), so this
// self-cancelled every turn before first audio (outcome=abandoned reason=barge).
func TestBargeIn_NoBargeWhileHolderNotYetSpeaking(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	// The turn holds the floor (Take at AddressRouted) but no FirstOpus yet.
	parent := voiceevent.WithTurnID(context.Background(), "T1")
	turnCtx, release, _ := floor.Take(parent)
	defer release()

	// Confirm window 0 would yield instantly on a merely-held floor (the old bug);
	// the speaking gate must suppress it regardless of the window.
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.VADSpeechStart{})

	if turnCtx.Err() != nil {
		t.Fatal("a speech_start before the agent is audibly speaking must NOT barge: it is the user's own continued speech during the thinking phase, not an interruption")
	}
	voicetest.AssertNoEvent[voiceevent.BargeDetected](t, h)
}

// TestBargeIn_BargesOnceHolderIsSpeaking is the positive counterpart: once the
// turn's first Opus packet is on the wire (FirstOpus) the agent IS audibly
// speaking, so a speech_start over it is a genuine barge and cancels the turn.
func TestBargeIn_BargesOnceHolderIsSpeaking(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "T2")
	turnCtx, release, _ := floor.Take(parent)
	defer release()

	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))
	// The turn becomes audible on the wire.
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "T2"})
	h.Bus.Publish(voiceevent.VADSpeechStart{})

	if turnCtx.Err() == nil {
		t.Fatal("a speech_start while the agent is audibly speaking must barge the turn")
	}
	if floor.Active() {
		t.Fatal("barge must free the floor")
	}
	voicetest.AssertEventCount[voiceevent.BargeDetected](t, h, 1)
}

// TestBargeIn_SpeakingSignalForStaleTurnIgnored pins the attribution: a FirstOpus
// carrying a DIFFERENT turn's id (a late signal from an already-superseded turn)
// must not mark the current holder as speaking, so a speech_start during the
// current holder's pre-audio phase still does not barge.
func TestBargeIn_SpeakingSignalForStaleTurnIgnored(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "T3")
	turnCtx, release, _ := floor.Take(parent)
	defer release()

	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))
	// FirstOpus for a turn that is NOT the current holder: must be ignored.
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "stale-other-turn"})
	h.Bus.Publish(voiceevent.VADSpeechStart{})

	if turnCtx.Err() != nil {
		t.Fatal("a FirstOpus for a non-holder turn must not mark the holder speaking; the holder's pre-audio phase must still not barge")
	}
	voicetest.AssertNoEvent[voiceevent.BargeDetected](t, h)
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

// TestBargeIn_SoftOverlapDoesNotCancel pins the confirm-window contract: over an
// audibly-speaking Agent, speech that ends before the window elapses is a
// backchannel and never cancels.
func TestBargeIn_SoftOverlapDoesNotCancel(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "T1")
	turnCtx, release, _ := floor.Take(parent)
	defer release()

	t.Cleanup(orchestrator.NewBargeIn(floor, 200*time.Millisecond).Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "T1"}) // the Agent is audibly speaking
	h.Bus.Publish(voiceevent.VADSpeechStart{})
	h.Bus.Publish(voiceevent.VADSpeechEnd{}) // ends well before the 200ms window

	voicetest.AssertNoEvent[voiceevent.BargeDetected](t, h)
	if turnCtx.Err() != nil {
		t.Fatal("a soft-overlap backchannel must not cancel the turn")
	}
}

// TestBargeIn_ConfirmWindowCrossingCancels proves speech that persists past the
// confirm window fires the barge, over an audibly-speaking Agent.
func TestBargeIn_ConfirmWindowCrossingCancels(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "T1")
	turnCtx, release, _ := floor.Take(parent)
	defer release()

	t.Cleanup(orchestrator.NewBargeIn(floor, 20*time.Millisecond).Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "T1"}) // the Agent is audibly speaking
	h.Bus.Publish(voiceevent.VADSpeechStart{})        // no speech_end → window elapses

	select {
	case <-turnCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("speech crossing the confirm window must cancel the turn")
	}
	if floor.Active() {
		t.Fatal("barge must free the floor")
	}
}
