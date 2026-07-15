package orchestrator

import (
	"context"
	"sync"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// BargeIn is the [Reactor] that yields the conversational floor when a human
// reclaims it while an Agent is speaking (ADR-0027). It subscribes to the VAD
// stage's speech transitions and, on a confirmed barge, calls [Floor.YieldTurn]
// — cancelling the Agent's turn — and publishes [voiceevent.BargeDetected].
//
// Trigger has two gates. First, the Agent must be AUDIBLY speaking: a barge can
// only fire once the held turn has produced its first audible frame
// ([voiceevent.FirstOpus] → [Floor.MarkSpeaking] → [Floor.SpeakingTurn]), never
// during its held-but-silent pre-audio LLM phase. Cancelling a silent turn was
// the no-NPC-response self-cancel — the addressing user's own continued/over-split
// speech, under the single shared VAD session, looked like a barge against a turn
// that had made no sound (ADR-0027). Gate 1 is re-checked when the confirm window
// EXPIRES, not only when it arms (#432): the window captures the speaking turn's
// id at arm time and only that same, still-speaking turn is cancellable at fire
// time — if the turn ended naturally inside the window and a new pre-audio turn
// took the floor, the expiry is a no-op instead of killing a turn the human never
// heard. Second, given a speaking Agent, floor-yielding waits for the speech to
// persist for confirmWindow continuous milliseconds, separating a genuine
// interruption from a sub-threshold backchannel ("mhm", a cough), which is left
// to run the normal transcription path and never cancels the Agent. A
// confirmWindow of 0 yields instantly once speech onsets over a speaking Agent —
// the simplest form, used to validate the async-turn plumbing before the window
// is tuned in.
//
// Soft-overlap is decided on VOICED duration, not on the segment boundary
// (#431): the production VAD leaves its speaking state only after a min-silence
// hangover LONGER than the confirm window, so the segment-final
// [voiceevent.VADSpeechEnd] can never beat the timer — disarming only on it
// would make every backchannel a barge. The reactor therefore disarms on the
// provisional [voiceevent.VADVoicingStopped] (the first sub-threshold frame,
// published as soon as the speaker actually falls silent) and re-arms on
// [voiceevent.VADVoicingResumed] (voicing picking back up inside the still-open
// utterance, which fires no fresh speech_start). VADSpeechEnd still disarms as a
// belt-and-braces fallback for VAD sources that emit no provisional transitions.
//
// Per ADR-0027 an Agent's own TTS never triggers a barge: only inbound
// participant audio is VAD'd, so every speech_start here is a human's.
//
// Per-speaker confirm windows (ADR-0050): the VAD speech transitions carry a
// SpeakerID (the Speaker Lane the transition came off), so a confirm window is
// armed and disarmed PER speaker — a speech_end from speaker B no longer disarms
// the window speaker A's interruption armed (the pre-lane caveat this fixes).
// Single-lane wiring runs one "" key, so the behaviour is identical to before
// lanes existed. The fired [voiceevent.BargeDetected] names the barging speaker.
type BargeIn struct {
	floor   *Floor
	confirm time.Duration

	mu sync.Mutex
	// pending maps SpeakerID → the channel closed to cancel that speaker's armed
	// confirm timer. A key is present only while that speaker's window is armed.
	pending map[string]chan struct{}
}

// NewBargeIn builds a barge-in reactor that yields floor after confirmWindow of
// continuous speech (0 = yield instantly on onset). floor must be non-nil.
func NewBargeIn(floor *Floor, confirmWindow time.Duration) *BargeIn {
	if floor == nil {
		panic("orchestrator.NewBargeIn: floor must not be nil")
	}
	return &BargeIn{floor: floor, confirm: confirmWindow, pending: make(map[string]chan struct{})}
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
	unsubStart := voiceevent.On(bus, func(e voiceevent.VADSpeechStart) {
		b.onVoicing(bus, e.SpeakerID)
	})
	unsubResumed := voiceevent.On(bus, func(e voiceevent.VADVoicingResumed) {
		// Voicing picked back up inside a still-open utterance (#431): the VAD
		// fires no fresh speech_start (the hangover never elapsed), so this is the
		// only onset a pause-and-keep-talking interruption produces. Treated
		// exactly like a speech_start: the window (re-)arms from now, so only
		// confirmWindow of CONTINUOUS voicing fires the barge.
		b.onVoicing(bus, e.SpeakerID)
	})
	unsubStopped := voiceevent.On(bus, func(e voiceevent.VADVoicingStopped) {
		// This speaker actually fell silent before their window elapsed: a
		// soft-overlap backchannel, no cancel (#431). This — not the segment-final
		// speech_end below, which the production hangover delays past any sane
		// confirm window — is the disarm that makes Soft-overlap reachable.
		b.disarm(e.SpeakerID)
	})
	unsubEnd := voiceevent.On(bus, func(e voiceevent.VADSpeechEnd) {
		// Belt-and-braces: a VAD source that publishes no provisional voicing
		// transitions still disarms at its segment boundary.
		b.disarm(e.SpeakerID)
	})

	return func() {
		unsubSpeaking()
		unsubStart()
		unsubResumed()
		unsubStopped()
		unsubEnd()
		b.disarmAll()
	}
}

// onVoicing is the shared onset path for a speech_start and an in-utterance
// voicing_resumed from speaker sp: only fight for the floor if an Agent is
// AUDIBLY speaking (ADR-0027) — otherwise this is the user's own utterance and
// the normal STT → AddressRouted → Floor.Take path (coalesce/supersede) handles
// it without cancelling a turn that has produced no audio. The speaking turn's
// id is captured HERE, so the eventual fire can only ever cancel this turn
// (#432), never whichever turn happens to hold the floor by then.
func (b *BargeIn) onVoicing(bus *voiceevent.Bus, sp string) {
	turnID, speaking := b.floor.SpeakingTurn()
	if !speaking {
		return
	}
	if b.confirm <= 0 {
		b.fire(bus, sp, turnID)
		return
	}
	b.arm(bus, sp, turnID)
}

// arm starts (or restarts) speaker sp's confirm-window timer against the
// speaking turn turnID. When it elapses without an intervening voicing_stopped /
// speech_end from the SAME speaker, the barge fires — against turnID only.
func (b *BargeIn) arm(bus *voiceevent.Bus, sp, turnID string) {
	done := make(chan struct{})
	b.mu.Lock()
	if prev := b.pending[sp]; prev != nil {
		close(prev) // supersede this speaker's still-armed window
	}
	b.pending[sp] = done
	b.mu.Unlock()

	go func() {
		t := time.NewTimer(b.confirm)
		defer t.Stop()
		select {
		case <-t.C:
			b.mu.Lock()
			// Only fire if still this speaker's armed window (not disarmed/superseded).
			current := b.pending[sp] == done
			if current {
				delete(b.pending, sp)
			}
			b.mu.Unlock()
			if current {
				b.fire(bus, sp, turnID)
			}
		case <-done:
		}
	}()
}

// disarm cancels speaker sp's pending confirm timer, if any. Another speaker's
// armed window is left untouched (ADR-0050).
func (b *BargeIn) disarm(sp string) {
	b.mu.Lock()
	if done := b.pending[sp]; done != nil {
		close(done)
		delete(b.pending, sp)
	}
	b.mu.Unlock()
}

// disarmAll cancels every pending confirm timer (teardown).
func (b *BargeIn) disarmAll() {
	b.mu.Lock()
	for sp, done := range b.pending {
		close(done)
		delete(b.pending, sp)
	}
	b.mu.Unlock()
}

// fire re-validates Gate 1 and yields the floor: [Floor.YieldTurn] cancels the
// turn ONLY if turnID — the turn that was audibly speaking when the window
// armed — still holds the floor and is still speaking (#432). A holder change
// inside the window (the turn ended naturally; a new, possibly still-silent
// turn took the floor) or an already-free floor makes the expiry a silent
// no-op: no cancel, no BargeDetected. On a real cut it announces the barge —
// the BargeDetected observability signal (ADR-0027) carrying the barging
// speaker (sp, ADR-0050) and a TurnEnded carrying the cut turn's TurnID + the
// barge reason, so the metrics subscriber attributes this turn's death to the
// barge rather than the coarse no-first-audio catch-all.
func (b *BargeIn) fire(bus *voiceevent.Bus, sp, turnID string) {
	if !b.floor.YieldTurn(turnID) {
		return
	}
	now := time.Now()
	bus.Publish(voiceevent.BargeDetected{At: now, SpeakerID: sp})
	bus.Publish(voiceevent.TurnEnded{At: now, TurnID: turnID, Reason: voiceevent.TurnEndBarge})
}
