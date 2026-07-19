package transcript

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// fakeLineStore is an in-memory LineStore for the relay's persistence tests: it
// records UPSERTs keyed (session, line_id) — so a coalescing reply collapses to
// one entry — and serves List/Count off that map.
type fakeLineStore struct {
	mu    sync.Mutex
	lines map[string]storage.TranscriptLine
}

func newFakeLineStore() *fakeLineStore {
	return &fakeLineStore{lines: map[string]storage.TranscriptLine{}}
}

func (f *fakeLineStore) key(sid uuid.UUID, lid string) string { return sid.String() + "/" + lid }

func (f *fakeLineStore) UpsertTranscriptLine(_ context.Context, l storage.TranscriptLine) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := f.key(l.VoiceSessionID, l.LineID)
	if prev, ok := f.lines[k]; ok {
		l.Seq = prev.Seq // mirror the real UPSERT (#149): seq is fixed at insert time
	}
	f.lines[k] = l
	return nil
}

func (f *fakeLineStore) ListTranscriptLines(_ context.Context, sid uuid.UUID) ([]storage.TranscriptLine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []storage.TranscriptLine
	for _, l := range f.lines {
		if l.VoiceSessionID == sid {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

func (f *fakeLineStore) CountTranscriptLines(_ context.Context, sid uuid.UUID) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, l := range f.lines {
		if l.VoiceSessionID == sid {
			n++
		}
	}
	return n, nil
}

// TestPersist_CoalescesAndCounts is the #74 persistence seam: the relay tees each
// projected Line into the async writer, an Agent reply coalesces to ONE row under
// its stable line_id, and Finalize drains the queue then returns the authoritative
// count — distinct lines == rows == the summary line_count.
func TestPersist_CoalescesAndCounts(t *testing.T) {
	bus := voiceevent.NewBus()
	fs := &fakeSessions{id: uuid.New(), active: true}
	store := newFakeLineStore()
	r := NewRelay(fwd(t, bus, fs), fs, store, nil)

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "Hello Bart", TurnID: "t1"})
	bus.Publish(voiceevent.AddressRouted{
		At: at(2), TurnID: "t1",
		Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"},
	})
	bus.Publish(voiceevent.TTSInvoked{At: at(3), Sentence: "Well met.", TurnID: "t1"})
	bus.Publish(voiceevent.TTSInvoked{At: at(4), Sentence: "What'll it be?", TurnID: "t1"})
	bus.Publish(voiceevent.TurnEnded{At: at(5), TurnID: "t1", Reason: voiceevent.TurnEndBarge})

	// Finalize drains the writer queue (flush barrier) then counts: 2 distinct
	// lines (one human, one coalesced reply) regardless of the 2 reply UPSERTs.
	n, err := r.Finalize(context.Background(), fs.id)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if n != 2 {
		t.Fatalf("Finalize count = %d, want 2 (coalesced reply is one line)", n)
	}

	got, _ := store.ListTranscriptLines(context.Background(), fs.id)
	if len(got) != 2 {
		t.Fatalf("persisted %d lines, want 2: %+v", len(got), got)
	}
	if got[0].LineID != "u:1" || got[0].Text != "Hello Bart" {
		t.Errorf("persisted human line = %+v", got[0])
	}
	if got[1].LineID != "a:t1" || got[1].Text != "Well met. What'll it be?" || got[1].VoiceSessionID != fs.id {
		t.Errorf("persisted coalesced reply = %+v", got[1])
	}
}

