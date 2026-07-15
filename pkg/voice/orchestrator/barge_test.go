package orchestrator_test

import (
	"context"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
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
	turnCtx, release, _ := floor.Take(parent, "")
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
	_, release, _ := floor.Take(parent, "")
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
	turnCtx, release, _ := floor.Take(parent, "")
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
	turnCtx, release, _ := floor.Take(parent, "")
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
	turnCtx, release, _ := floor.Take(parent, "")
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
	turnCtx, release, _ := floor.Take(parent, "")
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
	turnCtx, release, _ := floor.Take(parent, "")
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

// TestBargeIn_PerSpeakerWindowNotDisarmedByOther is step 9 (ADR-0050): speaker A's
// confirm window is armed; speaker B's speech_end arrives inside it. B's speech_end
// must NOT disarm A's window — A's sustained interruption still fires the barge, and
// the BargeDetected names A. Under one shared VAD (the pre-lane MVP) B's pause would
// have disarmed A's window and swallowed the interruption.
func TestBargeIn_PerSpeakerWindowNotDisarmedByOther(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "T1")
	turnCtx, release, _ := floor.Take(parent, "")
	defer release()

	t.Cleanup(orchestrator.NewBargeIn(floor, 40*time.Millisecond).Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "T1"}) // the Agent is audibly speaking

	// A starts interrupting; B backchannels and stops — B's speech_end must leave A's
	// window armed.
	h.Bus.Publish(voiceevent.VADSpeechStart{SpeakerID: "A"})
	h.Bus.Publish(voiceevent.VADSpeechStart{SpeakerID: "B"})
	h.Bus.Publish(voiceevent.VADSpeechEnd{SpeakerID: "B"}) // only B stopped

	// fire() cancels turnCtx BEFORE it publishes BargeDetected, so syncing on
	// turnCtx.Done() then snapshotting the log races the publish (the sibling flake
	// this fix targets). WaitEvent polls until the BargeDetected is observed; once
	// it is, the earlier cancel is guaranteed visible.
	voicetest.WaitEvent(t, h, 2*time.Second,
		func(e voiceevent.BargeDetected) bool { return e.SpeakerID == "A" },
		"barge.detected attributed to speaker A (A's sustained interruption; B's speech_end must not disarm A's window)",
	)
	if turnCtx.Err() == nil {
		t.Fatal("the barge must have cancelled the turn before publishing BargeDetected")
	}
}

// TestBargeIn_SameSpeakerSoftOverlapDisarms pins the complement: a speaker's OWN
// speech_end inside the window disarms it (a soft-overlap backchannel from that
// speaker), so no barge fires and the turn survives — the per-speaker analogue of
// TestBargeIn_SoftOverlapDoesNotCancel.
func TestBargeIn_SameSpeakerSoftOverlapDisarms(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "T1")
	turnCtx, release, _ := floor.Take(parent, "")
	defer release()

	t.Cleanup(orchestrator.NewBargeIn(floor, 200*time.Millisecond).Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "T1"})
	h.Bus.Publish(voiceevent.VADSpeechStart{SpeakerID: "A"})
	h.Bus.Publish(voiceevent.VADSpeechEnd{SpeakerID: "A"}) // A's own backchannel ends the window

	voicetest.AssertNoEvent[voiceevent.BargeDetected](t, h)
	if turnCtx.Err() != nil {
		t.Fatal("a same-speaker soft-overlap backchannel must not cancel the turn")
	}
}

// TestBargeIn_VoicingStoppedDisarms is the #431 core: the PROVISIONAL
// voicing_stopped — published as soon as the speaker actually falls silent,
// long before the hangover-delayed segment speech_end — disarms the window, so
// a backchannel burst shorter than the window never cancels the Agent even
// though its speech_end arrives (hangover) after the window would have fired.
// The test waits out the window to prove the timer really was disarmed, not
// merely not-yet-fired, then delivers the late speech_end as production would.
func TestBargeIn_VoicingStoppedDisarms(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "T1")
	turnCtx, release, _ := floor.Take(parent, "")
	defer release()

	const window = 60 * time.Millisecond
	t.Cleanup(orchestrator.NewBargeIn(floor, window).Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "T1"})
	h.Bus.Publish(voiceevent.VADSpeechStart{SpeakerID: "A"})
	h.Bus.Publish(voiceevent.VADVoicingStopped{SpeakerID: "A"}) // the burst really ended

	time.Sleep(3 * window) // the armed timer would have fired by now
	voicetest.AssertNoEvent[voiceevent.BargeDetected](t, h)
	if turnCtx.Err() != nil {
		t.Fatal("a voicing_stopped inside the window must disarm it: the burst was a soft-overlap backchannel")
	}

	// The segment-final speech_end lands only after the hangover — far too late
	// to have been the disarm — and must stay a harmless no-op.
	h.Bus.Publish(voiceevent.VADSpeechEnd{SpeakerID: "A"})
	voicetest.AssertNoEvent[voiceevent.BargeDetected](t, h)
	if turnCtx.Err() != nil {
		t.Fatal("the hangover-delayed speech_end must not cancel anything")
	}
}

