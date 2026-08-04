package appearance_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/appearance"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
)

// The appearance indexer (#545). What is asserted here is what the index is FOR
// (find the line again) and what it must never become (a fact an NPC recites).

type fakeStore struct {
	session   storage.VoiceSession
	campaign  storage.Campaign
	nodes     []storage.IndexableNode
	lines     []storage.IndexableLine
	recorded  []storage.NodeAppearance
	recordErr error
}

func (f *fakeStore) GetVoiceSession(context.Context, uuid.UUID) (storage.VoiceSession, error) {
	return f.session, nil
}
func (f *fakeStore) GetCampaign(context.Context, uuid.UUID) (storage.Campaign, error) {
	return f.campaign, nil
}
func (f *fakeStore) CampaignNodeNames(context.Context, uuid.UUID) ([]storage.IndexableNode, error) {
	return f.nodes, nil
}
func (f *fakeStore) SessionLinesForIndexing(context.Context, uuid.UUID) ([]storage.IndexableLine, error) {
	return f.lines, nil
}
func (f *fakeStore) RecordNodeAppearances(_ context.Context, rows []storage.NodeAppearance) error {
	if f.recordErr != nil {
		return f.recordErr
	}
	f.recorded = append(f.recorded, rows...)
	return nil
}

var (
	sessionID  = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	campaignID = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	bartID     = uuid.MustParse("33333333-3333-3333-3333-333333333333")
	ilvaID     = uuid.MustParse("44444444-4444-4444-4444-444444444444")
)

func world(lang string, lines ...string) *fakeStore {
	ls := make([]storage.IndexableLine, len(lines))
	for i, t := range lines {
		ls[i] = storage.IndexableLine{
			LineID: "u:" + string(rune('1'+i)),
			At:     time.Date(2026, 8, 4, 20, i, 0, 0, time.UTC),
			Text:   t,
		}
	}
	return &fakeStore{
		session:  storage.VoiceSession{ID: sessionID, CampaignID: campaignID},
		campaign: storage.Campaign{ID: campaignID, Language: lang},
		nodes: []storage.IndexableNode{
			{ID: bartID, Name: "Bart the innkeeper"},
			{ID: ilvaID, Name: "Dockmaster Ilva"},
		},
		lines: ls,
	}
}

func run(t *testing.T, st *fakeStore) {
	t.Helper()
	payload, err := appearance.Marshal(sessionID)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	h := appearance.IndexHandler(st, address.DefaultEncoders(), nil)
	if err := h(context.Background(), payload); err != nil {
		t.Fatalf("IndexHandler: %v", err)
	}
}

// TestIndex_RecordsTheLineANameWasSpokenIn: the whole point — "when did we last
// see that ogre" answered by a pointer to the exact line.
func TestIndex_RecordsTheLineANameWasSpokenIn(t *testing.T) {
	t.Parallel()
	st := world("en",
		"we should ask Bart the innkeeper about the ledger",
		"the weather is foul today",
		"Dockmaster Ilva wants a word",
	)
	run(t, st)

	if len(st.recorded) != 2 {
		t.Fatalf("recorded %d appearances, want one per named line: %+v", len(st.recorded), st.recorded)
	}
	byNode := map[uuid.UUID]storage.NodeAppearance{}
	for _, r := range st.recorded {
		byNode[r.NodeID] = r
	}
	if got := byNode[bartID].LineID; got != "u:1" {
		t.Errorf("Bart recorded against line %q, want the line he was named in", got)
	}
	if got := byNode[ilvaID].LineID; got != "u:3" {
		t.Errorf("Ilva recorded against line %q", got)
	}
	// The timestamp is denormalised so the "last seen" read sorts without a join.
	if !byNode[bartID].At.Equal(st.lines[0].At) {
		t.Errorf("appearance time = %v, want the line's own", byNode[bartID].At)
	}
	// Every row is scoped to the session's campaign, never the payload's.
	for _, r := range st.recorded {
		if r.CampaignID != campaignID || r.VoiceSessionID != sessionID {
			t.Errorf("row escaped its session/campaign: %+v", r)
		}
	}
}

// TestIndex_LinesThatNameNothingRecordNothing: an index that fires on every line
// is an index nobody reads.
func TestIndex_LinesThatNameNothingRecordNothing(t *testing.T) {
	t.Parallel()
	st := world("en",
		"we should smash the door open",
		"roll for initiative",
		"the ashes were still warm",
	)
	run(t, st)
	if len(st.recorded) != 0 {
		t.Fatalf("recorded %+v from lines that name nobody", st.recorded)
	}
}

// TestIndex_UsesTheCampaignLanguage: a German campaign matches a German
// mis-hearing. This is why the Campaign Language reaches the indexer at all.
func TestIndex_UsesTheCampaignLanguage(t *testing.T) {
	t.Parallel()
	st := &fakeStore{
		session:  storage.VoiceSession{ID: sessionID, CampaignID: campaignID},
		campaign: storage.Campaign{ID: campaignID, Language: "de"},
		nodes:    []storage.IndexableNode{{ID: bartID, Name: "Grimmzahn"}},
		lines: []storage.IndexableLine{
			{LineID: "u:1", At: time.Now(), Text: "hat Grimmzan das gesagt"},
		},
	}
	run(t, st)
	if len(st.recorded) != 1 {
		t.Fatalf("the German mis-spelling was not matched: %+v", st.recorded)
	}
}

// TestIndex_IsIdempotent: the handler re-derives the same pairs on every run, so
// a retry or a replay produces identical rows and the ON CONFLICT DO NOTHING
// write makes it a no-op. Nobody has to reason about whether it already ran.
func TestIndex_IsIdempotent(t *testing.T) {
	t.Parallel()
	st := world("en", "ask Bart the innkeeper")
	run(t, st)
	first := append([]storage.NodeAppearance(nil), st.recorded...)
	st.recorded = nil
	run(t, st)

	if len(st.recorded) != len(first) {
		t.Fatalf("re-run produced %d rows, first run %d", len(st.recorded), len(first))
	}
	for i := range first {
		if st.recorded[i] != first[i] {
			t.Errorf("row %d differs between runs:\n %+v\n %+v", i, first[i], st.recorded[i])
		}
	}
}

// TestIndex_EmptyCampaignOrSessionIsANoOp, and neither is an error: a session
// where nothing was said, or a campaign with no wiki, is ordinary.
func TestIndex_EmptyCampaignOrSessionIsANoOp(t *testing.T) {
	t.Parallel()
	noNodes := world("en", "ask Bart the innkeeper")
	noNodes.nodes = nil
	run(t, noNodes)
	if len(noNodes.recorded) != 0 {
		t.Errorf("recorded against an empty wiki: %+v", noNodes.recorded)
	}

	noLines := world("en")
	run(t, noLines)
	if len(noLines.recorded) != 0 {
		t.Errorf("recorded from an empty session: %+v", noLines.recorded)
	}
}

// TestIndex_UnparsablePayloadDeadLetters rather than retrying forever: a payload
// that cannot be parsed will never parse.
func TestIndex_UnparsablePayloadDeadLetters(t *testing.T) {
	t.Parallel()
	st := world("en", "ask Bart the innkeeper")
	h := appearance.IndexHandler(st, address.DefaultEncoders(), nil)
	if err := h(context.Background(), json.RawMessage(`{`)); err != nil {
		t.Fatalf("an unparsable payload should not retry: %v", err)
	}
	if len(st.recorded) != 0 {
		t.Error("an unparsable payload wrote rows")
	}
}
