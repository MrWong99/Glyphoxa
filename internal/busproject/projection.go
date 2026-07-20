// Package busproject is the shared scaffold under the process's Voice-Session
// bus projections (#447): the transcript Relay (ADR-0040, Line grain) and the
// Chunker (ADR-0011, Chunk grain) fold the same voiceevent.Bus into different
// records of the same speech, and everything around their fold rules is
// identical plumbing. The scaffold owns that plumbing exactly once:
//
//   - the subscribe-once-for-the-process subscription;
//   - the per-event session attribution (#487): each event on the PROCESS bus
//     carries a stamped SessionID (voiceevent.SessionIDOf), so the scaffold folds
//     it into that session's OWN state — many Voice Sessions coexist with no
//     cross-talk. One [Sessions.Resolve] per newly-seen session captures its
//     typed FKs; an event with no SessionID, an unparsable one, or one for a
//     session the registry no longer knows (a straggler or a pre-registry event)
//     is dropped;
//   - the per-session, per-TurnID coalescing state map: lazy creation on first
//     sight, torn down wholesale when the session Closes;
//   - the async write queue ([Queue]): non-blocking tee, ONE writer goroutine,
//     drop-on-overflow, and the flush barrier that makes drain-at-Stop
//     authoritative.
//
// Hosts supply only their genuinely distinct parts: the fold rules (which
// events mutate which turn state and what gets emitted), the flush sink, and
// their per-session Start/Finish hooks. ADR-0040/ADR-0011 guardrail: this
// scaffold merges PLUMBING only — the Transcript Line and Transcript Chunk
// grains, their schemas, and their fold semantics stay separate and must not
// converge here.
//
// Session END is EXPLICIT (#487): the scaffold never infers a session ended from
// observing a new session's id — under concurrency that inference is simply
// wrong. The host's session finalizer (the Manager's guaranteed loop-exit hook,
// relay.Finalize / chunker.FlushSession) calls [Projection.Close] for exactly the
// session that ended; there is no defensive rollover sweep.
//
// The observe StageSubscriber is deliberately NOT on this scaffold: it shares
// the subscription and turn-map coalescing but reaps by TTL sweep instead of
// session lifecycle, and forcing it on would bend the Close contract.
package busproject