// TestBargeIn_VoicingResumedReArms pins the onset half of #431: a speaker who
// pauses briefly (voicing_stopped disarms) and then KEEPS TALKING inside the
// still-open utterance fires no fresh speech_start — voicing_resumed is the
// only onset signal — so it must re-arm the window, and the now-continuous
// speech must still barge the Agent.
func TestBargeIn_VoicingResumedReArms(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "T1")
	turnCtx, release, _ := floor.Take(parent, "")
	defer release()

	t.Cleanup(orchestrator.NewBargeIn(floor, 30*time.Millisecond).Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "T1"})
	h.Bus.Publish(voiceevent.VADSpeechStart{SpeakerID: "A"})
	h.Bus.Publish(voiceevent.VADVoicingStopped{SpeakerID: "A"}) // brief pause: disarm
	h.Bus.Publish(voiceevent.VADVoicingResumed{SpeakerID: "A"}) // keeps talking: re-arm

	voicetest.WaitEvent(t, h, 2*time.Second,
		func(e voiceevent.BargeDetected) bool { return e.SpeakerID == "A" },
		"barge.detected for A's resumed, sustained speech (voicing_resumed must re-arm the window)",
	)
	if turnCtx.Err() == nil {
		t.Fatal("resumed sustained speech must cancel the turn")
	}
}

// TestBargeIn_VoicingStoppedOtherSpeakerKeepsWindow extends the ADR-0050
// per-speaker keying to the provisional transitions: B's voicing_stopped must
// not disarm A's window — A's sustained interruption still fires.
func TestBargeIn_VoicingStoppedOtherSpeakerKeepsWindow(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "T1")
	turnCtx, release, _ := floor.Take(parent, "")
	defer release()

	t.Cleanup(orchestrator.NewBargeIn(floor, 40*time.Millisecond).Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "T1"})
	h.Bus.Publish(voiceevent.VADSpeechStart{SpeakerID: "A"})
	h.Bus.Publish(voiceevent.VADVoicingStopped{SpeakerID: "B"}) // only B paused

	voicetest.WaitEvent(t, h, 2*time.Second,
		func(e voiceevent.BargeDetected) bool { return e.SpeakerID == "A" },
		"barge.detected attributed to A (B's voicing_stopped must not disarm A's window)",
	)
	if turnCtx.Err() == nil {
		t.Fatal("A's sustained interruption must cancel the turn")
	}
}

// TestBargeIn_ExpiryAfterHolderChange_DoesNotCancelNewTurn is the #432
// regression: Gate 1 must hold at window EXPIRY, not only at arm. The window
// arms against speaking turn T1; T1 ends naturally inside the window and a
// NEW turn T2 takes the floor, still silent in its pre-audio LLM phase. The
// expiring timer must not cancel T2 — the human's overlapping speech was
// aimed at T1, and killing a turn that has produced no audio is exactly the
// `no_audio` self-cancel class ADR-0027's Gate 1 exists to prevent.
func TestBargeIn_ExpiryAfterHolderChange_DoesNotCancelNewTurn(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()

	const window = 60 * time.Millisecond
	t.Cleanup(orchestrator.NewBargeIn(floor, window).Bind(t.Context(), h.Bus))

	// T1 speaks; a participant starts talking over it: the window arms on T1.
	parent1 := voiceevent.WithTurnID(context.Background(), "T1")
	_, release1, _ := floor.Take(parent1, "")
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "T1"})
	h.Bus.Publish(voiceevent.VADSpeechStart{SpeakerID: "A"})

	// T1 finishes naturally inside the window; T2 takes the floor, pre-audio.
	release1()
	parent2 := voiceevent.WithTurnID(context.Background(), "T2")
	turnCtx2, release2, _ := floor.Take(parent2, "")
	defer release2()

	time.Sleep(3 * window) // let the armed timer expire
	if turnCtx2.Err() != nil {
		t.Fatal("the expiring window armed on T1 must not cancel the new, not-yet-audible turn T2")
	}
	if !floor.Active() {
		t.Fatal("T2 must still hold the floor")
	}
	voicetest.AssertNoEvent[voiceevent.BargeDetected](t, h)
	voicetest.AssertNoEvent[voiceevent.TurnEnded](t, h)
}

