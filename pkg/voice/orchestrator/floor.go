package orchestrator

import (
	"context"
	"sync"
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
type Floor struct {
	mu     sync.Mutex
	cancel context.CancelFunc // non-nil while a turn holds the floor
	gen    uint64             // increments per Take; guards stale releases
}

// NewFloor returns an unheld floor.
func NewFloor() *Floor { return &Floor{} }

// Take derives a per-turn context from parent and installs it as the held floor,
// returning that context and a release function. A new Take supersedes any turn
// still holding the floor — its context is cancelled — so two turns never speak
// at once. release clears the floor (only if this turn still holds it) and
// cancels the turn's context; it is idempotent and must be called when the turn
// ends, conventionally via defer.
func (f *Floor) Take(parent context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)

	f.mu.Lock()
	if f.cancel != nil {
		f.cancel() // supersede a turn that is still unwinding
	}
	f.gen++
	gen := f.gen
	f.cancel = cancel
	f.mu.Unlock()

	release := func() {
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
	return ctx, release
}

// Yield cancels the turn currently holding the floor and reports whether one was
// held. It is the barge-in action: a true result means an Agent was actually
// speaking and has now been cut; false means the floor was free, so nothing was
// interrupted (and no BargeDetected should be emitted).
func (f *Floor) Yield() bool {
	f.mu.Lock()
	c := f.cancel
	f.cancel = nil
	f.mu.Unlock()
	if c == nil {
		return false
	}
	c()
	return true
}

// Active reports whether a turn currently holds the floor (an Agent is
// speaking). It is a point-in-time read; callers must tolerate the floor being
// taken or yielded immediately afterward.
func (f *Floor) Active() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancel != nil
}
