package storage

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Transcript-chunk persistence (#104, ADR-0011): the chunk writer inserts one row
// per closed chunk (3–6 utterances) with embedding NULL; a background worker
// (#116) fills the embedding later and retrieval filters WHERE embedding IS NOT
// NULL. This CHUNK grain — the retrieval/embedding unit for NPC Hot Context — is
// distinct from the per-line transcript_line grain (ADR-0040): the two are
// independent records of the same speech and do not share rows.

// TranscriptChunk is one persisted Transcript Chunk. embedding is always NULL at
// insert (the async embedding pipeline, ADR-0011); EmbeddingModel is empty until
// the backfill worker (#116) embeds the row and stamps which model produced the
// vector — the provenance a model-switch re-embed pass keys off (ADR-0011).
type TranscriptChunk struct {
	ID                    uuid.UUID
	CampaignID            uuid.UUID
	VoiceSessionID        uuid.UUID   // column nullable; the writer always sets it
	Content               string      // the chunk's utterances joined "\n"
	SpeakerDiscordUserIDs []string    // empty in v1.0: anonymous STT lane (ADR-0039)
	ParticipatedAgentIDs  []uuid.UUID // Agents that spoke in the chunk (NPC-knowledge filter)
	EmbeddingModel        string      // '' until the backfill worker embeds the row (#116)
	StartedAt             time.Time
	CreatedAt             time.Time
}

// InsertTranscriptChunk writes one closed chunk and returns its generated id. The
// embedding is left NULL — the async pipeline fills it later (ADR-0011) — and the
// arrays default to empty (never NULL). embedding_model is left at its empty
// column default; the backfill worker (#116) stamps it when it embeds the row.
func (s *Store) InsertTranscriptChunk(ctx context.Context, c TranscriptChunk) (uuid.UUID, error) {
	speakers := c.SpeakerDiscordUserIDs
	if speakers == nil {
		speakers = []string{}
	}
	agents := c.ParticipatedAgentIDs
	if agents == nil {
		agents = []uuid.UUID{}
	}
	var id uuid.UUID
	err := s.db.QueryRow(ctx,
		`INSERT INTO transcript_chunk
		   (campaign_id, voice_session_id, content,
		    speaker_discord_user_ids, participated_agent_ids, started_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id`,
		c.CampaignID, c.VoiceSessionID, c.Content, speakers, agents, c.StartedAt).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("storage: insert transcript chunk: %w", err)
	}
	return id, nil
}

// CountUnembeddedChunks returns the number of Transcript Chunks still awaiting an
// embedding (embedding IS NULL) — the embedding-backlog gauge value (#104). It is
// process-wide (no tenant/campaign filter): ADR-0032 keeps that cardinality off
// the metric, so the gauge is a single global number.
func (s *Store) CountUnembeddedChunks(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM transcript_chunk WHERE embedding IS NULL`).Scan(&n); err != nil {
		return 0, fmt.Errorf("storage: count unembedded transcript chunks: %w", err)
	}
	return n, nil
}

const transcriptChunkColumns = `
	id, campaign_id, voice_session_id, content,
	speaker_discord_user_ids, participated_agent_ids,
	embedding_model, started_at, created_at`

func scanTranscriptChunk(row pgx.Row) (TranscriptChunk, error) {
	var (
		c    TranscriptChunk
		vsID uuid.NullUUID // column is nullable (ADR-0011 SEAM); may be NULL
	)
	err := row.Scan(
		&c.ID, &c.CampaignID, &vsID, &c.Content,
		&c.SpeakerDiscordUserIDs, &c.ParticipatedAgentIDs,
		&c.EmbeddingModel, &c.StartedAt, &c.CreatedAt,
	)
	c.VoiceSessionID = vsID.UUID // uuid.Nil when NULL
	return c, err
}

// ListUnembeddedChunks returns up to limit Transcript Chunks still awaiting an
// embedding (embedding IS NULL), oldest first — the backfill worker's work queue
// (#116, ADR-0011). Ordering by (created_at, id) makes each pass drain the oldest
// backlog first and be a stable, deterministic batch. An empty result is not an
// error: it means the backlog is drained and the worker sleeps.
func (s *Store) ListUnembeddedChunks(ctx context.Context, limit int) ([]TranscriptChunk, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+transcriptChunkColumns+`
		   FROM transcript_chunk
		  WHERE embedding IS NULL
		  ORDER BY created_at, id
		  LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("storage: list unembedded transcript chunks: %w", err)
	}
	defer rows.Close()

	var out []TranscriptChunk
	for rows.Next() {
		c, err := scanTranscriptChunk(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan transcript chunk: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list unembedded transcript chunks: %w", err)
	}
	return out, nil
}

// SetChunkEmbedding fills one chunk's embedding vector and stamps the model that
// produced it (#116, ADR-0011). The vector is passed as pgvector's text form and
// cast server-side (::vector) so storage carries no pgvector-go dependency; the
// column is vector(768), so a wrong-length vector is rejected by Postgres. Once
// set, the row leaves the NULL-embedding backlog and becomes returnable by the
// embedding-filtered retrieval query (embedding IS NOT NULL).
func (s *Store) SetChunkEmbedding(ctx context.Context, id uuid.UUID, vec []float32, model string) error {
	if _, err := s.db.Exec(ctx,
		`UPDATE transcript_chunk
		    SET embedding = $2::vector, embedding_model = $3
		  WHERE id = $1`, id, encodeVector(vec), model); err != nil {
		return fmt.Errorf("storage: set chunk embedding %s: %w", id, err)
	}
	return nil
}