// TestSnapshot_EndedSessionReplaysFromDB is #74 AC3: the snapshot for a session
// that is NOT the live active one replays its persisted history from the store,
// ordered by seq, with status "idle" — so a reload sees the transcript after the
// in-memory ring is gone.
func TestSnapshot_EndedSessionReplaysFromDB(t *testing.T) {
	bus := voiceevent.NewBus()
	sid := uuid.New()
	fs := &fakeSessions{id: sid, active: false} // not live
	store := newFakeLineStore()
	r := NewRelay(fwd(t, bus, fs), fs, store, nil)

	ctx := context.Background()
	// Seed out of seq order to prove ORDER BY seq on read.
	_ = store.UpsertTranscriptLine(ctx, storage.TranscriptLine{
		VoiceSessionID: sid, LineID: "a:t1", Seq: 3, Who: "Bart", Tag: "NPC", Kind: "npc", TS: at(2), Text: "Well met. Sit.",
	})
	_ = store.UpsertTranscriptLine(ctx, storage.TranscriptLine{
		VoiceSessionID: sid, LineID: "u:1", Seq: 1, Who: "Player / DM", Kind: "player", TS: at(1), Text: "Hello Bart",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sid.String(), nil)
	req.SetPathValue("id", sid.String())
	w := httptest.NewRecorder()
	r.ServeSnapshot(w, req)

	var v View
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if v.Status != "idle" || v.Typing.Active {
		t.Fatalf("ended snapshot status=%q typing=%+v, want idle/inactive", v.Status, v.Typing)
	}
	if len(v.Lines) != 2 {
		t.Fatalf("replayed %d lines, want 2: %+v", len(v.Lines), v.Lines)
	}
	if v.Lines[0].ID != "u:1" || v.Lines[0].Text != "Hello Bart" {
		t.Errorf("line[0] = %+v, want human u:1 first (seq order)", v.Lines[0])
	}
	if v.Lines[1].ID != "a:t1" || v.Lines[1].Kind != KindNPC || v.Lines[1].Tag != "NPC" || v.Lines[1].Text != "Well met. Sit." {
		t.Errorf("line[1] = %+v, want coalesced NPC reply", v.Lines[1])
	}
}

// TestPersist_TwoSessionsAttributedIndependently is the #487 persistence
// isolation invariant: two live sessions' interleaved lines persist under their
// OWN session/campaign FKs, never under uuid.Nil and never cross-attributed.
// (Replaces the old single-Snapshot rollover test — attribution is now by the
// event's stamped SessionID, not a fragile two-read rollover.)
func TestPersist_TwoSessionsAttributedIndependently(t *testing.T) {
	sessA := storage.VoiceSession{ID: uuid.New(), CampaignID: uuid.New()}
	sessB := storage.VoiceSession{ID: uuid.New(), CampaignID: uuid.New()}
	busA, busB := voiceevent.NewBus(), voiceevent.NewBus()
	proc := voiceevent.NewBus()
	t.Cleanup(voiceevent.Forward(busA, proc, sessA.ID.String()))
	t.Cleanup(voiceevent.Forward(busB, proc, sessB.ID.String()))
	fs := newMultiSessions(sessA, sessB)
	store := newFakeLineStore()
	r := NewRelay(proc, fs, store, nil)
	ctx := context.Background()

	// Interleave the two sessions' human lines.
	busA.Publish(voiceevent.STTFinal{At: at(1), Text: "hi from A", TurnID: "t1"})
	busB.Publish(voiceevent.STTFinal{At: at(2), Text: "hi from B", TurnID: "t2"})

	if _, err := r.Finalize(ctx, sessA.ID); err != nil {
		t.Fatalf("Finalize(A): %v", err)
	}
	if _, err := r.Finalize(ctx, sessB.ID); err != nil {
		t.Fatalf("Finalize(B): %v", err)
	}

	if nilLines, _ := store.ListTranscriptLines(ctx, uuid.Nil); len(nilLines) != 0 {
		t.Errorf("line persisted under uuid.Nil: %+v", nilLines)
	}
	gotA, _ := store.ListTranscriptLines(ctx, sessA.ID)
	if len(gotA) != 1 || gotA[0].Text != "hi from A" || gotA[0].CampaignID != sessA.CampaignID {
		t.Errorf("session A lines = %+v, want exactly its own line under A's campaign (no B leakage)", gotA)
	}
	gotB, _ := store.ListTranscriptLines(ctx, sessB.ID)
	if len(gotB) != 1 || gotB[0].Text != "hi from B" || gotB[0].CampaignID != sessB.CampaignID {
		t.Errorf("session B lines = %+v, want its line attributed to B's UUID + campaign", gotB)
	}
}

// TestPersist_DisabledIsNoop: a nil store leaves persistence off — no writer
// goroutine, Finalize returns 0, and projection still works (live-only relay).
func TestPersist_DisabledIsNoop(t *testing.T) {
	bus, r, _, id := liveRelay(t)
	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "hi", TurnID: "t1"})

	n, err := r.Finalize(context.Background(), uuid.MustParse(id))
	if err != nil || n != 0 {
		t.Fatalf("Finalize with no store = %d, %v; want 0, nil", n, err)
	}
}
