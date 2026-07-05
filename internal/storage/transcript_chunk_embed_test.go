//go:build integration

package storage_test

import (
	"context"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings/embeddingstest"
)

// TestSetChunkEmbeddingRoundtrip is #116's storage half against a real pgvector
// DB: a chunk inserted with embedding NULL is filled by SetChunkEmbedding with a
// 768-dim vector + model stamp; it then becomes returnable by the
// embedding-filtered retrieval query (embedding IS NOT NULL) while a still-NULL
// sibling is excluded, the backlog COUNT drops, and ListUnembeddedChunks returns
// only the un-embedded rows (oldest first). The vector round-trips exactly
// through encodeVector + the ::vector cast (proven element-wise), and the
// migration's embedding_model column carries the stamp.
func TestSetChunkEmbeddingRoundtrip(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}

	// Two chunks, both embedding NULL. Explicit, ordered created_at so the
	// oldest-first ListUnembeddedChunks ordering is deterministic to assert.
	t0 := time.Date(2026, 7, 5, 18, 0, 0, 0, time.UTC)
	older := insertChunkAt(t, pool, campaignID, vs.ID, "older chunk", t0)
	newer := insertChunkAt(t, pool, campaignID, vs.ID, "newer chunk", t0.Add(time.Minute))

	// Backlog starts at 2 and the queue is oldest-first.
	if n, err := st.CountUnembeddedChunks(ctx); err != nil || n != 2 {
		t.Fatalf("CountUnembeddedChunks = %d, %v; want 2", n, err)
	}
	queue, err := st.ListUnembeddedChunks(ctx, 10)
	if err != nil {
		t.Fatalf("ListUnembeddedChunks: %v", err)
	}
	if len(queue) != 2 || queue[0].ID != older || queue[1].ID != newer {
		t.Fatalf("queue order = %v, want [%s %s] (oldest first)", ids(queue), older, newer)
	}
	if queue[0].Content != "older chunk" || queue[0].EmbeddingModel != "" {
		t.Errorf("queue[0] = %+v, want content 'older chunk' and empty model (still NULL)", queue[0])
	}

	// Embed the older chunk with a real 768-dim vector + model stamp.
	vec := deterministicVector(t, "older chunk")
	if err := st.SetChunkEmbedding(ctx, older, vec, "nomic-embed-text"); err != nil {
		t.Fatalf("SetChunkEmbedding: %v", err)
	}

	// The embedded row: dims = 768, model stamped, and the stored vector equals
	// the input element-wise (encodeVector + ::vector round-trip, exact float32).
	var (
		dims       int
		model      string
		storedText string
	)
	if err := pool.QueryRow(ctx,
		`SELECT vector_dims(embedding), embedding_model, embedding::text
		   FROM transcript_chunk WHERE id = $1`, older).
		Scan(&dims, &model, &storedText); err != nil {
		t.Fatalf("read embedded row: %v", err)
	}
	if dims != embeddings.Dim {
		t.Errorf("vector_dims = %d, want %d", dims, embeddings.Dim)
	}
	if model != "nomic-embed-text" {
		t.Errorf("embedding_model = %q, want nomic-embed-text", model)
	}
	assertVectorEqual(t, storedText, vec)

	// Embedding-filtered query returns the embedded chunk and excludes the NULL
	// sibling (the retrieval contract, ADR-0011).
	nonNull := selectNonNullIDs(t, pool, vs.ID)
	if len(nonNull) != 1 || nonNull[0] != older {
		t.Errorf("WHERE embedding IS NOT NULL = %v, want [%s] only", nonNull, older)
	}

	// The backlog dropped to just the still-NULL sibling, and the queue now
	// excludes the embedded row.
	if n, err := st.CountUnembeddedChunks(ctx); err != nil || n != 1 {
		t.Fatalf("CountUnembeddedChunks after embed = %d, %v; want 1", n, err)
	}
	queue, err = st.ListUnembeddedChunks(ctx, 10)
	if err != nil {
		t.Fatalf("ListUnembeddedChunks (after embed): %v", err)
	}
	if len(queue) != 1 || queue[0].ID != newer {
		t.Errorf("queue after embed = %v, want [%s] (only the still-NULL sibling)", ids(queue), newer)
	}
}

