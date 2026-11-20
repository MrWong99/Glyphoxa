package orchestrator

import (
	"context"
	"sync"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// Floor is the single conversational floor an Agent turn holds while it speaks.
// It is the shared seam between the [Replier] (which takes the floor for the
// duration of a reply) and the [BargeIn] reactor (which yields it when a human
// reclaims it). Holding the floor means owning a per-turn [context.Context];
// yielding cancels that context, which — because the same context threads
// through TTS synthesis and the wire playback pump — tears down synthesis and
// playback together (ADR-0027's hard cut at the forward boundary).
//
// Floor is safe for concurrent use: a turn is taken on one goroutine and may be
// yielded from another (the inbound VAD goroutine).
//
// Coalesce window (root cause #2 of the latency investigation): a turn's unit is
// a VAD segment, not a user utterance, so one spoken utterance VAD-split into two
// segments produces two [Replier] dispatches and two [Floor.Take]s — and the
// second Take's supersession cancels the first segment's turn mid-synthesis (a
// self-cancel with no barge involved). When a coalesce window is configured
// ([NewFloorWithCoalesce]) a Take arriving within that window of the previous one
// AND routed to the same target agent is treated as the SAME utterance
// continuing: it does not supersede the in-flight turn but yields to it — the
// new turn's context comes back already cancelled so its reply is suppressed and
// the turn already speaking keeps the floor. One utterance then maps to one turn
// even when VAD over-splits it. A take for a DIFFERENT agent inside the window
// is not the same utterance continuing — the matcher routed it to someone else
// ("Bart, hold the door. Greta, run!", #146) — so it supersedes as normal. A
// zero window (the [NewFloor] default) keeps the plain always-supersede
// behaviour the barge path and the tracer-bullet tests rely on.
type Floor struct {
	mu          sync.Mutex
	cancel      context.CancelFunc // non-nil while a turn holds the floor
	gen         uint64             // increments per Take; guards stale releases
	lastTake    time.Time          // when the current holder took the floor (coalesce anchor)
	holderTurn  string             // TurnID of the turn currently holding the floor (for Yield → barge attribution)
	holderAgent string             // target AgentID of the current holder's route (coalesce is same-target only, #146)
	// speaking is true once the current holder has produced its first audible
	// frame — the barge gate (ADR-0027). A held-but-silent turn (the pre-audio
	// LLM "thinking" phase) is NOT speaking, so a human speech_start in that
	// window is not a barge: the agent has made no sound to interrupt. Cleared on
	// every Take (a new holder starts silent) and on Yield; set by [Floor.MarkSpeaking].
	speaking bool

	// coalesce is the same-utterance debounce window; 0 disables it (plain
	// supersession). now is the clock, overridable in tests.
	coalesce time.Duration
	now      func() time.Time

	// onSuperseded, when non-nil, is called (outside f.mu) with the CUT turn's
	// TurnID after a [Floor.Take] superseded a live holder (#443) — the Take is
	// the only seam that knows the cause, so it publishes the cut turn's
	// terminal. Installed by [Floor.bindSupersedeTerminal] from the floor-sharing
	// reactors' Bind. The barge/mute paths ([Floor.YieldTurn], [Floor.YieldAgent]) clear the
	// holder before any new Take, and a released (completed) turn cleared its
	// cancel, so this fires ONLY for a genuine mid-flight supersession — never a
	// second terminal for a turn that already got one from its cutter.
	onSuperseded func(cutTurnID string)
}

// clock returns the floor's time source, defaulting to [time.Now] so a
// zero-value Floor{} (no constructor) does not panic on a nil now in [Floor.Take]
// — the constructors set it, but a bare Floor{} is otherwise fully usable, so the
// clock must be too. Called under f.mu.
func (f *Floor) clock() time.Time {
	if f.now == nil {
		return time.Now()
	}
	return f.now()
}

// NewFloor returns an unheld floor with no coalesce window: every [Floor.Take]
// supersedes the prior turn (the original behaviour).
func NewFloor() *Floor { return &Floor{now: time.Now} }

// bindSupersedeTerminal installs the supersede terminal publisher (#443): a
// [Floor.Take] that cancels a live holder publishes a
// [voiceevent.TurnEnded]{superseded} for the CUT turn on bus, so downstream
// consumers (observe subscriber, transcript relay, chunker) fold the cut turn's
// state on a terminal event instead of a TTL sweep. Called from every
// floor-sharing reactor's Bind (Replier, DirectSpeech, ClipReplay) — they all
// bind the same bus in one Conversation, so re-installation is idempotent.
func (f *Floor) bindSupersedeTerminal(bus *voiceevent.Bus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSuperseded = func(cutTurnID string) {
		bus.Publish(voiceevent.TurnEnded{At: time.Now(), TurnID: cutTurnID, Reason: voiceevent.TurnEndSuperseded})
	}
}

// NewFloorWithCoalesce returns an unheld floor whose [Floor.Take] coalesces a
// same-target re-take arriving within window of the previous take into the turn
// already holding the floor, rather than superseding it (see [Floor] — root
// cause #2). A non-positive window behaves like [NewFloor].
func NewFloorWithCoalesce(window time.Duration) *Floor {
	if window < 0 {
		window = 0
	}
	return &Floor{coalesce: window, now: time.Now}
}

// Take derives a per-turn context from parent and installs it as the held floor,
// returning that context and a release function. agentID is the target agent of
// the route this turn answers ([voiceevent.AddressTarget.AgentID]); it decides
// whom a coalesce window may fold this take into. A new Take supersedes any turn
// still holding the floor — its context is cancelled — so two turns never speak
// at once. release clears the floor (only if this turn still holds it) and
// cancels the turn's context; it is idempotent and must be called when the turn
// ends, conventionally via defer.
//
// With a coalesce window ([NewFloorWithCoalesce]) a Take landing within that
// window of the previous one AND addressing the holder's agent is a
// split-utterance continuation: it does NOT cancel the in-flight turn. The
// returned context comes back already cancelled, the returned release is a
// no-op on the floor, and coalesced is true — so the caller can see this Take
// yielded (rather than took) the floor and react (e.g. publish
// [voiceevent.TurnEnded] for the dropped segment) instead of speaking it, while
// the turn already holding the floor keeps it. A take for a DIFFERENT agent
// inside the window supersedes as normal (#146): "same utterance continuing" is
// provably false once the matcher routed the segment to another agent, and
// coalescing it away would silently drop a direct address. On a normal take
// coalesced is false.
//
// The turn's TurnID is recovered from parent ([voiceevent.TurnIDFrom]) and held
// so [Floor.YieldTurn] can attribute a barge to the turn it cancelled.
func (f *Floor) Take(parent context.Context, agentID string) (ctx context.Context, release func(), coalesced bool) {
	ctx, rel, coalesced := f.take(parent, agentID)
	return ctx, func() { rel() }, coalesced
}

// take is [Floor.Take] with a release that reports whether this turn still held
// the floor at release time — i.e. no Yield (barge/mute) and no superseding Take
// cut it. The check and the clear happen in ONE critical section, so a turn's
// natural-completion terminal can be published exactly when no cutter will
// publish one: once released, a later Yield finds the floor free and reports
// nothing (W1 — every turn ends with exactly one TurnEnded).
func (f *Floor) take(parent context.Context, agentID string) (ctx context.Context, release func() (held bool), coalesced bool) {
	ctx, cancel := context.WithCancel(parent)

	f.mu.Lock()
	if f.cancel != nil && f.coalesce > 0 && agentID == f.holderAgent && f.clock().Sub(f.lastTake) < f.coalesce {
		// Same-utterance re-take inside the coalesce window, addressed to the same
		// agent: yield to the turn already holding the floor instead of superseding
		// it. Cancel only THIS (the late segment's) context and leave the holder
		// untouched. Refresh the anchor so a run of closely-spaced splits keeps
		// coalescing (each segment is within the window of the previous one, not
		// just the first).
		f.lastTake = f.clock()
		f.mu.Unlock()
		cancel()
		return ctx, func() bool { return false }, true // no-op release: this turn never held the floor
	}
	var superseded string
	var notify func(string)
	if f.cancel != nil {
		f.cancel() // supersede a turn that is still unwinding
		// Capture the cut turn's id + the terminal publisher (#443): every turn
		// that took the floor terminates with exactly one TurnEnded, and a
		// supersede cut previously published none — leaving consumers to TTL-reap
		// the cut turn's state and the outcome metrics missing the class.
		superseded = f.holderTurn
		notify = f.onSuperseded
	}
	f.gen++
	gen := f.gen
	f.cancel = cancel
	f.lastTake = f.clock()
	f.holderTurn = voiceevent.TurnIDFrom(parent)
	f.holderAgent = agentID
	f.speaking = false // a fresh holder starts silent until it produces audio
	f.mu.Unlock()

	// Publish the cut turn's terminal OUTSIDE f.mu (the bus fans out
	// synchronously and subscribers may read the floor). An id-less holder (a
	// parent that carried no TurnID) has nothing to attribute the terminal to.
	if notify != nil && superseded != "" {
		notify(superseded)
	}

	release = func() (held bool) {
		f.mu.Lock()
		// Only clear if this turn still holds the floor: a later Take (or a
		// Yield) may already have moved on, and a stale release must not wipe a
		// newer turn's cancel. A Yield nils f.cancel without bumping gen, so both
		// halves are needed to say "nobody cut this turn".
		if f.gen == gen && f.cancel != nil {
			f.cancel = nil
			held = true
		}
		f.mu.Unlock()
		cancel()
		return held
	}
	return ctx, release, false
}

// YieldTurn cancels the turn holding the floor ONLY when turnID is that turn
// AND it is audibly speaking ([Floor.Speaking]), reporting whether it did. It
// is the barge fire path's Gate-1 re-check (#432, ADR-0027): the confirm
// window captures the speaking holder's TurnID when it arms
// ([Floor.SpeakingTurn]), and when the timer expires this re-validates that
// the SAME turn still holds the floor and is still speaking. A holder change
// inside the window — the speaking turn ended naturally and a new, not-yet-
// audible turn took the floor — makes this a no-op: the human's overlapping
// speech was aimed at a turn that no longer exists, and cancelling the new
// turn would be exactly the pre-audio self-cancel Gate 1 exists to prevent
// (`no_audio` turns must not be cancellable). An empty floor is likewise a
// no-op. Once the guard passes it clears the holder and cancels its ctx.
// It reports the AgentID of the holder it cut, read in the same critical section
// as the yield itself so the attribution can never be stale. It is "" on a no-op
// yield and on a holder taken with no target.
func (f *Floor) YieldTurn(turnID string) (agentID string, yielded bool) {
	f.mu.Lock()
	if f.cancel == nil || f.holderTurn != turnID || !f.speaking {
		f.mu.Unlock()
		return "", false
	}
	c := f.cancel
	cut := f.holderAgent
	f.cancel = nil
	f.holderTurn = ""
	f.holderAgent = ""
	f.speaking = false
	f.mu.Unlock()
	c()
	return cut, true
}

// SpeakingTurn reports the TurnID of the floor's current holder when — and
// only when — that holder is audibly speaking ([Floor.Speaking]). It is the
// arm-time Gate-1 read of the [BargeIn] confirm window (#432): the captured
// id is what [Floor.YieldTurn] re-validates at window expiry, so a barge can
// only ever cancel the turn the human was actually talking over. speaking is
// false when the floor is free or the holder is still in its silent pre-audio
// phase (turnID is then empty). Point-in-time, like [Floor.Speaking].
func (f *Floor) SpeakingTurn() (turnID string, speaking bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancel == nil || !f.speaking {
		return "", false
	}
	return f.holderTurn, true
}

// YieldAgent cancels the turn holding the floor ONLY when agentID is that turn's
// target Agent, reporting the cut turn's TurnID and yielded=true; otherwise it is
// a no-op reporting ("", false) and the floor is left untouched. It is the
// per-Agent mute cut (#211): muting the Agent that is speaking cuts it, while
// muting anyone else never disturbs whoever holds the floor (AC3).
//
// Unlike the barge gate ([Floor.Speaking]) this deliberately ignores f.speaking:
// a mute is a deliberate GM action that kills a held-but-silent pre-audio turn
// too (the LLM "thinking" phase), so a just-muted Agent never starts speaking
// after the fact (AC2). Mechanically identical to [Floor.YieldTurn] once the holder
// matches — the same forward-boundary hard cut (ADR-0027) — differing only in the
// holder-match guard.
func (f *Floor) YieldAgent(agentID string) (turnID string, yielded bool) {
	f.mu.Lock()
	if f.cancel == nil || f.holderAgent != agentID {
		f.mu.Unlock()
		return "", false
	}
	c := f.cancel
	turnID = f.holderTurn
	f.cancel = nil
	f.holderTurn = ""
	f.holderAgent = ""
	f.speaking = false
	f.mu.Unlock()
	c()
	return turnID, true
}

// MarkSpeaking records that the turn identified by turnID has produced its first
// audible frame, so the floor's current holder counts as audibly speaking for the
// barge gate ([Floor.Speaking]). It is driven by the audible-on-wire signal
// ([voiceevent.FirstOpus]) the [BargeIn] reactor subscribes to. turnID must match
// the current holder: a late signal from an already-superseded/yielded turn (its
// id no longer the holder) is ignored, so it cannot mark a newer, still-silent
// holder as speaking. A no-op when the floor is free or the id does not match.
func (f *Floor) MarkSpeaking(turnID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancel != nil && f.holderTurn == turnID {
		f.speaking = true
	}
}

// Speaking reports whether the floor's current holder is audibly speaking — it
// holds the floor AND has produced its first audible frame ([Floor.MarkSpeaking]).
// This is the barge gate (ADR-0027): a human speech_start is a barge only while
// the Agent is actually speaking, not during the held-but-silent pre-audio LLM
// phase (where the speech is the addressing user's own continued/over-split
// utterance, not an interruption). A point-in-time read.
func (f *Floor) Speaking() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancel != nil && f.speaking
}
