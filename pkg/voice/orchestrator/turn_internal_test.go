package orchestrator

import (
	"context"
	"errors"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// These are the #444 turn-module state-machine tests: the deliver-then-commit
// protocol (ADR-0012) in one tested place — delivered / not-delivered /
// cut-before / cut-mid-drain / zero-delivered / terminal-reason mapping —
// instead of seven replicated dispatch sites each hand-rolling it.

// fakeSynth is a scripted stand-in for TTS.Dispatch.
type fakeSynth struct {
	calls     []string
	lookahead []bool // whether each call's ctx was playback-lookahead-marked
	err       error  // returned by every call
	onCall    func() // runs before returning (e.g. cancel the turn ctx mid-drain)
}

func (f *fakeSynth) dispatch(ctx context.Context, sentence string, _ tts.Voice) error {
	f.calls = append(f.calls, sentence)
	f.lookahead = append(f.lookahead, voiceevent.IsPlaybackLookahead(ctx))
	if f.onCall != nil {
		f.onCall()
	}
	return f.err
}

// errCollector records errors surfaced through the module's ErrorFunc.
type errCollector struct{ got []error }

func (c *errCollector) fn(err error) { c.got = append(c.got, err) }

func TestTurnRun_Delivered_FiresHookOnce(t *testing.T) {
	synth := &fakeSynth{}
	ec := &errCollector{}
	run := newTurnRun(context.Background(), synth.dispatch, ec.fn)

	fired := 0
	err := run.dispatch(Reply{Sentence: "hello", OnDelivered: func() { fired++ }})
	if err != nil {
		t.Fatalf("dispatch = %v, want nil (delivered)", err)
	}
	if OutcomeOf(err) != SentenceDelivered {
		t.Fatalf("OutcomeOf(nil) = %v, want SentenceDelivered", OutcomeOf(err))
	}
	if fired != 1 {
		t.Fatalf("OnDelivered fired %d times, want exactly 1", fired)
	}
	if len(synth.calls) != 1 || synth.calls[0] != "hello" {
		t.Fatalf("synth calls = %v, want [hello]", synth.calls)
	}
	if run.ttsFailed || !run.attempted {
		t.Fatalf("state after delivery: ttsFailed=%v attempted=%v, want false/true", run.ttsFailed, run.attempted)
	}
	if got := run.finish(nil); got != "" {
		t.Fatalf("finish(nil) after clean delivery = %q, want \"\"", got)
	}
	if len(ec.got) != 0 {
		t.Fatalf("no error should surface on delivery, got %v", ec.got)
	}
}

func TestTurnRun_StartError_NotDeliveredKeepsTurnAlive(t *testing.T) {
	boom := errors.New("synth down")
	synth := &fakeSynth{err: boom}
	ec := &errCollector{}
	run := newTurnRun(context.Background(), synth.dispatch, ec.fn)

	fired := false
	err := run.dispatch(Reply{Sentence: "s1", OnDelivered: func() { fired = true }})
	if !errors.Is(err, ErrNotDelivered) {
		t.Fatalf("dispatch on start-error = %v, want ErrNotDelivered", err)
	}
	if OutcomeOf(err) != SentenceNotDelivered {
		t.Fatalf("OutcomeOf = %v, want SentenceNotDelivered", OutcomeOf(err))
	}
	if fired {
		t.Fatal("a start-errored sentence was never delivered: OnDelivered must not fire")
	}
	if !run.ttsFailed {
		t.Fatal("a start-error under a live ctx must set ttsFailed")
	}
	if len(ec.got) != 1 || !errors.Is(ec.got[0], boom) {
		t.Fatalf("the synth error must surface via ErrorFunc, got %v", ec.got)
	}
	// Zero-delivered turn: the sticky failure is the terminal reason.
	if got := run.finish(nil); got != voiceevent.TurnEndTTSError {
		t.Fatalf("finish(nil) after all-start-error = %q, want tts_error", got)
	}
}

func TestTurnRun_CutBeforeDispatch_NeverSynthesizes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	synth := &fakeSynth{}
	run := newTurnRun(ctx, synth.dispatch, nil)

	fired := false
	err := run.dispatch(Reply{Sentence: "late", OnDelivered: func() { fired = true }})
	if OutcomeOf(err) != SentenceCut {
		t.Fatalf("dispatch on dead ctx = %v (outcome %v), want a cut", err, OutcomeOf(err))
	}
	if len(synth.calls) != 0 {
		t.Fatal("a cut turn must never reach the synthesizer")
	}
	if fired || run.attempted || run.ttsFailed {
		t.Fatalf("cut-before state: fired=%v attempted=%v ttsFailed=%v, want all false", fired, run.attempted, run.ttsFailed)
	}
	if got := run.finish(nil); got != "" {
		t.Fatalf("finish on a cut turn = %q, want \"\" (the cutter publishes its own terminal)", got)
	}
}