import (
	"log/slog"
	"sync"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// Sessions is the narrow read a projection needs: resolve a stamped event's
// SessionID to its full Voice Session (hence its typed Campaign FKs). *session.Registry
// satisfies it via Resolve; tests fake it. It replaces the old single-active
// Snapshot() seam (#487) — the scaffold resolves the specific session each event
// names, not one global active session.
type Sessions interface {
	Resolve(sessionID uuid.UUID) (storage.VoiceSession, bool)
}

// Hooks are the host's callbacks into the projection lifecycle. Every hook runs
// with the shared lock HELD (the bus delivers synchronously, so none of them may
// block or re-lock). The sessionID passed to Start/Finish is the same string key
// [Projection.Session], [Projection.Turn] and [Projection.Close] use, so the
// host keys its own per-session state off it.
type Hooks struct {
	// Fold applies one attributed event to the host's projection state — the
	// genuinely distinct part of each projector (Relay: display text at
	// TTSInvoked per ADR-0040; Chunker: delivered-only at FirstAudio per
	// ADR-0012). The host reads the event's session via voiceevent.SessionIDOf
	// and looks its state up with Session/Turn. Required.
	Fold func(e voiceevent.Event)
	// StartSession initializes the host's state for a newly-seen session, right
	// after the scaffold created its entry (Session(sessionID) already returns the
	// resolved FKs). Optional.
	StartSession func(sessionID string)
	// FinishSession closes out a session's state on [Projection.Close], BEFORE the
	// scaffold deletes the entry — so Session(sessionID) still returns the ending
	// session's FKs and a stale open chunk flushed here keeps them. Optional.
	FinishSession func(sessionID string)
}

// entry is one session's scaffold state: its captured Voice Session (the typed
// FKs persistence attributes rows to) and its per-TurnID coalescing map.
type entry[T any] struct {
	vs    storage.VoiceSession
	turns map[string]*T
}

// Projection is the scaffold under one bus projector. It attributes every bus
// event to its stamped Voice Session under the host's own lock and owns each
// session's per-TurnID coalescing map. The type parameter T is the host's
// per-turn state; new turns are created as new(T).
//
// The lock is BORROWED from the host (mu), not owned: the host's fold state and
// the scaffold's session state form one consistency domain, and the host's
// other entry points (HTTP handlers, window timers, flush hooks) keep taking the
// same mutex.
type Projection[T any] struct {
	sessions Sessions
	mu       sync.Locker
	hooks    Hooks
	log      *slog.Logger

	entries map[string]*entry[T] // keyed by session id string
	// closed tombstones recently Closed session ids (#487): a straggler event for
	// a Closed session — arriving in the window before the Manager cuts the bridge —
	// is dropped here WITHOUT re-Resolving, so it can never re-create a fresh entry
	// (a live frame after the terminal idle, #144) even if the registry still
	// resolves it. Session ids are unique uuids and never reused, so a tombstone is
	// never a false drop of a genuinely new session. The set is bounded (#483):
	// closedOrder evicts the OLDEST tombstone once maxClosedTombstones is exceeded —
	// the straggler window is the seconds-scale bridge-cut delay, so a tombstone
	// evicted maxClosedTombstones closes later is far past any straggler.
	closed      map[string]struct{}
	closedOrder []string // FIFO eviction order for the tombstone bound
}

// maxClosedTombstones bounds the closed set (#483): without it the map grows one
// uuid string per Closed session for the process lifetime (two Projection hosts —
// relay + chunker — each carry one). 1024 is orders of magnitude more closes than
// any straggler window (the bridge-cut delay, seconds) could span.
const maxClosedTombstones = 1024

// New returns a Projection guarding its state with the host's mu and calling
// back through hooks. A nil log discards. It does not subscribe; the host calls
// Subscribe once its own construction is complete.
func New[T any](sessions Sessions, mu sync.Locker, log *slog.Logger, hooks Hooks) *Projection[T] {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Projection[T]{
		sessions: sessions,
		mu:       mu,
		hooks:    hooks,
		log:      log,
		entries:  map[string]*entry[T]{},
		closed:   map[string]struct{}{},
	}
}

// Subscribe registers the projection on bus once, for the life of the process.
// A nil bus subscribes nothing (event-less unit-test hosts).
func (p *Projection[T]) Subscribe(bus *voiceevent.Bus) {
	if bus == nil {
		return
	}
	bus.Subscribe(p.project)
}

// project is the bus callback: it attributes the event to its stamped Voice
// Session and folds it into that session's state, creating the session's entry
// (one Resolve) on first sight. It must not block — the bus delivers
// synchronously. Events the scaffold cannot attribute are dropped:
//   - no SessionID (a session-local event that never crossed voiceevent.Forward);
//   - an unparsable SessionID (never expected — a stamped id is a uuid string);
//   - a SessionID the registry no longer resolves (a straggler after the session
//     ended, or a pre-registry event).
func (p *Projection[T]) project(e voiceevent.Event) {
	sid := voiceevent.SessionIDOf(e)
	if sid == "" {
		p.log.Debug("busproject: dropping event with no session id", "event", e.EventName())
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if _, gone := p.closed[sid]; gone {
		// A straggler for a session already Closed: drop it without re-Resolving, so
		// it can never resurrect a torn-down entry (#487/#144).
		p.log.Debug("busproject: dropping straggler event for a closed session", "event", e.EventName(), "session", sid)
		return
	}

	if _, ok := p.entries[sid]; !ok {
		uid, err := uuid.Parse(sid)
		if err != nil {
			p.log.Debug("busproject: dropping event with unparsable session id", "event", e.EventName(), "session", sid)
			return
		}
		vs, ok := p.sessions.Resolve(uid)
		if !ok {
			// Straggler (session ended) or pre-registry: nothing to attribute it to.
			p.log.Debug("busproject: dropping event for unresolved session", "event", e.EventName(), "session", sid)
			return
		}
		p.entries[sid] = &entry[T]{vs: vs, turns: map[string]*T{}}
		if p.hooks.StartSession != nil {
			p.hooks.StartSession(sid)
		}
	}
	p.hooks.Fold(e)
}

// Close tears down a session's scaffold state — the host's finalizer calls it
// for exactly the session that ended (there is no rollover inference, #487). It
// runs FinishSession first (Session(sessionID) still returns the ending
// session's FKs, so a stale flush there keeps them), then deletes the entry.
// Closing an unknown session (already closed, or one that folded zero events) is
// a no-op. Caller must NOT hold the shared lock — Close takes it.
func (p *Projection[T]) Close(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Tombstone the session so any straggler racing this Close is dropped, whether
	// or not the session folded events (a Close for a zero-event session still
	// arms the guard) — #487/#144. Bounded FIFO (#483): a repeat Close re-arms
	// nothing (already tombstoned), and past the cap the oldest tombstone —
	// long outside any straggler window — is evicted.
	if _, ok := p.closed[sessionID]; !ok {
		p.closed[sessionID] = struct{}{}
		p.closedOrder = append(p.closedOrder, sessionID)
		if len(p.closedOrder) > maxClosedTombstones {
			delete(p.closed, p.closedOrder[0])
			p.closedOrder = p.closedOrder[1:]
		}
	}
	if _, ok := p.entries[sessionID]; !ok {
		return
	}
	if p.hooks.FinishSession != nil {
		p.hooks.FinishSession(sessionID)
	}
	delete(p.entries, sessionID)
}

// Has reports whether the session currently has scaffold state — folded at least
// one event and not yet Closed. The host's HTTP reads use it as the "live"
// predicate. Caller must hold the shared lock.
func (p *Projection[T]) Has(sessionID string) bool {
	_, ok := p.entries[sessionID]
	return ok
}

// Session returns the Voice Session captured for sessionID — the typed FKs
// persistence attributes rows to — or the zero value when the session has no
// entry. Caller must hold the shared lock.
func (p *Projection[T]) Session(sessionID string) storage.VoiceSession {
	if ent := p.entries[sessionID]; ent != nil {
		return ent.vs
	}
	return storage.VoiceSession{}
}

// Turn returns the coalescing state for (sessionID, turnID), creating it on
// first sight within a KNOWN session. A turn for a session with no entry returns
// nil (the caller only reaches Turn from Fold, where the entry exists). Caller
// must hold the shared lock.
func (p *Projection[T]) Turn(sessionID, turnID string) *T {
	ent := p.entries[sessionID]
	if ent == nil {
		return nil
	}
	t := ent.turns[turnID]
	if t == nil {
		t = new(T)
		ent.turns[turnID] = t
	}
	return t
}

// Lookup returns the coalescing state for (sessionID, turnID) WITHOUT creating
// it — nil when the turn (or its session) has never been seen. Caller must hold
// the shared lock.
func (p *Projection[T]) Lookup(sessionID, turnID string) *T {
	ent := p.entries[sessionID]
	if ent == nil {
		return nil
	}
	return ent.turns[turnID]
}
