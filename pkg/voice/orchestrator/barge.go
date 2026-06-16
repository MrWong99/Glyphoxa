package orchestrator

import (
	"context"
	"sync"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// BargeIn is the [Reactor] that yields the conversational floor when a human
// reclaims it while an Agent is speaking (ADR-0027). It subscribes to the VAD
// stage's speech transitions and, on a confirmed barge, calls [Floor.Yield] —
// cancelling the Agent's turn — and publishes [voiceevent.BargeDetected].
//
// Trigger has two gates. First, the Agent must be AUDIBLY speaking: a barge can
// only fire once the held turn has produced its first audible frame
// ([voiceevent.FirstOpus] → [Floor.MarkSpeaking] → [Floor.Speaking]), never
// during its held-but-silent pre-audio LLM phase. Cancelling a silent turn was
// the no-NPC-response self-cancel — the addressing user's own continued/over-split
// speech, under the single shared VAD session, looked like a barge against a turn
// that had made no sound (ADR-0027). Second, given a speaking Agent, floor-yielding
// waits for the speech to persist for confirmWindow continuous milliseconds,
// separating a genuine interruption from a sub-threshold backchannel ("mhm", a
// cough), which is left to run the normal transcription path and never cancels the
// Agent. A confirmWindow of 0 yields instantly once speech onsets over a speaking
// Agent — the simplest form, used to validate the async-turn plumbing before the
// window is tuned in.
//
// Per ADR-0027 an Agent's own TTS never triggers a barge: only inbound
// participant audio is VAD'd, so every speech_start here is a human's.
//
// Multi-speaker caveat: [voiceevent.VADSpeechStart]/[voiceevent.VADSpeechEnd]
// carry no speaker identity because the current wiring runs ONE VAD session
// over all participants' interleaved frames — the transitions describe the
// mix, not a person. The confirm window is therefore only meaningful with the
// single-active-speaker assumption of the MVP slice; do not tune
// confirmWindow > 0 for a multi-speaker table until per-participant VAD
// sessions (ADR-0019, deferred) attribute these events, or one speaker's
// pause can disarm a window another speaker's interruption armed.
type BargeIn struct {
	floor   *Floor
	confirm time.Duration

	mu      sync.Mutex
	pending chan struct{} // closed to cancel the armed confirm timer; nil when unarmed
}

// NewBargeIn builds a barge-in reactor that yields floor after confirmWindow of
// continuous speech (0 = yield instantly on onset). floor must be non-nil.
func NewBargeIn(floor *Floor, confirmWindow time.Duration) *BargeIn {
	if floor == nil {
		panic("orchestrator.NewBargeIn: floor must not be nil")
	}
	return &BargeIn{floor: floor, confirm: confirmWindow}
}

// Bind subscribes the reactor to the VAD speech transitions on bus and returns a
// function that removes the subscriptions and disarms any pending timer. It
// implements [Reactor]; bus must be non-nil. The speech_start callback never
// blocks: the confirm window is timed on its own goroutine so the synchronous
// bus fan-out is not held up.
//
// It also subscribes to [voiceevent.FirstOpus] — the audible-on-wire signal — to
// drive [Floor.MarkSpeaking]: a turn counts as speaking (and so barge-able) only
// once its first Opus packet reaches Discord, not while it merely holds the floor
// during its pre-audio LLM phase. This is what stops the addressing user's own
// continued speech (single shared VAD session, no speaker identity) from
// self-cancelling the turn it just triggered, before the Agent has made a sound.
func (b *BargeIn) Bind(_ context.Context, bus *voiceevent.Bus) (cancel func()) {
	if bus == nil {
		panic("orchestrator.BargeIn.Bind: bus must not be nil")
	}

	unsubSpeaking := voiceevent.On(bus, func(e voiceevent.FirstOpus) {
		// The holder is now audible on the wire: arm the barge for this turn.
		b.floor.MarkSpeaking(e.TurnID)
	})
	unsubStart := voiceevent.On(bus, func(voiceevent.VADSpeechStart) {
		// Only fight for the floor if an Agent is AUDIBLY speaking (ADR-0027);
		// otherwise this is the user's own utterance — the onset of a new turn, or
		// the continuation of the one that holds the still-silent floor — and the
		// normal STT → AddressRouted → Floor.Take path (coalesce/supersede) handles
		// it without cancelling a turn that has produced no audio.
		if !b.floor.Speaking() {
			return
		}
		if b.confirm <= 0 {
			b.fire(bus)
			return
		}
		b.arm(bus)
	})
	unsubEnd := voiceevent.On(bus, func(voiceevent.VADSpeechEnd) {
		b.disarm() // speech ended before the window: soft overlap, no cancel
	})

	return func() {
		unsubSpeaking()
		unsubStart()
		unsubEnd()
		b.disarm()
	}
}

// arm starts (or restarts) the confirm-window timer. When it elapses without an
// intervening speech_end, the barge fires.
func (b *BargeIn) arm(bus *voiceevent.Bus) {
	done := make(chan struct{})
	b.mu.Lock()
	if b.pending != nil {
		close(b.pending) // supersede a still-armed window
	}
	b.pending = done
	b.mu.Unlock()

	go func() {
		t := time.NewTimer(b.confirm)
		defer t.Stop()
		select {
		case <-t.C:
			b.mu.Lock()
			// Only fire if still the armed window (not disarmed/superseded).
			current := b.pending == done
			if current {
				b.pending = nil
			}
			b.mu.Unlock()
			if current {
				b.fire(bus)
			}
		case <-done:
		}
	}()
}

// disarm cancels a pending confirm timer, if any.
func (b *BargeIn) disarm() {
	b.mu.Lock()
	if b.pending != nil {
		close(b.pending)
		b.pending = nil
	}
	b.mu.Unlock()
}

// fire yields the floor and, if a turn was actually cancelled, announces it: the
// BargeDetected observability signal (ADR-0027) and a TurnEnded carrying the cut
// turn's TurnID + the barge reason, so the metrics subscriber attributes this
// turn's death to the barge rather than the coarse no-first-audio catch-all.
func (b *BargeIn) fire(bus *voiceevent.Bus) {
	turnID, yielded := b.floor.Yield()
	if !yielded {
		return
	}
	now := time.Now()
	bus.Publish(voiceevent.BargeDetected{At: now})
	bus.Publish(voiceevent.TurnEnded{At: now, TurnID: turnID, Reason: voiceevent.TurnEndBarge})
}