func TestTurnRun_CutMidDrain_UndeliveredNotCommitted(t *testing.T) {
	// Dispatch returns nil but the ctx died DURING the drain (barge/mute): the
	// sentence is ambiguous, treated as undelivered (ADR-0012 under-report bias).
	ctx, cancel := context.WithCancel(context.Background())
	synth := &fakeSynth{onCall: cancel} // the cut lands inside the synth call
	run := newTurnRun(ctx, synth.dispatch, nil)

	fired := false
	err := run.dispatch(Reply{Sentence: "s1", OnDelivered: func() { fired = true }})
	if OutcomeOf(err) != SentenceCut {
		t.Fatalf("dispatch cut mid-drain = %v (outcome %v), want a cut", err, OutcomeOf(err))
	}
	if fired {
		t.Fatal("a cut-mid-drain sentence must NOT commit (OnDelivered uninvoked)")
	}
}

func TestTurnRun_SynthErrorOnDeadCtx_IsCutNotTTSError(t *testing.T) {
	// The synth surfaces an error BECAUSE the ctx was cancelled: that is a cut,
	// not a tts_error — ttsFailed must stay unset so the cutter's terminal wins.
	ctx, cancel := context.WithCancel(context.Background())
	synth := &fakeSynth{err: context.Canceled, onCall: cancel}
	ec := &errCollector{}
	run := newTurnRun(ctx, synth.dispatch, ec.fn)

	err := run.dispatch(Reply{Sentence: "s1"})
	if OutcomeOf(err) != SentenceCut {
		t.Fatalf("outcome = %v, want SentenceCut", OutcomeOf(err))
	}
	if run.ttsFailed {
		t.Fatal("a cancel-induced synth error must not set ttsFailed")
	}
	if len(ec.got) != 1 {
		t.Fatalf("the synth error still surfaces via ErrorFunc (site parity), got %v", ec.got)
	}
}

func TestTurnRun_Finish_ProducerErrorMapping(t *testing.T) {
	t.Run("text delivered", func(t *testing.T) {
		run := newTurnRun(context.Background(), (&fakeSynth{}).dispatch, nil)
		if got := run.finish(ErrTextDelivered); got != voiceevent.TurnEndTextDelivered {
			t.Fatalf("finish(ErrTextDelivered) = %q, want text_delivered", got)
		}
	})
	t.Run("genuine producer error", func(t *testing.T) {
		ec := &errCollector{}
		run := newTurnRun(context.Background(), (&fakeSynth{}).dispatch, ec.fn)
		boom := errors.New("llm down")
		if got := run.finish(boom); got != voiceevent.TurnEndProviderError {
			t.Fatalf("finish(genuine err) = %q, want provider_error", got)
		}
		if len(ec.got) != 1 || !errors.Is(ec.got[0], boom) {
			t.Fatalf("a producer error must surface via ErrorFunc, got %v", ec.got)
		}
	})
	t.Run("leaked ErrNotDelivered is tts_error not provider_error", func(t *testing.T) {
		ec := &errCollector{}
		synth := &fakeSynth{err: errors.New("synth down")}
		run := newTurnRun(context.Background(), synth.dispatch, ec.fn)
		_ = run.dispatch(Reply{Sentence: "s1"}) // sets ttsFailed
		if got := run.finish(ErrNotDelivered); got != voiceevent.TurnEndTTSError {
			t.Fatalf("finish(leaked ErrNotDelivered) = %q, want tts_error", got)
		}
		// Only the synth error surfaced — the leaked sentinel is not an error.
		if len(ec.got) != 1 {
			t.Fatalf("the sentinel leak must not surface via ErrorFunc, got %v", ec.got)
		}
	})
	t.Run("cut turn reports nothing", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		run := newTurnRun(ctx, (&fakeSynth{}).dispatch, nil)
		if got := run.finish(errors.New("anything")); got != "" {
			t.Fatalf("finish on a cut turn = %q, want \"\" (the cutter owns the terminal)", got)
		}
	})
}

