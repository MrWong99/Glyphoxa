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

// TestTranscriptLineUpsertKeepsInsertSeq is defect A of #149: a coalescing
// re-upsert must NOT move the line's ordering key. An Agent reply inserted at
// seq S keeps S across later upserts (which still update the text), so replay
// (ORDER BY seq) matches the live-view/insertion order even when an interleaved
// human line landed between the reply's sentences.
func TestTranscriptLineUpsertKeepsInsertSeq(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}

	ts := func(sec int) time.Time { return time.Date(2026, 6, 27, 18, 0, sec, 0, time.UTC) }

	// Agent reply starts at seq 10; a human backchannel lands at seq 12; the
	// reply's next sentence re-upserts the SAME line_id at seq 14.
	steps := []storage.TranscriptLine{
		{VoiceSessionID: vs.ID, CampaignID: campaignID, LineID: "a:t1", Seq: 10, Who: "Bart", Tag: "NPC", Kind: "npc", TS: ts(1), Text: "Well met."},
		{VoiceSessionID: vs.ID, CampaignID: campaignID, LineID: "u:5", Seq: 12, Who: "Player / DM", Kind: "player", TS: ts(2), Text: "mhm"},
		{VoiceSessionID: vs.ID, CampaignID: campaignID, LineID: "a:t1", Seq: 14, Who: "Bart", Tag: "NPC", Kind: "npc", TS: ts(3), Text: "Well met. What'll it be?"},
	}
	for _, l := range steps {
		if err := st.UpsertTranscriptLine(ctx, l); err != nil {
			t.Fatalf("UpsertTranscriptLine %s seq %d: %v", l.LineID, l.Seq, err)
		}
	}

	got, err := st.ListTranscriptLines(ctx, vs.ID)
	if err != nil {
		t.Fatalf("ListTranscriptLines: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("list len = %d, want 2: %+v", len(got), got)
	}
	// Replay order == insertion order: the reply stays FIRST at its insert seq.
	if got[0].LineID != "a:t1" || got[0].Seq != 10 {
		t.Errorf("line[0] = %s seq %d, want a:t1 at insert-time seq 10 (replay must match live order)", got[0].LineID, got[0].Seq)
	}
	if got[0].Text != "Well met. What'll it be?" {
		t.Errorf("line[0] text = %q, want coalesced final text (non-seq updates still apply)", got[0].Text)
	}
	if got[1].LineID != "u:5" || got[1].Seq != 12 {
		t.Errorf("line[1] = %s seq %d, want interjection u:5 at seq 12", got[1].LineID, got[1].Seq)
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

// TestTranscriptLineSpeakerID is #278 (E4, ADR-0050): a SpeakerID-bearing human
// Line persists the Discord snowflake per row, while an unattributed utterance
// (empty SpeakerID) persists NULL — asserted via direct SQL — and scans back as
// "" ("" ↔ NULL round-trip, so the empty case is byte-identical to old behavior).
func TestTranscriptLineSpeakerID(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}
	ts := time.Date(2026, 7, 9, 18, 0, 0, 0, time.UTC)

	// An attributed human line and an unattributed one (empty SpeakerID).
	lines := []storage.TranscriptLine{
		{VoiceSessionID: vs.ID, CampaignID: campaignID, LineID: "u:1", Seq: 1, Who: "Player / DM", Kind: "player", TS: ts, Text: "Hello", SpeakerDiscordUserID: "111222333"},
		{VoiceSessionID: vs.ID, CampaignID: campaignID, LineID: "u:2", Seq: 2, Who: "Player / DM", Kind: "player", TS: ts, Text: "Silent", SpeakerDiscordUserID: ""},
	}
	for _, l := range lines {
		if err := st.UpsertTranscriptLine(ctx, l); err != nil {
			t.Fatalf("UpsertTranscriptLine %s: %v", l.LineID, err)
		}
	}

	// Direct SQL: the attributed row stores the snowflake, the empty one stores NULL.
	var attributed string
	if err := pool.QueryRow(ctx,
		`SELECT speaker_discord_user_id FROM transcript_line WHERE voice_session_id=$1 AND line_id='u:1'`, vs.ID).
		Scan(&attributed); err != nil {
		t.Fatalf("select u:1 speaker: %v", err)
	}
	if attributed != "111222333" {
		t.Errorf("u:1 speaker_discord_user_id = %q, want 111222333", attributed)
	}
	var isNull bool
	if err := pool.QueryRow(ctx,
		`SELECT speaker_discord_user_id IS NULL FROM transcript_line WHERE voice_session_id=$1 AND line_id='u:2'`, vs.ID).
		Scan(&isNull); err != nil {
		t.Fatalf("select u:2 null-check: %v", err)
	}
	if !isNull {
		t.Errorf("empty SpeakerID must persist NULL, got non-null")
	}

	// Scan round-trips: "" for the unattributed row, the snowflake for the other.
	got, err := st.ListTranscriptLines(ctx, vs.ID)
	if err != nil {
		t.Fatalf("ListTranscriptLines: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("list len = %d, want 2", len(got))
	}
	if got[0].SpeakerDiscordUserID != "111222333" {
		t.Errorf("line[0].SpeakerDiscordUserID = %q, want 111222333", got[0].SpeakerDiscordUserID)
	}
	if got[1].SpeakerDiscordUserID != "" {
		t.Errorf("line[1].SpeakerDiscordUserID = %q, want \"\" (NULL round-trips to empty)", got[1].SpeakerDiscordUserID)
	}
}
