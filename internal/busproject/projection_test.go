package busproject

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// scriptedSessions is a Sessions fake whose Snapshot answer the test scripts
// per call — the tool for pinning interleavings where the Voice Session state
// changes BETWEEN two Snapshot reads inside one projection (#149).
type scriptedSessions struct {
	mu    sync.Mutex
	fn    func(call int) (storage.VoiceSession, bool)
	calls int
}

func (s *scriptedSessions) set(fn func(call int) (storage.VoiceSession, bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fn = fn
	s.calls = 0
}

func (s *scriptedSessions) Snapshot() (storage.VoiceSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.fn(s.calls)
}

func (s *scriptedSessions) snapshotCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// testTurn is a minimal per-turn state for the scaffold tests.
type testTurn struct {
	text string
}

func event(sec int) voiceevent.Event {
	return voiceevent.STTFinal{At: time.Date(2026, 6, 27, 18, 0, sec, 0, time.UTC), Text: "x", TurnID: "t"}
}

// TestProjection_OneSnapshotPerEvent is the #149 invariant, mid-event session
// change: the Voice Session ends between the event's projection and any
// hypothetical second capture read. The scaffold must take exactly ONE
// Snapshot per event — serving both the id comparison and the FK capture — so
// the triggering event is attributed to the session that Snapshot returned,
// never to uuid.Nil and never dropped.
func TestProjection_OneSnapshotPerEvent(t *testing.T) {
	sessB := storage.VoiceSession{ID: uuid.New(), CampaignID: uuid.New()}
	ss := &scriptedSessions{}
	// The FIRST Snapshot of the event sees B active; any LATER Snapshot sees no
	// active session (B ended mid-projection).
	ss.set(func(call int) (storage.VoiceSession, bool) {
		if call == 1 {
			return sessB, true
		}
		return storage.VoiceSession{}, false
	})

	var mu sync.Mutex
	var folded []storage.VoiceSession
	var p *Projection[testTurn]
	p = New[testTurn](ss, &mu, Hooks{
		Fold: func(voiceevent.Event) { folded = append(folded, p.Session()) },
	})
	bus := voiceevent.NewBus()
	p.Subscribe(bus)

	bus.Publish(event(1))

	if got := ss.snapshotCalls(); got != 1 {
		t.Fatalf("projection took %d Snapshots for one event, want exactly 1 (#149)", got)
	}
	if len(folded) != 1 {
		t.Fatalf("folded %d events, want 1", len(folded))
	}
	if folded[0].ID != sessB.ID || folded[0].CampaignID != sessB.CampaignID {
		t.Errorf("event attributed to %+v, want session B (%s/%s) — never uuid.Nil", folded[0], sessB.ID, sessB.CampaignID)
	}
	if folded[0].ID == uuid.Nil {
		t.Errorf("event attributed to uuid.Nil")
	}
}

// TestProjection_RolloverAttribution is the stale-snapshot attribution half of
// #149 plus the rollover hook contract: FinishSession observes the OUTGOING
// session's FKs (a stale flush there must keep the OLD session's ids —
// TestChunker_RolloverFlushKeepsOldSessionIDs depends on this), StartSession
// and Fold observe the new session's, and the turn map resets wholesale.
func TestProjection_RolloverAttribution(t *testing.T) {
	sessA := storage.VoiceSession{ID: uuid.New(), CampaignID: uuid.New()}
	sessB := storage.VoiceSession{ID: uuid.New(), CampaignID: uuid.New()}
	ss := &scriptedSessions{}
	ss.set(func(int) (storage.VoiceSession, bool) { return sessA, true })

	var mu sync.Mutex
	var finishSaw, startSaw, foldSaw []storage.VoiceSession
	var p *Projection[testTurn]
	p = New[testTurn](ss, &mu, Hooks{
		Fold:          func(voiceevent.Event) { foldSaw = append(foldSaw, p.Session()) },
		FinishSession: func() { finishSaw = append(finishSaw, p.Session()) },
		StartSession:  func() { startSaw = append(startSaw, p.Session()) },
	})
	bus := voiceevent.NewBus()
	p.Subscribe(bus)

	// First event under A: the first rollover fires FinishSession with the
	// process-start zero state, then StartSession + Fold under A.
	bus.Publish(event(1))
	mu.Lock()
	p.Turn("t1").text = "under A"
	mu.Unlock()

	// A ends, B starts: FinishSession must still see A (old FKs), everything
	// after must see B, and A's turn state must be gone.
	ss.set(func(int) (storage.VoiceSession, bool) { return sessB, true })
	bus.Publish(event(2))

	if len(finishSaw) != 2 || finishSaw[0].ID != uuid.Nil || finishSaw[1].ID != sessA.ID {
		t.Errorf("FinishSession saw %+v, want [zero, session A] (old FKs at flush time)", finishSaw)
	}
	if len(startSaw) != 2 || startSaw[0].ID != sessA.ID || startSaw[1].ID != sessB.ID {
		t.Errorf("StartSession saw %+v, want [A, B]", startSaw)
	}
	if len(foldSaw) != 2 || foldSaw[0].ID != sessA.ID || foldSaw[1].ID != sessB.ID {
		t.Errorf("Fold saw %+v, want [A, B]", foldSaw)
	}
	if foldSaw[1].CampaignID != sessB.CampaignID {
		t.Errorf("post-rollover fold campaign = %s, want B's %s", foldSaw[1].CampaignID, sessB.CampaignID)
	}

	mu.Lock()
	defer mu.Unlock()
	if got := p.Lookup("t1"); got != nil {
		t.Errorf("turn map survived the rollover: %+v", got)
	}
	if got := p.ActiveID(); got != sessB.ID.String() {
		t.Errorf("ActiveID = %q, want session B", got)
	}
}

// TestProjection_InactiveDropsEvent: with no active Voice Session the event is
// dropped (ADR-0039) — no rollover, no fold, no state change.
func TestProjection_InactiveDropsEvent(t *testing.T) {
	ss := &scriptedSessions{}
	ss.set(func(int) (storage.VoiceSession, bool) { return storage.VoiceSession{}, false })

	var mu sync.Mutex
	folds := 0
	p := New[testTurn](ss, &mu, Hooks{
		Fold:          func(voiceevent.Event) { folds++ },
		FinishSession: func() { t.Error("FinishSession fired with no active session") },
		StartSession:  func() { t.Error("StartSession fired with no active session") },
	})
	bus := voiceevent.NewBus()
	p.Subscribe(bus)

	bus.Publish(event(1))

	if folds != 0 {
		t.Errorf("folded %d events with no active session, want 0", folds)
	}
	mu.Lock()
	defer mu.Unlock()
	if p.ActiveID() != "" {
		t.Errorf("ActiveID = %q after dropped event, want \"\"", p.ActiveID())
	}
}

// TestProjection_InactiveStragglerKeepsState: an event arriving AFTER the
// active Voice Session ended (Snapshot flips inactive between the session's
// last fold and Finalize) is dropped WITHOUT touching the captured state — the
// relay's endSession guard compares against ActiveID, so a straggler that
// cleared it would suppress the terminal `status: idle` frame (#144), and a
// cleared turn map would break a same-session resume.
func TestProjection_InactiveStragglerKeepsState(t *testing.T) {
	sess := storage.VoiceSession{ID: uuid.New(), CampaignID: uuid.New()}
	ss := &scriptedSessions{}
	ss.set(func(int) (storage.VoiceSession, bool) { return sess, true })

	var mu sync.Mutex
	folds := 0
	var p *Projection[testTurn]
	p = New[testTurn](ss, &mu, Hooks{
		Fold: func(voiceevent.Event) { folds++; p.Turn("t1").text = "kept" },
	})
	bus := voiceevent.NewBus()
	p.Subscribe(bus)

	bus.Publish(event(1)) // attributed to sess; creates turn t1

	// The session ends; a straggler event is dropped, state untouched.
	ss.set(func(int) (storage.VoiceSession, bool) { return storage.VoiceSession{}, false })
	bus.Publish(event(2))

	if folds != 1 {
		t.Fatalf("folded %d events, want 1 (straggler dropped)", folds)
	}
	mu.Lock()
	if got := p.ActiveID(); got != sess.ID.String() {
		t.Errorf("ActiveID = %q after dropped straggler, want the ended session's id retained", got)
	}
	if got := p.Session(); got.ID != sess.ID || got.CampaignID != sess.CampaignID {
		t.Errorf("Session = %+v after dropped straggler, want the captured FKs retained", got)
	}
	if turn := p.Lookup("t1"); turn == nil || turn.text != "kept" {
		t.Errorf("turn state = %+v after dropped straggler, want it retained", turn)
	}
	mu.Unlock()

	// The SAME session reported active again (Snapshot raced, not a new
	// session): no rollover — folding resumes on the retained turn state.
	ss.set(func(int) (storage.VoiceSession, bool) { return sess, true })
	bus.Publish(event(3))

	mu.Lock()
	defer mu.Unlock()
	if folds != 2 {
		t.Fatalf("folded %d events, want 2", folds)
	}
	if turn := p.Lookup("t1"); turn == nil || turn.text != "kept" {
		t.Errorf("turn state = %+v after resume, want no rollover for the same session id", turn)
	}
}

// TestProjection_SameSessionNoRollover: consecutive events under one session
// fold without re-firing the rollover hooks, and turn state persists.
func TestProjection_SameSessionNoRollover(t *testing.T) {
	sess := storage.VoiceSession{ID: uuid.New(), CampaignID: uuid.New()}
	ss := &scriptedSessions{}
	ss.set(func(int) (storage.VoiceSession, bool) { return sess, true })

	var mu sync.Mutex
	rollovers := 0
	var p *Projection[testTurn]
	p = New[testTurn](ss, &mu, Hooks{
		Fold:         func(voiceevent.Event) { p.Turn("t1").text += "." },
		StartSession: func() { rollovers++ },
	})
	bus := voiceevent.NewBus()
	p.Subscribe(bus)

	bus.Publish(event(1))
	bus.Publish(event(2))
	bus.Publish(event(3))

	if rollovers != 1 {
		t.Errorf("rolled over %d times for one session, want 1", rollovers)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := p.Turn("t1").text; got != "..." {
		t.Errorf("turn state = %q, want it to coalesce across the session's events", got)
	}
}

// TestProjection_TurnLifecycle: Turn lazy-creates exactly once per id and
// Lookup never creates.
func TestProjection_TurnLifecycle(t *testing.T) {
	ss := &scriptedSessions{}
	ss.set(func(int) (storage.VoiceSession, bool) { return storage.VoiceSession{}, false })
	var mu sync.Mutex
	p := New[testTurn](ss, &mu, Hooks{Fold: func(voiceevent.Event) {}})

	mu.Lock()
	defer mu.Unlock()
	if got := p.Lookup("t1"); got != nil {
		t.Fatalf("Lookup created a turn: %+v", got)
	}
	first := p.Turn("t1")
	if first == nil {
		t.Fatal("Turn returned nil")
	}
	first.text = "kept"
	if again := p.Turn("t1"); again != first {
		t.Errorf("Turn returned a fresh state for a seen id")
	}
	if got := p.Lookup("t1"); got != first {
		t.Errorf("Lookup = %+v, want the created turn", got)
	}
}

// TestProjection_NilBusSubscribeNoop: an event-less host (unit tests driving
// the fold directly) subscribes nothing and does not panic.
func TestProjection_NilBusSubscribeNoop(t *testing.T) {
	ss := &scriptedSessions{}
	ss.set(func(int) (storage.VoiceSession, bool) { return storage.VoiceSession{}, false })
	var mu sync.Mutex
	p := New[testTurn](ss, &mu, Hooks{Fold: func(voiceevent.Event) {}})
	p.Subscribe(nil)
}
