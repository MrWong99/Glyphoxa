package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Transcript-chunk persistence (#104, ADR-0011): the chunk writer inserts one row
// per closed chunk (3–6 utterances) with embedding NULL; a background worker
// (#116) fills the embedding later and retrieval filters WHERE embedding IS NOT
// NULL. This CHUNK grain — the retrieval/embedding unit for NPC Hot Context — is
// distinct from the per-line transcript_line grain (ADR-0040): the two are
// independent records of the same speech and do not share rows.

// TranscriptChunk is one persisted Transcript Chunk. embedding is always NULL at
// insert (the async embedding pipeline, ADR-0011); EmbeddingModel is deferred to
// #116 (no column yet), so it is neither written nor read here and scans "".
type TranscriptChunk struct {
	ID                    uuid.UUID
	CampaignID            uuid.UUID
	VoiceSessionID        uuid.UUID   // column nullable; the writer always sets it
	Content               string      // the chunk's utterances joined "\n"
	SpeakerDiscordUserIDs []string    // empty in v1.0: anonymous STT lane (ADR-0039)
	ParticipatedAgentIDs  []uuid.UUID // Agents that spoke in the chunk (NPC-knowledge filter)
	EmbeddingModel        string      // deferred to #116; not persisted here
	StartedAt             time.Time
	CreatedAt             time.Time
}

// InsertTranscriptChunk writes one closed chunk and returns its generated id. The
// embedding is left NULL — the async pipeline fills it later (ADR-0011) — and the
// arrays default to empty (never NULL). embedding_model is deferred (#116), so it
// is not written here.
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
