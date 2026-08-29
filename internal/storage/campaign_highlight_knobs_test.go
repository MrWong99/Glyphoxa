//go:build integration

package storage_test

import (
	"context"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestUpdateCampaignHighlightKnobs pins the Session-Highlights tuning columns
// against a real Postgres (#632 follow-up): both default to 0 ("engine
// default"), an UpdateCampaign that sets them persists and is returned, one
// that leaves them nil does NOT reset them (optional-field COALESCE
// semantics), and an explicit 0 is a real write back to the default.
func TestUpdateCampaignHighlightKnobs(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// Default 0/0: an untouched campaign runs the engine defaults (8.0 / 2 via
	// highlight.Config.withDefaults).
	c, err := st.GetCampaign(ctx, campaignID)
	if err != nil {
		t.Fatalf("GetCampaign: %v", err)
	}
	if c.HighlightBar != 0 || c.HighlightConfirmWindows != 0 {
		t.Fatalf("knob defaults = %v/%v, want 0/0 (engine default)", c.HighlightBar, c.HighlightConfirmWindows)
	}

	// Set both.
	bar, cw := 4.5, 1
	updated, err := st.UpdateCampaign(ctx, storage.CampaignUpdate{
		TenantID: tenantID, ID: campaignID, Name: c.Name, System: c.System, Language: c.Language,
		HighlightBar: &bar, HighlightConfirmWindows: &cw,
	})
	if err != nil {
		t.Fatalf("UpdateCampaign (set knobs): %v", err)
	}
	if updated.HighlightBar != 4.5 || updated.HighlightConfirmWindows != 1 {
		t.Fatalf("knobs after set = %v/%v, want 4.5/1", updated.HighlightBar, updated.HighlightConfirmWindows)
	}

	// A subsequent update that leaves them nil must not reset them.
	updated, err = st.UpdateCampaign(ctx, storage.CampaignUpdate{
		TenantID: tenantID, ID: campaignID, Name: "Renamed", System: c.System, Language: c.Language,
	})
	if err != nil {
		t.Fatalf("UpdateCampaign (rename): %v", err)
	}
	if updated.HighlightBar != 4.5 || updated.HighlightConfirmWindows != 1 {
		t.Fatalf("knobs after nil update = %v/%v, want 4.5/1 (COALESCE must not reset)", updated.HighlightBar, updated.HighlightConfirmWindows)
	}

	// Explicit 0 is a real write: back to the engine default.
	zeroBar, zeroCW := 0.0, 0
	updated, err = st.UpdateCampaign(ctx, storage.CampaignUpdate{
		TenantID: tenantID, ID: campaignID, Name: updated.Name, System: c.System, Language: c.Language,
		HighlightBar: &zeroBar, HighlightConfirmWindows: &zeroCW,
	})
	if err != nil {
		t.Fatalf("UpdateCampaign (reset knobs): %v", err)
	}
	if updated.HighlightBar != 0 || updated.HighlightConfirmWindows != 0 {
		t.Fatalf("knobs after explicit 0 = %v/%v, want 0/0", updated.HighlightBar, updated.HighlightConfirmWindows)
	}
}
