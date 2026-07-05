//go:build integration

package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestTranscriptChunkPersistence is #104 AC1 + the ADR-0011 grain: a chunk is
// inserted with embedding NULL (the async pipeline fills it later), its fts column
// is generated non-null, and the text[]/uuid[] arrays round-trip. The
// NULL-embedding COUNT (the backlog gauge value) counts only un-embedded rows, and
// the same Voice Session carries transcript_line rows too — proving the chunk and
// line grains coexist as independent records of the same speech (ADR-0040).
func TestTranscriptChunkPersistence(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}

	agentA, agentB := uuid.New(), uuid.New()
	startedAt := time.Date(2026, 7, 5, 18, 0, 1, 0, time.UTC)

	id, err := st.InsertTranscriptChunk(ctx, storage.TranscriptChunk{
		CampaignID:            campaignID,
		VoiceSessionID:        vs.ID,
		Content:               "Player / DM: Hello Bart\nBart: Well met. Sit.",
		SpeakerDiscordUserIDs: []string{}, // anonymous STT lane (ADR-0039)
		ParticipatedAgentIDs:  []uuid.UUID{agentA, agentB},
		StartedAt:             startedAt,
	})
	if err != nil {
		t.Fatalf("InsertTranscriptChunk: %v", err)
	}
	if id == uuid.Nil {
		t.Fatal("insert returned a nil id")
	}

	// Read back the row's derived state directly: embedding NULL, fts generated
	// non-null, and the arrays scan back into their Go slice types.
	var (
		embNull    bool
		ftsNotNull bool
		content    string
		speakers   []string
		agents     []uuid.UUID
		gotVSID    uuid.UUID
	)
	if err := pool.QueryRow(ctx,
		`SELECT embedding IS NULL, fts IS NOT NULL, content,
		        speaker_discord_user_ids, participated_agent_ids, voice_session_id
		   FROM transcript_chunk WHERE id = $1`, id).
		Scan(&embNull, &ftsNotNull, &content, &speakers, &agents, &gotVSID); err != nil {
		t.Fatalf("read back chunk: %v", err)
	}
	if !embNull {
		t.Error("embedding is not NULL — the writer must insert with NULL embedding (ADR-0011)")
	}
	if !ftsNotNull {
		t.Error("fts generated column is NULL — expected a non-null tsvector over content")
	}
	if content != "Player / DM: Hello Bart\nBart: Well met. Sit." {
		t.Errorf("content = %q", content)
	}
	if len(speakers) != 0 {
		t.Errorf("speakers = %v, want empty", speakers)
	}
	if len(agents) != 2 || agents[0] != agentA || agents[1] != agentB {
		t.Errorf("participated_agent_ids = %v, want [%s %s] in order", agents, agentA, agentB)
	}
	if gotVSID != vs.ID {
		t.Errorf("voice_session_id = %s, want %s", gotVSID, vs.ID)
	}

	// CountUnembeddedChunks counts NULL-embedding rows only. Insert a second chunk,
	// then embed the FIRST (manually) and confirm the count drops to just the
	// still-unembedded one.
	if n, err := st.CountUnembeddedChunks(ctx); err != nil || n != 1 {
		t.Fatalf("CountUnembeddedChunks = %d, %v; want 1", n, err)
	}
	if _, err := st.InsertTranscriptChunk(ctx, storage.TranscriptChunk{
		CampaignID: campaignID, VoiceSessionID: vs.ID, Content: "Player / DM: second", StartedAt: startedAt,
	}); err != nil {
		t.Fatalf("InsertTranscriptChunk (second): %v", err)
	}
	if n, err := st.CountUnembeddedChunks(ctx); err != nil || n != 2 {
		t.Fatalf("CountUnembeddedChunks after second insert = %d, %v; want 2", n, err)
	}
	// Give the first chunk an embedding: the count must exclude it (only NULLs).
	if _, err := pool.Exec(ctx,
		`UPDATE transcript_chunk SET embedding = array_fill(0::real, ARRAY[768])::vector WHERE id = $1`, id); err != nil {
		t.Fatalf("embed first chunk: %v", err)
	}
	if n, err := st.CountUnembeddedChunks(ctx); err != nil || n != 1 {
		t.Fatalf("CountUnembeddedChunks after embedding one = %d, %v; want 1 (NULL only)", n, err)
	}

	// The two grains coexist: the SAME Voice Session also has transcript_line rows.
	if err := st.UpsertTranscriptLine(ctx, storage.TranscriptLine{
		VoiceSessionID: vs.ID, CampaignID: campaignID, LineID: "u:1", Seq: 1,
		Who: "Player / DM", Kind: "player", TS: startedAt, Text: "Hello Bart",
	}); err != nil {
		t.Fatalf("UpsertTranscriptLine: %v", err)
	}
	if n, err := st.CountTranscriptLines(ctx, vs.ID); err != nil || n != 1 {
		t.Fatalf("CountTranscriptLines = %d, %v; want 1 (line grain independent of chunk grain)", n, err)
	}
	var chunkRows int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM transcript_chunk WHERE voice_session_id = $1`, vs.ID).Scan(&chunkRows); err != nil {
		t.Fatalf("count chunk rows: %v", err)
	}
	if chunkRows != 2 {
		t.Errorf("chunk rows for session = %d, want 2 (both grains persist under one session)", chunkRows)
	}
}