// TestGetEmbeddingsProviderConfig covers the worker's provider-resolution read:
// ErrNotFound when nothing is bound, and the most-recently-updated 'embeddings'
// row otherwise (process-wide, no tenant filter).
func TestGetEmbeddingsProviderConfig(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	if _, err := st.GetEmbeddingsProviderConfig(ctx); err != storage.ErrNotFound {
		t.Fatalf("GetEmbeddingsProviderConfig with none bound = %v, want ErrNotFound", err)
	}

	if _, err := pool.Exec(ctx,
		`INSERT INTO provider_config
		   (tenant_id, component, provider, model, credentials_ciphertext, credentials_last4)
		 VALUES ($1, 'embeddings', 'ollama', 'nomic-embed-text', '\x00', 'ab12')`, tenantID); err != nil {
		t.Fatalf("insert embeddings config: %v", err)
	}

	cfg, err := st.GetEmbeddingsProviderConfig(ctx)
	if err != nil {
		t.Fatalf("GetEmbeddingsProviderConfig: %v", err)
	}
	if cfg.Component != storage.ComponentEmbeddings || cfg.Provider != "ollama" || cfg.Model != "nomic-embed-text" {
		t.Errorf("cfg = %+v, want the embeddings/ollama/nomic-embed-text row", cfg)
	}
}

// insertChunkAt inserts a NULL-embedding chunk with an explicit created_at so
// ordering assertions are deterministic (InsertTranscriptChunk defaults now()).
func insertChunkAt(t *testing.T, pool *pgxpool.Pool, campaignID, vsID uuid.UUID, content string, createdAt time.Time) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO transcript_chunk (campaign_id, voice_session_id, content, started_at, created_at)
		 VALUES ($1, $2, $3, $4, $4) RETURNING id`, campaignID, vsID, content, createdAt).Scan(&id); err != nil {
		t.Fatalf("insert chunk %q: %v", content, err)
	}
	return id
}

func deterministicVector(t *testing.T, text string) []float32 {
	t.Helper()
	vecs, err := (embeddingstest.Deterministic{}).Embed(context.Background(), []string{text})
	if err != nil {
		t.Fatalf("deterministic embed: %v", err)
	}
	return vecs[0]
}

func assertVectorEqual(t *testing.T, storedText string, want []float32) {
	t.Helper()
	got := parseVector(t, storedText)
	if len(got) != len(want) {
		t.Fatalf("stored vector len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if math.Abs(float64(got[i]-want[i])) > 1e-6 {
			t.Fatalf("stored vector[%d] = %g, want %g (round-trip not exact)", i, got[i], want[i])
		}
	}
}

func parseVector(t *testing.T, s string) []float32 {
	t.Helper()
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	parts := strings.Split(s, ",")
	out := make([]float32, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			t.Fatalf("parse vector element %q: %v", p, err)
		}
		out[i] = float32(f)
	}
	return out
}

func selectNonNullIDs(t *testing.T, pool *pgxpool.Pool, vsID uuid.UUID) []uuid.UUID {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT id FROM transcript_chunk
		  WHERE voice_session_id = $1 AND embedding IS NOT NULL
		  ORDER BY created_at`, vsID)
	if err != nil {
		t.Fatalf("select non-null: %v", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan id: %v", err)
		}
		out = append(out, id)
	}
	return out
}

func ids(chunks []storage.TranscriptChunk) []uuid.UUID {
	out := make([]uuid.UUID, len(chunks))
	for i, c := range chunks {
		out[i] = c.ID
	}
	return out
}