// encodeVector renders a float32 vector as pgvector's text input form,
// "[0.1,0.2,...]", for a server-side ::vector cast. Each element uses the
// shortest round-trippable float32 decimal ('g', 32-bit) so the stored value is
// exactly what was embedded. Keeping this a plain string keeps the storage layer
// free of a pgvector-go binding.
func encodeVector(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// ChunkMatch is one Transcript Chunk returned by ANN retrieval together with its
// cosine distance to the query vector (#119, ADR-0011). Distance comes from
// pgvector's <=> operator: smaller = nearer, ascending order = nearest first.
type ChunkMatch struct {
	Chunk    TranscriptChunk
	Distance float64
}

func scanChunkMatch(row pgx.Row) (ChunkMatch, error) {
	var (
		m    ChunkMatch
		vsID uuid.NullUUID // column nullable (ADR-0011 SEAM); may be NULL
	)
	err := row.Scan(
		&m.Chunk.ID, &m.Chunk.CampaignID, &vsID, &m.Chunk.Content,
		&m.Chunk.SpeakerDiscordUserIDs, &m.Chunk.ParticipatedAgentIDs,
		&m.Chunk.EmbeddingModel, &m.Chunk.StartedAt, &m.Chunk.CreatedAt,
		&m.Distance,
	)
	m.Chunk.VoiceSessionID = vsID.UUID // uuid.Nil when NULL
	return m, err
}

// SearchChunksByAgent is NPC-knowledge retrieval (ADR-0011): the k Transcript
// Chunks in campaignID nearest the query vector by cosine distance whose
// participated set CONTAINS agentID — "what this NPC could personally know". A
// multi-agent chunk is returned for every one of its participants (containment,
// not equality). NULL-embedding rows are excluded (partial HNSW index).
func (s *Store) SearchChunksByAgent(ctx context.Context, campaignID, agentID uuid.UUID, query []float32, k int) ([]ChunkMatch, error) {
	return s.searchChunks(ctx, campaignID, &agentID, query, k)
}

// SearchChunksByCampaign is world-context retrieval (ADR-0011): the k Transcript
// Chunks in campaignID nearest the query vector by cosine distance, participants
// ignored — topical Campaign context the NPC "may not personally know".
// NULL-embedding rows are excluded (partial HNSW index).
func (s *Store) SearchChunksByCampaign(ctx context.Context, campaignID uuid.UUID, query []float32, k int) ([]ChunkMatch, error) {
	return s.searchChunks(ctx, campaignID, nil, query, k)
}

// searchChunks runs the shared ANN query for both retrieval modes: cosine
// distance (<=>) against the query vector, ascending (nearest first), matching
// the partial HNSW vector_cosine_ops index. It is always scoped to one Campaign
// and to non-null embeddings; a non-nil agentID adds the participated-set
// containment filter (NPC-knowledge mode). k <= 0 is a caller bug and errors
// rather than silently defaulting. The query vector reuses encodeVector + a
// server-side ::vector cast, so storage carries no pgvector-go dependency.
func (s *Store) searchChunks(ctx context.Context, campaignID uuid.UUID, agentID *uuid.UUID, query []float32, k int) ([]ChunkMatch, error) {
	if k <= 0 {
		return nil, fmt.Errorf("storage: search chunks: k must be > 0, got %d", k)
	}
	// $1 campaign, $2 query vector (also the <=> operand), [$3 agent], $N limit.
	args := []any{campaignID, encodeVector(query)}
	agentFilter := ""
	if agentID != nil {
		args = append(args, *agentID)
		agentFilter = " AND participated_agent_ids @> ARRAY[$3]::uuid[]"
	}
	args = append(args, k)
	sql := `SELECT ` + transcriptChunkColumns + `, embedding <=> $2::vector AS distance
		   FROM transcript_chunk
		  WHERE campaign_id = $1 AND embedding IS NOT NULL` + agentFilter + `
		  ORDER BY distance
		  LIMIT $` + strconv.Itoa(len(args))

	rows, err := s.db.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: search transcript chunks: %w", err)
	}
	defer rows.Close()

	var out []ChunkMatch
	for rows.Next() {
		m, err := scanChunkMatch(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan chunk match: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: search transcript chunks: %w", err)
	}
	return out, nil
}

// GetEmbeddingsProviderConfig returns the most-recently-updated 'embeddings'
// Provider Config, or ErrNotFound when none is bound. Process-wide (no tenant
// filter), mirroring GetActiveCampaign's single-operator posture (ADR-0039): the
// backfill worker resolves ONE embeddings provider for the process. Not-found is
// not fatal — the worker falls back to the local Ollama default (ADR-0004/0011).
func (s *Store) GetEmbeddingsProviderConfig(ctx context.Context) (ProviderConfig, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+providerConfigColumns+`
		   FROM provider_config
		  WHERE component = 'embeddings'
		  ORDER BY updated_at DESC, id DESC
		  LIMIT 1`)
	p, err := scanProviderConfig(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProviderConfig{}, ErrNotFound
	}
	if err != nil {
		return ProviderConfig{}, fmt.Errorf("storage: get embeddings provider_config: %w", err)
	}
	return p, nil
}
