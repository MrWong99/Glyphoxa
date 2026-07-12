//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestCampaignShareChannel_RoundTrip proves the #310 highlight-share memory: a
// fresh campaign reports "" (never shared), a Set persists a channel id that a
// later Get reads back, and a re-Set overwrites (last-choice-wins).
func TestCampaignShareChannel_RoundTrip(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	st := storage.New(pool)
	ctx := context.Background()

	got, err := st.GetCampaignShareChannel(ctx, campaignID)
	if err != nil {
		t.Fatalf("get default: %v", err)
	}
	if got != "" {
		t.Fatalf("fresh campaign share channel = %q, want empty", got)
	}

	if err := st.SetCampaignShareChannel(ctx, campaignID, "111222333"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err = st.GetCampaignShareChannel(ctx, campaignID)
	if err != nil {
		t.Fatalf("get after set: %v", err)
	}
	if got != "111222333" {
		t.Fatalf("share channel = %q, want 111222333", got)
	}

	if err := st.SetCampaignShareChannel(ctx, campaignID, "444555666"); err != nil {
		t.Fatalf("re-set: %v", err)
	}
	got, err = st.GetCampaignShareChannel(ctx, campaignID)
	if err != nil {
		t.Fatalf("get after re-set: %v", err)
	}
	if got != "444555666" {
		t.Fatalf("share channel after re-set = %q, want 444555666", got)
	}
}

// TestCampaignShareChannel_UnknownCampaign proves an unknown id is ErrNotFound on
// both the getter and the setter, distinct from the "" never-shared state.
func TestCampaignShareChannel_UnknownCampaign(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, _ := seedCampaign(t, dsn)
	st := storage.New(pool)
	ctx := context.Background()

	if _, err := st.GetCampaignShareChannel(ctx, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("get unknown campaign err = %v, want ErrNotFound", err)
	}
	if err := st.SetCampaignShareChannel(ctx, uuid.New(), "999"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("set unknown campaign err = %v, want ErrNotFound", err)
	}
}
