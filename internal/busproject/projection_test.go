package busproject

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// mapSessions is a Sessions fake resolving a fixed set of live Voice Sessions by
// id — the multi-session registry stand-in.
type mapSessions struct {
	mu   sync.Mutex
	live map[uuid.UUID]storage.VoiceSession
}

func newMapSessions(vss ...storage.VoiceSession) *mapSessions {
	m := &mapSessions{live: map[uuid.UUID]storage.VoiceSession{}}
	for _, vs := range vss {
		m.live[vs.ID] = vs
	}
	return m
}

func (m *mapSessions) Resolve(id uuid.UUID) (storage.VoiceSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	vs, ok := m.live[id]
	return vs, ok
}

func (m *mapSessions) remove(id uuid.UUID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.live, id)
}

// testTurn is a minimal per-turn state for the scaffold tests.
type testTurn struct {
	text string
}

// stampedFinal is an STTFinal already stamped with sid (as voiceevent.Forward
// leaves it on the process bus).
func stampedFinal(sid string, sec int) voiceevent.Event {
	return voiceevent.STTFinal{
		At:        time.Date(2026, 6, 27, 18, 0, sec, 0, time.UTC),
		Text:      "x",
		TurnID:    "t",
		SessionID: sid,
	}
}

// TestProjection_TwoSessionsInterleaveNoCrossTalk is the #487 isolation invariant:
// two sessions publishing interleaved events fold into their OWN entries — each
// event attributed to the Voice Session its SessionID names, each session's turn
// state kept separate, one Resolve per session (cached thereafter).
func TestProjection_TwoSessionsInterleaveNoCrossTalk(t *testing.T) {
	vsA := storage.VoiceSession{ID: uuid.New(), CampaignID: uuid.New()}
	vsB := storage.VoiceSession{ID: uuid.New(), CampaignID: uuid.New()}
	ss := newMapSessions(vsA, vsB)

	var mu sync.Mutex
	var foldSessions []storage.VoiceSession
	var starts []string
	var p *Projection[testTurn]
	p = New[testTurn](ss, &mu, nil, Hooks{
		Fold: func(e voiceevent.Event) {
			sid := voiceevent.SessionIDOf(e)
			foldSessions = append(foldSessions, p.Session(sid))
			p.Turn(sid, "t").text += sid[:1]
		},
		StartSession: func(sid string) { starts = append(starts, sid) },
	})
	bus := voiceevent.NewBus()
	p.Subscribe(bus)

	a, b := vsA.ID.String(), vsB.ID.String()
	// Interleave A, B, A, B.
	bus.Publish(stampedFinal(a, 1))
	bus.Publish(stampedFinal(b, 2))
	bus.Publish(stampedFinal(a, 3))
	bus.Publish(stampedFinal(b, 4))

	if len(starts) != 2 {
		t.Fatalf("StartSession fired %d times, want 2 (once per session)", len(starts))
	}
	if len(foldSessions) != 4 {
		t.Fatalf("folded %d events, want 4", len(foldSessions))
	}
	// Each fold saw the FKs of the session its event named — never the other's.
	wantByCall := []storage.VoiceSession{vsA, vsB, vsA, vsB}
	for i, want := range wantByCall {
		if foldSessions[i].ID != want.ID || foldSessions[i].CampaignID != want.CampaignID {
			t.Errorf("fold %d attributed to %+v, want %s/%s", i, foldSessions[i], want.ID, want.CampaignID)
		}
	}
	// Turn state is per-session: A's turn saw only A's events, B's only B's.
	mu.Lock()
	defer mu.Unlock()
	if got := p.Turn(a, "t").text; got != vsA.ID.String()[:1]+vsA.ID.String()[:1] {
		t.Errorf("session A turn text = %q, want two A-marks (no B leakage)", got)
	}
	if got := p.Turn(b, "t").text; got != vsB.ID.String()[:1]+vsB.ID.String()[:1] {
		t.Errorf("session B turn text = %q, want two B-marks (no A leakage)", got)
	}
}

// TestProjection_ResolveOncePerSession pins the one-Resolve-per-session cost
// model: the FIRST event for a session Resolves it and caches the FKs; later
// events for the same session never Resolve again.
func TestProjection_ResolveOncePerSession(t *testing.T) {
	vs := storage.VoiceSession{ID: uuid.New(), CampaignID: uuid.New()}
	ss := &countingSessions{inner: newMapSessions(vs)}

	var mu sync.Mutex
	p := New[testTurn](ss, &mu, nil, Hooks{Fold: func(voiceevent.Event) {}})
	bus := voiceevent.NewBus()
	p.Subscribe(bus)

	sid := vs.ID.String()
	bus.Publish(stampedFinal(sid, 1))
	bus.Publish(stampedFinal(sid, 2))
	bus.Publish(stampedFinal(sid, 3))

	if got := ss.calls(); got != 1 {
		t.Errorf("Resolve called %d times for one session, want 1 (cached after first)", got)
	}
}

// countingSessions counts Resolve calls.
type countingSessions struct {
	inner *mapSessions
	mu    sync.Mutex
	n     int
}

