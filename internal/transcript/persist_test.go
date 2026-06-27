package transcript

import (
	"context"
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
	f.lines[f.key(l.VoiceSessionID, l.LineID)] = l
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
	r := NewRelay(bus, fs, store, nil)

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
