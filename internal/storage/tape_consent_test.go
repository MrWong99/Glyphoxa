//go:build integration

package storage_test

import (
	"context"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestTapeConsentRoundTrip proves the consent CRUD against a real Postgres (#306):
// upsert adds a Speaker, list returns them, upsert is idempotent, and delete
// revokes — leaving the set empty. Both upsert and delete are idempotent so a
// repeated button press is harmless.
func TestTapeConsentRoundTrip(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// Empty to start.
	got, err := st.ListTapeConsent(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListTapeConsent (empty): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("consent set = %v, want empty", got)
	}

	// Two Speakers consent.
	for _, id := range []string{"111", "222"} {
		if err := st.UpsertTapeConsent(ctx, campaignID, id); err != nil {
			t.Fatalf("UpsertTapeConsent(%s): %v", id, err)
		}
	}
	// Idempotent: consenting again keeps one row.
	if err := st.UpsertTapeConsent(ctx, campaignID, "111"); err != nil {
		t.Fatalf("UpsertTapeConsent (repeat): %v", err)
	}

	got, err = st.ListTapeConsent(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListTapeConsent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("consent set = %v, want two speakers", got)
	}

	// Revoke one.
	if err := st.DeleteTapeConsent(ctx, campaignID, "111"); err != nil {
		t.Fatalf("DeleteTapeConsent: %v", err)
	}
	// Idempotent: revoking an absent row is a no-op.
	if err := st.DeleteTapeConsent(ctx, campaignID, "111"); err != nil {
		t.Fatalf("DeleteTapeConsent (repeat): %v", err)
	}

	got, err = st.ListTapeConsent(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListTapeConsent (after revoke): %v", err)
	}
	if len(got) != 1 || got[0] != "222" {
		t.Fatalf("consent set after revoke = %v, want [222]", got)
	}
}

// TestUpdateCampaignTapeArmed pins the opt-in persistence (#306): tape_armed
// defaults false, an UpdateCampaign that sets it persists and is returned, and an
// UpdateCampaign that leaves it nil does NOT disarm it (optional-field semantics).
func TestUpdateCampaignTapeArmed(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// Default OFF (ADR-0051).
	c, err := st.GetCampaign(ctx, campaignID)
	if err != nil {
		t.Fatalf("GetCampaign: %v", err)
	}
	if c.TapeArmed {
		t.Fatalf("tape_armed default = true, want false (ADR-0051 default OFF)")
	}

	// Arm it.
	armed := true
	updated, err := st.UpdateCampaign(ctx, storage.CampaignUpdate{
		ID: campaignID, Name: c.Name, System: c.System, Language: c.Language,
		TapeArmed: &armed,
	})
	if err != nil {
		t.Fatalf("UpdateCampaign (arm): %v", err)
	}
	if !updated.TapeArmed {
		t.Fatalf("tape_armed after arm = false, want true")
	}

	// A subsequent update that leaves TapeArmed nil must not disarm it.
	updated, err = st.UpdateCampaign(ctx, storage.CampaignUpdate{
		ID: campaignID, Name: "Renamed", System: c.System, Language: c.Language,
	})
	if err != nil {
		t.Fatalf("UpdateCampaign (rename): %v", err)
	}
	if !updated.TapeArmed {
		t.Fatalf("tape_armed after nil update = false, want true (optional field must not disarm)")
	}

	// Disarm explicitly.
	disarmed := false
	updated, err = st.UpdateCampaign(ctx, storage.CampaignUpdate{
		ID: campaignID, Name: updated.Name, System: c.System, Language: c.Language,
		TapeArmed: &disarmed,
	})
	if err != nil {
		t.Fatalf("UpdateCampaign (disarm): %v", err)
	}
	if updated.TapeArmed {
		t.Fatalf("tape_armed after disarm = true, want false")
	}

	// Consent rows cascade away with the campaign (#265 delete semantics).
	if err := st.UpsertTapeConsent(ctx, campaignID, "111"); err != nil {
		t.Fatalf("UpsertTapeConsent: %v", err)
	}
	if _, err := st.ArchiveCampaign(ctx, campaignID); err != nil {
		t.Fatalf("ArchiveCampaign: %v", err)
	}
	if err := st.DeleteCampaign(ctx, campaignID); err != nil {
		t.Fatalf("DeleteCampaign: %v", err)
	}
	if _, err := st.GetCampaign(ctx, campaignID); err == nil {
		t.Fatalf("campaign still present after delete")
	}
	// A fresh consent list for the deleted campaign is empty (rows cascaded).
	got, err := st.ListTapeConsent(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListTapeConsent after campaign delete: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("consent rows survived campaign delete: %v", got)
	}
}