func (c *countingSessions) Resolve(id uuid.UUID) (storage.VoiceSession, bool) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return c.inner.Resolve(id)
}
func (c *countingSessions) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// TestProjection_DropsUnattributable pins the three drop paths: an event with no
// SessionID, an unparsable one, and one for a session the registry does not
// resolve are all dropped — never folded, no entry created.
func TestProjection_DropsUnattributable(t *testing.T) {
	ss := newMapSessions() // resolves nothing
	var mu sync.Mutex
	folds := 0
	p := New[testTurn](ss, &mu, nil, Hooks{
		Fold:         func(voiceevent.Event) { folds++ },
		StartSession: func(string) { t.Error("StartSession fired for an unattributable event") },
	})
	bus := voiceevent.NewBus()
	p.Subscribe(bus)

	bus.Publish(voiceevent.STTFinal{Text: "no session id"})                // "" → drop
	bus.Publish(voiceevent.STTFinal{Text: "bad", SessionID: "not-a-uuid"}) // parse fail → drop
	bus.Publish(stampedFinal(uuid.New().String(), 1))                      // unresolved → drop

	if folds != 0 {
		t.Errorf("folded %d unattributable events, want 0", folds)
	}
	mu.Lock()
	defer mu.Unlock()
	if p.Has(uuid.New().String()) {
		t.Error("Has reported an entry for an event that was dropped")
	}
}

// TestProjection_CloseOnlyThatEntry pins explicit-Close isolation (#487): Closing
// one session runs its FinishSession with its FKs still visible and tears down
// ONLY its entry — the other session's state is untouched, and a later event for
// the closed session (a straggler the registry no longer resolves) is dropped
// rather than reviving it.
func TestProjection_CloseOnlyThatEntry(t *testing.T) {
	vsA := storage.VoiceSession{ID: uuid.New(), CampaignID: uuid.New()}
	vsB := storage.VoiceSession{ID: uuid.New(), CampaignID: uuid.New()}
	ss := newMapSessions(vsA, vsB)

	var mu sync.Mutex
	var finishSaw []storage.VoiceSession
	var p *Projection[testTurn]
	p = New[testTurn](ss, &mu, nil, Hooks{
		Fold:          func(e voiceevent.Event) { p.Turn(voiceevent.SessionIDOf(e), "t").text += "." },
		FinishSession: func(sid string) { finishSaw = append(finishSaw, p.Session(sid)) },
	})
	bus := voiceevent.NewBus()
	p.Subscribe(bus)

	a, b := vsA.ID.String(), vsB.ID.String()
	bus.Publish(stampedFinal(a, 1))
	bus.Publish(stampedFinal(b, 2))

	// Close A only. The session leaves the registry (its finalizer ran).
	ss.remove(vsA.ID)
	p.Close(a)

	if len(finishSaw) != 1 || finishSaw[0].ID != vsA.ID {
		t.Fatalf("FinishSession saw %+v, want [session A] with its FKs still visible", finishSaw)
	}

	mu.Lock()
	if p.Has(a) {
		t.Error("session A entry survived Close")
	}
	if !p.Has(b) {
		t.Error("session B entry was torn down by A's Close")
	}
	if p.Turn(b, "t").text != "." {
		t.Errorf("session B turn state = %q, want it untouched by A's Close", p.Turn(b, "t").text)
	}
	mu.Unlock()

	// A straggler for the closed session A is dropped, not revived.
	bus.Publish(stampedFinal(a, 3))
	mu.Lock()
	defer mu.Unlock()
	if p.Has(a) {
		t.Error("a straggler revived the closed session A")
	}
}

// TestProjection_CloseUnknownNoop: Closing a session that folded no events (or is
// already closed) is a no-op — no FinishSession, no panic.
func TestProjection_CloseUnknownNoop(t *testing.T) {
	ss := newMapSessions()
	var mu sync.Mutex
	p := New[testTurn](ss, &mu, nil, Hooks{
		FinishSession: func(string) { t.Error("FinishSession fired for an unknown session") },
	})
	p.Close(uuid.New().String()) // must not panic
}

// TestProjection_TurnLifecycle: Turn lazy-creates once per (session, turn) within
// a known session, Lookup never creates, and both return nil for an unknown
// session.
func TestProjection_TurnLifecycle(t *testing.T) {
	vs := storage.VoiceSession{ID: uuid.New(), CampaignID: uuid.New()}
	ss := newMapSessions(vs)
	var mu sync.Mutex
	var p *Projection[testTurn]
	p = New[testTurn](ss, &mu, nil, Hooks{Fold: func(e voiceevent.Event) {
		sid := voiceevent.SessionIDOf(e)
		p.Turn(sid, "t1")
	}})
	bus := voiceevent.NewBus()
	p.Subscribe(bus)

	sid := vs.ID.String()
	mu.Lock()
	if got := p.Turn("unknown-session", "t1"); got != nil {
		t.Errorf("Turn on unknown session = %+v, want nil", got)
	}
	if got := p.Lookup(sid, "t1"); got != nil {
		t.Errorf("Lookup before any event = %+v, want nil", got)
	}
	mu.Unlock()

	bus.Publish(stampedFinal(sid, 1)) // creates the session entry + turn t1

	mu.Lock()
	defer mu.Unlock()
	first := p.Turn(sid, "t1")
	if first == nil {
		t.Fatal("Turn returned nil for a known session")
	}
	first.text = "kept"
	if again := p.Turn(sid, "t1"); again != first {
		t.Error("Turn returned a fresh state for a seen (session, turn)")
	}
	if got := p.Lookup(sid, "t1"); got != first {
		t.Errorf("Lookup = %+v, want the created turn", got)
	}
}

// TestProjection_NilBusSubscribeNoop: an event-less host subscribes nothing and
// does not panic.
func TestProjection_NilBusSubscribeNoop(t *testing.T) {
	ss := newMapSessions()
	var mu sync.Mutex
	p := New[testTurn](ss, &mu, nil, Hooks{Fold: func(voiceevent.Event) {}})
	p.Subscribe(nil)
}
