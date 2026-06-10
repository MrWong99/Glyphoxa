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
// is treated as the SAME utterance continuing: it does not supersede the
// in-flight turn but yields to it — the new turn's context comes back already
// cancelled so its reply is suppressed and the turn already speaking keeps the
// floor. One utterance then maps to one turn even when VAD over-splits it. A zero
// window (the [NewFloor] default) keeps the plain always-supersede behaviour the
// barge path and the tracer-bullet tests rely on.
type Floor struct {
	mu         sync.Mutex
	cancel     context.CancelFunc // non-nil while a turn holds the floor
	gen        uint64             // increments per Take; guards stale releases
	lastTake   time.Time          // when the current holder took the floor (coalesce anchor)
	holderTurn string             // TurnID of the turn currently holding the floor (for Yield → barge attribution)

	// coalesce is the same-utterance debounce window; 0 disables it (plain
	// supersession). now is the clock, overridable in tests.
	coalesce time.Duration
	now      func() time.Time
}

// NewFloor returns an unheld floor with no coalesce window: every [Floor.Take]
// supersedes the prior turn (the original behaviour).
func NewFloor() *Floor { return &Floor{now: time.Now} }

// NewFloorWithCoalesce returns an unheld floor whose [Floor.Take] coalesces a
// re-take arriving within window of the previous take into the turn already
// holding the floor, rather than superseding it (see [Floor] — root cause #2).
// A non-positive window behaves like [NewFloor].
func NewFloorWithCoalesce(window time.Duration) *Floor {
	if window < 0 {
		window = 0
	}
	return &Floor{coalesce: window, now: time.Now}
}

// Take derives a per-turn context from parent and installs it as the held floor,
// returning that context and a release function. A new Take supersedes any turn
// still holding the floor — its context is cancelled — so two turns never speak
// at once. release clears the floor (only if this turn still holds it) and
// cancels the turn's context; it is idempotent and must be called when the turn
// ends, conventionally via defer.
//
// With a coalesce window ([NewFloorWithCoalesce]) a Take landing within that
// window of the previous one is a split-utterance continuation: it does NOT
// cancel the in-flight turn. The returned context comes back already cancelled,
// the returned release is a no-op on the floor, and coalesced is true — so the
// caller can see this Take yielded (rather than took) the floor and react (e.g.
// publish [voiceevent.TurnEnded] for the dropped segment) instead of speaking
// it, while the turn already holding the floor keeps it. On a normal take
// coalesced is false.
//
// The turn's TurnID is recovered from parent ([voiceevent.TurnIDFrom]) and held
// so [Floor.Yield] can attribute a barge to the turn it cancelled.
func (f *Floor) Take(parent context.Context) (ctx context.Context, release func(), coalesced bool) {
	ctx, cancel := context.WithCancel(parent)

	f.mu.Lock()
	if f.cancel != nil && f.coalesce > 0 && f.now().Sub(f.lastTake) < f.coalesce {
		// Same-utterance re-take inside the coalesce window: yield to the turn
		// already holding the floor instead of superseding it. Cancel only THIS
		// (the late segment's) context and leave the holder untouched. Refresh the
		// anchor so a run of closely-spaced splits keeps coalescing (each segment
		// is within the window of the previous one, not just the first).
		f.lastTake = f.now()
		f.mu.Unlock()
		cancel()
		return ctx, func() {}, true // no-op release: this turn never held the floor
	}
	if f.cancel != nil {
		f.cancel() // supersede a turn that is still unwinding
	}
	f.gen++
	gen := f.gen
	f.cancel = cancel
	f.lastTake = f.now()
	f.holderTurn = voiceevent.TurnIDFrom(parent)
	f.mu.Unlock()

	release = func() {
		f.mu.Lock()
		// Only clear if this turn still holds the floor: a later Take (or a
		// Yield) may already have moved on, and a stale release must not wipe a
		// newer turn's cancel.
		if f.gen == gen {
			f.cancel = nil
		}
		f.mu.Unlock()
		cancel()
	}
	return ctx, release, false
}

// Yield cancels the turn currently holding the floor and reports whether one was
// held, along with that turn's TurnID. It is the barge-in action: yielded=true
// means an Agent was actually speaking and has now been cut (and turnID is the
// turn that was cut, so the caller can attribute the barge); yielded=false means
// the floor was free, so nothing was interrupted (turnID is empty and no
// BargeDetected/TurnEnded should be emitted).
func (f *Floor) Yield() (turnID string, yielded bool) {
	f.mu.Lock()
	c := f.cancel
	turnID = f.holderTurn
	f.cancel = nil
	f.holderTurn = ""
	f.mu.Unlock()
	if c == nil {
		return "", false
	}
	c()
	return turnID, true
}

// Active reports whether a turn currently holds the floor (an Agent is
// speaking). It is a point-in-time read; callers must tolerate the floor being
// taken or yielded immediately afterward.
func (f *Floor) Active() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancel != nil
}