// TestBargeIn_ExpiryAfterHolderChange_SparesNewSpeakingTurn pins the STRICT
// identity reading of the Gate-1 re-check (#432): even when the replacement
// turn T2 has begun speaking by expiry, the window armed against T1 must not
// cut it — the human started talking before they had heard a word of T2, so
// their speech cannot have been an interruption of it. T2's own barge window
// arms on the human's next voicing onset, not this stale one.
func TestBargeIn_ExpiryAfterHolderChange_SparesNewSpeakingTurn(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()

	const window = 60 * time.Millisecond
	t.Cleanup(orchestrator.NewBargeIn(floor, window).Bind(t.Context(), h.Bus))

	parent1 := voiceevent.WithTurnID(context.Background(), "T1")
	_, release1, _ := floor.Take(parent1, "")
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "T1"})
	h.Bus.Publish(voiceevent.VADSpeechStart{SpeakerID: "A"})

	// T1 ends; T2 takes the floor AND becomes audible inside the window.
	release1()
	parent2 := voiceevent.WithTurnID(context.Background(), "T2")
	turnCtx2, release2, _ := floor.Take(parent2, "")
	defer release2()
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "T2"})

	time.Sleep(3 * window)
	if turnCtx2.Err() != nil {
		t.Fatal("a window armed on T1 must not cancel T2, even though T2 is speaking at expiry")
	}
	voicetest.AssertNoEvent[voiceevent.BargeDetected](t, h)
}

// TestBargeIn_SoftOverlapBurst_StillTranscribed closes #431's remaining half:
// the backchannel burst that must NOT barge must still run the normal
// transcription path (Soft-overlap per CONTEXT.md is "transcribed and
// committed normally — it just doesn't cancel the Agent"). A scripted VAD
// drives the segmenter through start → voicing_stopped (disarm) → the
// hangover → speech_end on the SAME bus a BargeIn is bound to: the turn
// survives the window expiry, and the utterance still reaches the recognizer
// and publishes its STTFinal.
func TestBargeIn_SoftOverlapBurst_StillTranscribed(t *testing.T) {
	h := voicetest.New(t)
	rec := &recordingRecognizer{}
	vadStage := orchestrator.NewVAD(h.Bus, &scriptedVAD{events: []vad.VADEventType{
		vad.VADSpeechStart,    // burst onset: window arms
		vad.VADVoicingStopped, // burst really ended: window disarms
		vad.VADSpeechContinue, // hangover still counting
		vad.VADSpeechEnd,      // segment closes: transcription dispatches
	}})
	sttStage := orchestrator.NewSTT(h.Bus, rec)
	seg := orchestrator.NewSegmenter(vadStage, sttStage)

	floor := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "T1")
	turnCtx, release, _ := floor.Take(parent, "")
	defer release()

	const window = 50 * time.Millisecond
	t.Cleanup(orchestrator.NewBargeIn(floor, window).Bind(t.Context(), h.Bus))
	t.Cleanup(seg.Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "T1"}) // the Agent is audibly speaking

	feed(t, seg, 2)        // start + voicing_stopped: armed, then disarmed
	time.Sleep(3 * window) // the window would have fired had the stop not disarmed it
	feed(t, seg, 2)        // hangover frame + speech_end: the burst commits to STT

	if err := seg.Flush(); err != nil {
		t.Fatalf("seg.Flush: %v", err)
	}
	if got := len(rec.batches()); got != 1 {
		t.Fatalf("recognizer saw %d segments, want 1: the soft-overlap burst must still be transcribed", got)
	}
	voicetest.AssertEventCount[voiceevent.STTFinal](t, h, 1)
	voicetest.AssertNoEvent[voiceevent.BargeDetected](t, h)
	if turnCtx.Err() != nil {
		t.Fatal("the soft-overlap burst must not cancel the Agent's turn")
	}
}

// TestBargeIn_ExpiryOnFreeFloor_NoSpuriousEvent: the window arms on a speaking
// turn which then ends naturally, leaving the floor free at expiry. Nothing is
// cancelled and no BargeDetected/TurnEnded is announced (#432 AC3).
func TestBargeIn_ExpiryOnFreeFloor_NoSpuriousEvent(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()

	const window = 60 * time.Millisecond
	t.Cleanup(orchestrator.NewBargeIn(floor, window).Bind(t.Context(), h.Bus))

	parent := voiceevent.WithTurnID(context.Background(), "T1")
	_, release, _ := floor.Take(parent, "")
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "T1"})
	h.Bus.Publish(voiceevent.VADSpeechStart{SpeakerID: "A"})
	release() // T1 finishes on its own; floor is free

	time.Sleep(3 * window)
	voicetest.AssertNoEvent[voiceevent.BargeDetected](t, h)
	voicetest.AssertNoEvent[voiceevent.TurnEnded](t, h)
}
