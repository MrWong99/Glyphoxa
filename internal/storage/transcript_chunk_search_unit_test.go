package storage_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestSearchChunks_KMustBePositiveNoDB pins the caller-bug guard (#119): k<=0
// errors before any DB access, so it is asserted here Docker-free against a
// nil-pool Store — the check must reject the bad k without touching s.db.
func TestSearchChunks_KMustBePositiveNoDB(t *testing.T) {
	st := storage.New(nil)
	ctx := context.Background()
	campaignID, agentID := uuid.New(), uuid.New()
	query := []float32{1, 0}

	for _, k := range []int{0, -1} {
		if _, err := st.SearchChunksByCampaign(ctx, campaignID, query, k); err == nil {
			t.Errorf("SearchChunksByCampaign k=%d returned nil error", k)
		}
		if _, err := st.SearchChunksByAgent(ctx, campaignID, agentID, query, k); err == nil {
			t.Errorf("SearchChunksByAgent k=%d returned nil error", k)
		}
	}
}
