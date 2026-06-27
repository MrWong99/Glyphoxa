//go:build integration

package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestTranscriptLinePersistence is AC1 (#74): lines are written under the session
// id, an Agent reply COALESCES under one line_id (UPSERT keyed
// (voice_session_id, line_id)) so re-writes update in place, Count == distinct
// rows == the line_count the summary records, and List returns them ordered by
// seq for replay-on-reload (AC3).
func TestTranscriptLinePersistence(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}

	ts := func(sec int) time.Time { return time.Date(2026, 6, 27, 18, 0, sec, 0, time.UTC) }

	// A human line, then an Agent reply across two sentences (same line_id) — the
	// coalescing UPSERT must leave ONE agent row with the final text + seq.
	lines := []storage.TranscriptLine{
		{VoiceSessionID: vs.ID, CampaignID: campaignID, LineID: "u:1", Seq: 1, Who: "Player / DM", Kind: "player", TS: ts(1), Text: "Hello Bart"},
		{VoiceSessionID: vs.ID, CampaignID: campaignID, LineID: "a:t1", Seq: 2, Who: "Bart", Tag: "NPC", Kind: "npc", TS: ts(2), Text: "Well met."},
		{VoiceSessionID: vs.ID, CampaignID: campaignID, LineID: "a:t1", Seq: 3, Who: "Bart", Tag: "NPC", Kind: "npc", TS: ts(2), Text: "Well met. What'll it be?"},
	}
	for _, l := range lines {
		if err := st.UpsertTranscriptLine(ctx, l); err != nil {
			t.Fatalf("UpsertTranscriptLine %s: %v", l.LineID, err)
		}
	}

	// Count == distinct line_ids == 2 (the coalescing reply is one row).
	n, err := st.CountTranscriptLines(ctx, vs.ID)
	if err != nil {
		t.Fatalf("CountTranscriptLines: %v", err)
	}
	if n != 2 {
		t.Fatalf("count = %d, want 2 (coalesced reply is one row)", n)
	}

	// List returns them ordered by seq, with the coalesced final text.
	got, err := st.ListTranscriptLines(ctx, vs.ID)
	if err != nil {
		t.Fatalf("ListTranscriptLines: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("list len = %d, want 2: %+v", len(got), got)
	}
	if got[0].LineID != "u:1" || got[0].Text != "Hello Bart" {
		t.Errorf("line[0] = %+v", got[0])
	}
	if got[1].LineID != "a:t1" || got[1].Text != "Well met. What'll it be?" || got[1].Tag != "NPC" {
		t.Errorf("line[1] (coalesced) = %+v", got[1])
	}

	// End the session with the authoritative count; line_count matches the rows.
	ended, err := st.EndVoiceSession(ctx, vs.ID, n)
	if err != nil {
		t.Fatalf("EndVoiceSession: %v", err)
	}
	if ended.LineCount != 2 {
		t.Errorf("ended line_count = %d, want 2 (matches rows)", ended.LineCount)
	}
}

// TestTranscriptLineCascade: an unknown session counts zero, and deleting the
// Voice Session cascades its lines (FK ON DELETE CASCADE).
func TestTranscriptLineCascade(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	if n, err := st.CountTranscriptLines(ctx, uuid.New()); err != nil || n != 0 {
		t.Fatalf("count of unknown session = %d, %v; want 0, nil", n, err)
	}

	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}
	if err := st.UpsertTranscriptLine(ctx, storage.TranscriptLine{
		VoiceSessionID: vs.ID, CampaignID: campaignID, LineID: "u:1", Seq: 1,
		Who: "Player / DM", Kind: "player", TS: time.Now(), Text: "hi",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM voice_sessions WHERE id = $1`, vs.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if n, err := st.CountTranscriptLines(ctx, vs.ID); err != nil || n != 0 {
		t.Errorf("count after cascade = %d, %v; want 0, nil", n, err)
	}
}
