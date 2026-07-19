package knowledge_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/knowledge"
	"github.com/MrWong99/Glyphoxa/internal/recap"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeRecapEngine is a scripted RecapEngine: it records the session ids it was
// handed and replays a fixed recap text/error, so the adapter's picker and error
// mapping are pinned without an LLM.
type fakeRecapEngine struct {
	text    string
	err     error
	gotIDs  []uuid.UUID
	callCnt int
}

func (f *fakeRecapEngine) Recap(_ context.Context, ids []uuid.UUID) (recap.Result, error) {
	f.callCnt++
	f.gotIDs = ids
	return recap.Result{Text: f.text, SessionIDs: ids}, f.err
}

// fakeRecapStore is a scripted RecapStore returning a fixed newest-first session list.
type fakeRecapStore struct {
	sessions []storage.VoiceSession
	err      error
	gotCID   uuid.UUID
	gotLimit int
}

func (f *fakeRecapStore) ListVoiceSessions(_ context.Context, cid uuid.UUID, limit int) ([]storage.VoiceSession, error) {
	f.gotCID = cid
	f.gotLimit = limit
	return f.sessions, f.err
}

func endedSession(lines int) storage.VoiceSession {
	return storage.VoiceSession{ID: uuid.New(), Status: storage.VoiceSessionEnded, LineCount: lines}
}
func runningSession() storage.VoiceSession {
	return storage.VoiceSession{ID: uuid.New(), Status: storage.VoiceSessionRunning, LineCount: 3}
}

// TestRecapAdapterIdleNoSession pins that with no live session the adapter reports
// ErrNoActiveSession (the active session is what resolves the Campaign) — the engine
// is never called.
func TestRecapAdapterIdleNoSession(t *testing.T) {
	eng := &fakeRecapEngine{text: "x"}
	a := knowledge.NewRecap(eng, &fakeRecapStore{})
	_, err := a.RecapLastSessions(context.Background(), 1)
	if !errors.Is(err, knowledge.ErrNoActiveSession) {
		t.Errorf("err = %v, want ErrNoActiveSession", err)
	}
	if eng.callCnt != 0 {
		t.Error("engine called with no active session")
	}
}

// TestRecapAdapterPicksNewestEndedNonEmpty pins the picker rule (isRecappable
// parity): a running row on top and an empty-ended row are skipped; the newest
// ended non-empty session is recapped.
func TestRecapAdapterPicksNewestEndedNonEmpty(t *testing.T) {
	cid := uuid.New()
	want := endedSession(5)
	store := &fakeRecapStore{sessions: []storage.VoiceSession{
		runningSession(), // skipped: still running
		{ID: uuid.New(), Status: storage.VoiceSessionEnded, LineCount: 0}, // skipped: empty
		want, // newest recappable
		endedSession(9),
	}}
	eng := &fakeRecapEngine{text: "the recap"}
	a := knowledge.NewRecap(eng, store)

	out, err := a.RecapLastSessions(liveCtx(cid), 1)
	if err != nil {
		t.Fatalf("RecapLastSessions: %v", err)
	}
	if out != "the recap" {
		t.Errorf("out = %q, want engine text verbatim", out)
	}
	if store.gotCID != cid {
		t.Errorf("listed campaign %s, want active %s", store.gotCID, cid)
	}
	if len(eng.gotIDs) != 1 || eng.gotIDs[0] != want.ID {
		t.Errorf("recapped ids = %v, want [%s]", eng.gotIDs, want.ID)
	}
}

// TestRecapAdapterTakesNNewest pins that n=2 recaps the two newest recappable
// sessions in newest-first list order.
func TestRecapAdapterTakesNNewest(t *testing.T) {
	first, second, third := endedSession(4), endedSession(5), endedSession(6)
	store := &fakeRecapStore{sessions: []storage.VoiceSession{first, second, third}}
	eng := &fakeRecapEngine{text: "two"}
	a := knowledge.NewRecap(eng, store)

	if _, err := a.RecapLastSessions(liveCtx(uuid.New()), 2); err != nil {
		t.Fatalf("RecapLastSessions: %v", err)
	}
	if len(eng.gotIDs) != 2 || eng.gotIDs[0] != first.ID || eng.gotIDs[1] != second.ID {
		t.Errorf("recapped ids = %v, want [%s %s]", eng.gotIDs, first.ID, second.ID)
	}
}

// TestRecapAdapterNoneRecappable pins the friendly error when only running/empty
// rows exist — the engine is never called.
func TestRecapAdapterNoneRecappable(t *testing.T) {
	store := &fakeRecapStore{sessions: []storage.VoiceSession{
		runningSession(),
		{ID: uuid.New(), Status: storage.VoiceSessionEnded, LineCount: 0},
	}}
	eng := &fakeRecapEngine{text: "x"}
	a := knowledge.NewRecap(eng, store)

	_, err := a.RecapLastSessions(liveCtx(uuid.New()), 1)
	if err == nil {
		t.Fatal("want a no-recappable-session error")
	}
	if eng.callCnt != 0 {
		t.Error("engine called with nothing recappable")
	}
}

// TestRecapAdapterMapsNoTranscript pins that recap.ErrNoTranscript (a race let an
// empty row through) is mapped to the same friendly no-session error, not surfaced
// raw.
func TestRecapAdapterMapsNoTranscript(t *testing.T) {
	store := &fakeRecapStore{sessions: []storage.VoiceSession{endedSession(3)}}
	eng := &fakeRecapEngine{err: recap.ErrNoTranscript}
	a := knowledge.NewRecap(eng, store)

	_, err := a.RecapLastSessions(liveCtx(uuid.New()), 1)
	if err == nil {
		t.Fatal("want the friendly no-session error")
	}
	if errors.Is(err, recap.ErrNoTranscript) {
		t.Errorf("raw recap.ErrNoTranscript leaked: %v", err)
	}
}

// TestNewRecapNilDepsPanics pins the wiring-bug guard (mirrors knowledge.New).
func TestNewRecapNilDepsPanics(t *testing.T) {
	cases := map[string]func(){
		"nil engine": func() { knowledge.NewRecap(nil, &fakeRecapStore{}) },
		"nil store":  func() { knowledge.NewRecap(&fakeRecapEngine{}, nil) },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("NewRecap should panic on a nil dep")
				}
			}()
			fn()
		})
	}
}
