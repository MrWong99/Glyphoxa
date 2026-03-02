package session

import (
	"github.com/google/uuid"

	"github.com/MrWong99/glyphoxa/pkg/memory"
)

// ChunkEntries converts transcript entries into chunks suitable for L2
// semantic indexing. Each non-empty TranscriptEntry produces exactly one Chunk.
//
// Entries with empty Text are skipped (silence markers, etc.).
// The returned chunks have no Embedding set — the caller is responsible for
// embedding before calling SemanticIndex.IndexChunk.
func ChunkEntries(sessionID string, entries []memory.TranscriptEntry) []memory.Chunk {
	chunks := make([]memory.Chunk, 0, len(entries))
	for _, e := range entries {
		if e.Text == "" {
			continue
		}
		chunks = append(chunks, memory.Chunk{
			ID:        uuid.NewString(),
			SessionID: sessionID,
			Content:   e.Text,
			SpeakerID: e.SpeakerID,
			EntityID:  e.NPCID, // empty for player entries
			Timestamp: e.Timestamp,
		})
	}
	return chunks
}
