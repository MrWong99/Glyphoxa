//go:build integration

package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/highlight"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// enrichJobPayload marshals the enrich job payload the reconciliation query
// matches on (highlight_id).
func enrichJobPayload(t *testing.T, highlightID, tenantID uuid.UUID) []byte {
	t.Helper()
	b, err := highlight.MarshalEnrichImage(highlightID, tenantID)
	if err != nil {
		t.Fatalf("marshal enrich payload: %v", err)
	}
	return b
}

// TestListPromotedHighlightsNeedingEnrichment_SelectsOnlyImagelessPromotedWithNoLiveJob
// pins the (a)-half query of the boot reconciliation sweep (#406): only PROMOTED,
// image_key=” Highlights with no pending/running/done enrich job are returned.
func TestListPromotedHighlightsNeedingEnrichment_SelectsOnlyImagelessPromotedWithNoLiveJob(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("create voice session: %v", err)
	}

	// (want) promoted, imageless, no job → a target.
	wantID, _ := seedHighlight(t, st, tenantID, vs.ID, campaignID, storage.HighlightCandidate)
	if _, err := st.PromoteHighlight(ctx, tenantID, wantID); err != nil {
		t.Fatalf("promote want: %v", err)
	}

	// promoted, imageless, but a pending enrich job exists → excluded.
	hasJobID, _ := seedHighlight(t, st, tenantID, vs.ID, campaignID, storage.HighlightCandidate)
	if _, err := st.PromoteHighlight(ctx, tenantID, hasJobID); err != nil {
		t.Fatalf("promote hasJob: %v", err)
	}
	if _, err := st.EnqueueJob(ctx, highlight.JobKindEnrichImage, enrichJobPayload(t, hasJobID, tenantID), 0); err != nil {
		t.Fatalf("enqueue enrich job: %v", err)
	}

	// promoted but already enriched → excluded.
	enrichedID, _ := seedHighlight(t, st, tenantID, vs.ID, campaignID, storage.HighlightCandidate)
	if _, err := st.PromoteHighlight(ctx, tenantID, enrichedID); err != nil {
		t.Fatalf("promote enriched: %v", err)
	}
	imgKey := "t/" + tenantID.String() + "/highlight/" + enrichedID.String() + "/image"
	if err := st.SetHighlightImage(ctx, enrichedID, imgKey, "image/png", 10); err != nil {
		t.Fatalf("set image: %v", err)
	}

	// candidate (never promoted) → excluded.
	seedHighlight(t, st, tenantID, vs.ID, campaignID, storage.HighlightCandidate)

	got, err := st.ListPromotedHighlightsNeedingEnrichment(ctx, highlight.JobKindEnrichImage)
	if err != nil {
		t.Fatalf("list needing enrichment: %v", err)
	}
	if len(got) != 1 || got[0].HighlightID != wantID || got[0].TenantID != tenantID {
		t.Fatalf("want exactly the imageless-promoted-no-job target %s, got %+v", wantID, got)
	}
}

// TestTryClaimHighlightEnrich_ConditionalTransition pins the race-proof claim
// (#406): the first claim wins, a second fresh claim loses, a release re-opens it,
// and an already-imaged row is unclaimable.
func TestTryClaimHighlightEnrich_ConditionalTransition(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("create voice session: %v", err)
	}
	id, _ := seedHighlight(t, st, tenantID, vs.ID, campaignID, storage.HighlightPromoted)

	// First claim wins.
	won, err := st.TryClaimHighlightEnrich(ctx, id, time.Hour)
	if err != nil || !won {
		t.Fatalf("first claim: won=%v err=%v; want won", won, err)
	}
	// Second, within the lease, loses (a concurrent worker holds it).
	won, err = st.TryClaimHighlightEnrich(ctx, id, time.Hour)
	if err != nil || won {
		t.Fatalf("second claim: won=%v err=%v; want lost", won, err)
	}
	// Release re-opens the claim.
	if err := st.ReleaseHighlightEnrichClaim(ctx, id); err != nil {
		t.Fatalf("release: %v", err)
	}
	won, err = st.TryClaimHighlightEnrich(ctx, id, time.Hour)
	if err != nil || !won {
		t.Fatalf("post-release claim: won=%v err=%v; want won", won, err)
	}
	// A stale claim (ttl already elapsed) is reclaimable.
	won, err = st.TryClaimHighlightEnrich(ctx, id, time.Nanosecond)
	if err != nil || !won {
		t.Fatalf("stale-claim reclaim: won=%v err=%v; want won", won, err)
	}

	// Once enriched (image_key set), the row is unclaimable — no double spend.
	imgKey := "t/" + tenantID.String() + "/highlight/" + id.String() + "/image"
	if err := st.SetHighlightImage(ctx, id, imgKey, "image/png", 10); err != nil {
		t.Fatalf("set image: %v", err)
	}
	won, err = st.TryClaimHighlightEnrich(ctx, id, time.Hour)
	if err != nil || won {
		t.Fatalf("claim on enriched row: won=%v err=%v; want lost", won, err)
	}
}

// TestHighlightsExist_ReportsSurvivingRows pins the membership half of the boot
// orphan-image sweep's anti-join (#421): given a mix of ids, only those with a
// surviving highlight row come back present; a deleted/never-existed id is absent.
// The anti-join itself (candidate blobs − present rows) runs in the sweep, in Go.
func TestHighlightsExist_ReportsSurvivingRows(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("create voice session: %v", err)
	}

	liveID, _ := seedHighlight(t, st, tenantID, vs.ID, campaignID, storage.HighlightPromoted)
	goneID := uuid.New() // no row was ever created

	present, err := st.HighlightsExist(ctx, []uuid.UUID{liveID, goneID})
	if err != nil {
		t.Fatalf("highlights exist: %v", err)
	}
	if !present[liveID] {
		t.Fatalf("live highlight %s reported absent", liveID)
	}
	if present[goneID] {
		t.Fatalf("row-less id %s reported present", goneID)
	}
	if len(present) != 1 {
		t.Fatalf("want exactly the surviving id in the set, got %v", present)
	}

	// Empty input runs no query and returns an empty set.
	empty, err := st.HighlightsExist(ctx, nil)
	if err != nil {
		t.Fatalf("highlights exist (empty): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty input should yield an empty set, got %v", empty)
	}
}
