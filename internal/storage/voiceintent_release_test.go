//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestReleaseVoiceSessionIntent (S13): a claimed intent whose worker shut down
// before starting it goes back to 'pending' for another worker — fenced to the
// claiming instance, and never once the GM asked to stop.
func TestReleaseVoiceSessionIntent(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	if _, err := st.CreateVoiceSessionIntent(ctx, tenantID, campaignID, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	claimed, err := st.ClaimVoiceSessionIntent(ctx, "worker-1")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := st.ReleaseVoiceSessionIntent(ctx, claimed.ID, "worker-2"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("release by a foreign instance err = %v, want ErrNotFound", err)
	}
	if err := st.ReleaseVoiceSessionIntent(ctx, claimed.ID, "worker-1"); err != nil {
		t.Fatalf("release by the owner: %v", err)
	}

	reclaimed, err := st.ClaimVoiceSessionIntent(ctx, "worker-2")
	if err != nil {
		t.Fatalf("re-claim after release: %v", err)
	}
	if reclaimed.ID != claimed.ID || reclaimed.InstanceID != "worker-2" {
		t.Fatalf("re-claimed %+v, want intent %s held by worker-2", reclaimed, claimed.ID)
	}

	// A stop requested meanwhile wins: the intent is not handed out again.
	if _, err := st.RequestVoiceSessionStop(ctx, reclaimed.ID); err != nil {
		t.Fatalf("request stop: %v", err)
	}
	if err := st.ReleaseVoiceSessionIntent(ctx, reclaimed.ID, "worker-2"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("release after a stop request err = %v, want ErrNotFound", err)
	}
}