func TestTurnRun_DispatchHeld_LookaheadFirstSentence(t *testing.T) {
	synth := &fakeSynth{}
	run := newTurnRun(context.Background(), synth.dispatch, nil)

	var held string
	abort := errors.New("lookahead aborted")
	err := run.dispatchHeld(Reply{Sentence: "first"}, func(s string) { held = s }, abort)
	if err != nil {
		t.Fatalf("dispatchHeld = %v, want nil (delivered)", err)
	}
	if held != "first" {
		t.Fatalf("onHeld got %q, want the sentence BEFORE the blocking synth", held)
	}
	if len(synth.lookahead) != 1 || !synth.lookahead[0] {
		t.Fatal("the held first sentence must be synthesized under a playback-lookahead-marked ctx")
	}
}

func TestTurnRun_DispatchHeld_StartErrorAbortsWithSentinel(t *testing.T) {
	// A start-error on the HELD first sentence must return the caller's abort
	// error (NOT ErrNotDelivered): skip-and-continue would let sentence 2
	// leapfrog the still-playing Lead (#375).
	synth := &fakeSynth{err: errors.New("synth down")}
	ec := &errCollector{}
	run := newTurnRun(context.Background(), synth.dispatch, ec.fn)

	abort := errors.New("lookahead aborted")
	err := run.dispatchHeld(Reply{Sentence: "first"}, func(string) {}, abort)
	if !errors.Is(err, abort) {
		t.Fatalf("dispatchHeld on start-error = %v, want the abort error", err)
	}
	if errors.Is(err, ErrNotDelivered) {
		t.Fatal("the abort must not be the skip-and-continue sentinel")
	}
	if !run.ttsFailed {
		t.Fatal("a held-first start-error still counts toward the tts_error verdict (#391)")
	}
}

func TestTurnRun_DispatchHeld_CutBeforeHoldsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	synth := &fakeSynth{}
	run := newTurnRun(ctx, synth.dispatch, nil)

	heldCalled := false
	err := run.dispatchHeld(Reply{Sentence: "first"}, func(string) { heldCalled = true }, errors.New("abort"))
	if OutcomeOf(err) != SentenceCut {
		t.Fatalf("dispatchHeld on dead ctx = %v, want a cut", err)
	}
	if heldCalled {
		t.Fatal("a cut turn must not hand the coordinator a held sentence")
	}
	if len(synth.calls) != 0 {
		t.Fatal("a cut turn must never reach the synthesizer")
	}
}

func TestOutcomeOf_ThreeClassContract(t *testing.T) {
	if OutcomeOf(nil) != SentenceDelivered {
		t.Fatal("nil must classify as delivered")
	}
	if OutcomeOf(ErrNotDelivered) != SentenceNotDelivered {
		t.Fatal("ErrNotDelivered must classify as not-delivered")
	}
	if OutcomeOf(context.Canceled) != SentenceCut {
		t.Fatal("a ctx error must classify as cut")
	}
	if OutcomeOf(errors.New("anything else")) != SentenceCut {
		t.Fatal("an unknown dispatch error must classify as cut (stop the producer)")
	}
}
