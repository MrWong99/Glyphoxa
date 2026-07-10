//go:build integration

package storage_test

import (
	"context"
	"strings"
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

// TestListTranscriptChunks is #288: the Campaign Bundle exporter's chunk read. It
// returns a Campaign's chunks ordered (created_at, id) with embedding_model always
// populated; with includeVectors=false every Embedding is "" (the default
// vector-stripping export, ADR-0053 d3); with includeVectors=true an embedded row
// carries pgvector text form "[...]" while a NULL-embedding row stays ""; and it is
// Campaign-scoped (#342) so a second Campaign's chunks never leak.
func TestListTranscriptChunks(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignA := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, err := st.CreateVoiceSession(ctx, campaignA)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}
	startedAt := time.Date(2026, 7, 5, 18, 0, 1, 0, time.UTC)

	// Two chunks in A; embed only the first.
	id1, err := st.InsertTranscriptChunk(ctx, storage.TranscriptChunk{
		CampaignID: campaignA, VoiceSessionID: vs.ID, Content: "first", StartedAt: startedAt,
	})
	if err != nil {
		t.Fatalf("InsertTranscriptChunk 1: %v", err)
	}
	id2, err := st.InsertTranscriptChunk(ctx, storage.TranscriptChunk{
		CampaignID: campaignA, VoiceSessionID: vs.ID, Content: "second", StartedAt: startedAt,
	})
	if err != nil {
		t.Fatalf("InsertTranscriptChunk 2: %v", err)
	}
	vec := make([]float32, 768)
	vec[0] = 0.25
	if err := st.SetChunkEmbedding(ctx, id1, vec, "nomic-embed-text"); err != nil {
		t.Fatalf("SetChunkEmbedding: %v", err)
	}

	// includeVectors=false: ordered rows, every Embedding "", model stamped on id1.
	chunks, err := st.ListTranscriptChunks(ctx, campaignA, false)
	if err != nil {
		t.Fatalf("ListTranscriptChunks false: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("ListTranscriptChunks false = %d, want 2", len(chunks))
	}
	if chunks[0].ID != id1 || chunks[1].ID != id2 {
		t.Errorf("order = [%s %s], want [%s %s] (created_at, id)", chunks[0].ID, chunks[1].ID, id1, id2)
	}
	for i, c := range chunks {
		if c.Embedding != "" {
			t.Errorf("chunk[%d].Embedding = %q, want \"\" (vectors stripped when includeVectors=false)", i, c.Embedding)
		}
		if c.CampaignID != campaignA {
			t.Errorf("chunk[%d].CampaignID = %s, want %s", i, c.CampaignID, campaignA)
		}
	}
	if chunks[0].EmbeddingModel != "nomic-embed-text" {
		t.Errorf("chunk[0].EmbeddingModel = %q, want nomic-embed-text (always selected)", chunks[0].EmbeddingModel)
	}
	if chunks[1].EmbeddingModel != "" {
		t.Errorf("chunk[1].EmbeddingModel = %q, want \"\" (never embedded)", chunks[1].EmbeddingModel)
	}

	// includeVectors=true: id1 carries its "[...]" vector text; id2 (NULL) stays "".
	withVec, err := st.ListTranscriptChunks(ctx, campaignA, true)
	if err != nil {
		t.Fatalf("ListTranscriptChunks true: %v", err)
	}
	if len(withVec) != 2 {
		t.Fatalf("ListTranscriptChunks true = %d, want 2", len(withVec))
	}
	if !strings.HasPrefix(withVec[0].Embedding, "[") || !strings.HasSuffix(withVec[0].Embedding, "]") {
		t.Errorf("embedded chunk Embedding = %q, want pgvector text form \"[...]\"", withVec[0].Embedding)
	}
	if withVec[1].Embedding != "" {
		t.Errorf("NULL-embedding chunk Embedding = %q, want \"\"", withVec[1].Embedding)
	}

	// Campaign-scoped: a second Campaign's chunk never leaks into A's list.
	campaignB, err := st.CreateCampaign(ctx, storage.NewCampaign{
		TenantID: tenantID, Name: "Other", System: "dnd5e", Language: "en",
	})
	if err != nil {
		t.Fatalf("CreateCampaign B: %v", err)
	}
	vsB, err := st.CreateVoiceSession(ctx, campaignB)
	if err != nil {
		t.Fatalf("CreateVoiceSession B: %v", err)
	}
	if _, err := st.InsertTranscriptChunk(ctx, storage.TranscriptChunk{
		CampaignID: campaignB, VoiceSessionID: vsB.ID, Content: "b-chunk", StartedAt: startedAt,
	}); err != nil {
		t.Fatalf("InsertTranscriptChunk B: %v", err)
	}
	aAgain, err := st.ListTranscriptChunks(ctx, campaignA, false)
	if err != nil {
		t.Fatalf("ListTranscriptChunks A again: %v", err)
	}
	if len(aAgain) != 2 {
		t.Errorf("ListTranscriptChunks A = %d, want 2 (no B leakage)", len(aAgain))
	}
	bChunks, err := st.ListTranscriptChunks(ctx, campaignB, false)
	if err != nil {
		t.Fatalf("ListTranscriptChunks B: %v", err)
	}
	if len(bChunks) != 1 {
		t.Errorf("ListTranscriptChunks B = %d, want 1", len(bChunks))
	}
}
