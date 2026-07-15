// Package busproject is the shared scaffold under the process's Voice-Session
// bus projections (#447): the transcript Relay (ADR-0040, Line grain) and the
// Chunker (ADR-0011, Chunk grain) fold the same voiceevent.Bus into different
// records of the same speech, and everything around their fold rules is
// identical plumbing. The scaffold owns that plumbing exactly once:
//
//   - the subscribe-once-for-the-process subscription (single active Voice
//     Session across reconnects and sessions, ADR-0039);
//   - the one-Snapshot-per-event session rollover (#149): ONE Sessions.Snapshot
//     serves both the active-id comparison and the typed-FK capture, so a
//     Voice Session change between two reads can never mis-attribute the
//     triggering event to the previous session or to uuid.Nil;
//   - the per-TurnID coalescing state map: lazy creation on first sight,
//     wholesale reset at rollover;
//   - the async write queue ([Queue]): non-blocking tee, ONE writer goroutine,
//     drop-on-overflow, and the flush barrier that makes drain-at-Stop
//     authoritative.
//
// Hosts supply only their genuinely distinct parts: the fold rules (which
// events mutate which turn state and what gets emitted), the flush sink, and
// their session-finalize hooks. ADR-0040/ADR-0011 guardrail: this scaffold
// merges PLUMBING only — the Transcript Line and Transcript Chunk grains, their
// schemas, and their fold semantics stay separate and must not converge here.
//
// The observe StageSubscriber is deliberately NOT on this scaffold: it shares
// the subscription and turn-map coalescing but reaps by TTL sweep instead of
// Snapshot rollover, and forcing it on would bend the rollover contract.
package busproject

import (
	"sync"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// Sessions is the narrow read a projection needs from the SessionManager:
// which Voice Session (if any) is currently active. *session.Manager satisfies
// it via Snapshot; tests fake it. It is the same seam the hosts already expose
// — deliberately kept this thin (#447).
type Sessions interface {
	Snapshot() (storage.VoiceSession, bool)
}

// Hooks are the host's callbacks into the projection lifecycle. Every hook runs
// with the shared lock HELD (the bus delivers synchronously, so none of them
// may block or re-lock).
type Hooks struct {
	// Fold applies one attributed event to the host's projection state — the
	// genuinely distinct part of each projector (Relay: display text at
	// TTSInvoked per ADR-0040; Chunker: delivered-only at FirstAudio per
	// ADR-0012). Required.
	Fold func(e voiceevent.Event)
	// FinishSession closes out the OUTGOING session's state on a rollover,
	// BEFORE the active FKs repoint and the turn map resets: Session() still
	// returns the previous Voice Session, so a stale open chunk flushed here
	// keeps the OLD session's ids. Optional; also fires on the first rollover
	// (from the process-start idle state), where hosts have nothing to close.
	FinishSession func()
	// StartSession initializes the host's state for the newly-active session,
	// AFTER the FKs repoint and the turn map resets: Session() returns the new
	// Voice Session. Optional.
	StartSession func()
}

// Projection is the scaffold under one bus projector. It attributes every bus
// event to the active Voice Session under the host's own lock, rolls over on a
// session change, and owns the per-TurnID coalescing map. The type parameter T
// is the host's per-turn state; new turns are created as new(T).
//
// The lock is BORROWED from the host (mu), not owned: the host's fold state and
// the scaffold's session state form one consistency domain, and the host's
// other entry points (HTTP handlers, window timers, flush hooks) keep taking
// the same mutex they always did.
type Projection[T any] struct {
	sessions Sessions
	mu       sync.Locker
	hooks    Hooks

	// activeID is the active Voice Session id string ("" before the first
	// attributed event); active is the FULL Snapshot captured at rollover — the
	// SAME read that won the id comparison (#149), so the typed FKs can never
	// belong to a different session than activeID.
	activeID string
	active   storage.VoiceSession
	turns    map[string]*T
}

// New returns a Projection guarding its state with the host's mu and calling
// back through hooks. It does not subscribe; the host calls Subscribe once its
// own construction is complete.
func New[T any](sessions Sessions, mu sync.Locker, hooks Hooks) *Projection[T] {
	return &Projection[T]{
		sessions: sessions,
		mu:       mu,
		hooks:    hooks,
		turns:    map[string]*T{},
	}
}

// Subscribe registers the projection on bus once, for the life of the process:
// the same bus persists across reconnect cycles AND across sessions (single
// active Voice Session, ADR-0039), so the projector sees every event without
// re-subscribing. A nil bus subscribes nothing (event-less unit-test hosts).
func (p *Projection[T]) Subscribe(bus *voiceevent.Bus) {
	if bus == nil {
		return
	}
	bus.Subscribe(p.project)
}

// project is the bus callback: it attributes the event to the active Voice
// Session (rolling over on a session change) and folds it into the host's
// state. It must not block — the bus delivers synchronously.
func (p *Projection[T]) project(e voiceevent.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// ONE Snapshot serves both the id comparison and the rollover's FK capture
	// (#149): the manager's mutex is independent of mu, so a second read could
	// see the session already ended and leave the FKs pointing at the previous
	// session (or uuid.Nil at process start).
	vs, active := p.sessions.Snapshot()
	if !active {
		return // no active Voice Session — drop the event (ADR-0039)
	}
	if vs.ID.String() != p.activeID {
		if p.hooks.FinishSession != nil {
			p.hooks.FinishSession()
		}
		p.activeID = vs.ID.String()
		p.active = vs
		p.turns = map[string]*T{}
		if p.hooks.StartSession != nil {
			p.hooks.StartSession()
		}
	}
	p.hooks.Fold(e)
}

// Session returns the Voice Session captured by the rollover's Snapshot — the
// typed FKs (ID, CampaignID) persistence attributes rows to. The zero value
// before the first attributed event. Caller must hold the shared lock.
func (p *Projection[T]) Session() storage.VoiceSession {
	return p.active
}

// ActiveID returns the active Voice Session id string, "" before the first
// attributed event. Caller must hold the shared lock.
func (p *Projection[T]) ActiveID() string {
	return p.activeID
}

// Turn returns the coalescing state for a turn id, creating it on first sight.
// Caller must hold the shared lock.
func (p *Projection[T]) Turn(id string) *T {
	t := p.turns[id]
	if t == nil {
		t = new(T)
		p.turns[id] = t
	}
	return t
}

// Lookup returns the coalescing state for a turn id WITHOUT creating it — nil
// when the turn has never been seen (or the map has since rolled over). Caller
// must hold the shared lock.
func (p *Projection[T]) Lookup(id string) *T {
	return p.turns[id]
}
