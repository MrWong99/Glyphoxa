//go:build integration

package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// seedSearchHighlight inserts one highlight row with the given status and text.
func seedSearchHighlight(t *testing.T, st *storage.Store, tenantID, campaignID, vsID uuid.UUID, status, excerpt, reason string, startsAt time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if err := st.CreateHighlight(context.Background(), storage.Highlight{
		ID: id, TenantID: tenantID, VoiceSessionID: vsID, CampaignID: campaignID,
		Status: status, StartsAt: startsAt, EndsAt: startsAt.Add(20 * time.Second),
		Score: 8.5, Excerpt: excerpt, Reason: reason, ClipKey: "clip/" + id.String(),
	}); err != nil {
		t.Fatalf("CreateHighlight: %v", err)
	}
	return id
}

// TestSearchPromotedHighlights is #591: the palette's Highlight search matches
// excerpt + reason (excerpt weighted above reason), is tenant- AND
// campaign-scoped, and NEVER returns a candidate — the promoted-only filter
// lives in the query, so candidates cannot starve promoted matches through the
// LIMIT either. An all-punctuation query is a no-op, not an error.
func TestSearchPromotedHighlights(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}
	at := time.Date(2026, 8, 1, 20, 0, 0, 0, time.UTC)

	// Promoted rows: the term in the excerpt (weight A) must outrank the term
	// only in the reason (weight B).
	inExcerpt := seedSearchHighlight(t, st, tenantID, campaignID, vs.ID,
		storage.HighlightPromoted, "the dragon hoard negotiation", "table erupted", at)
	inReason := seedSearchHighlight(t, st, tenantID, campaignID, vs.ID,
		storage.HighlightPromoted, "an unrelated moment", "everyone chanted dragon dragon", at.Add(time.Minute))
	// A matching CANDIDATE must never surface (GM curation stays private).
	seedSearchHighlight(t, st, tenantID, campaignID, vs.ID,
		storage.HighlightCandidate, "the dragon candidate moment", "pending review", at.Add(2*time.Minute))

	got, err := st.SearchPromotedHighlights(ctx, tenantID, campaignID, "dragon", 10)
	if err != nil {
		t.Fatalf("SearchPromotedHighlights: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2 (both promoted, never the candidate)", len(got))
	}
	if got[0].ID != inExcerpt || got[1].ID != inReason {
		t.Fatalf("rank order = [%s %s], want excerpt hit above reason hit", got[0].Excerpt, got[1].Excerpt)
	}

	// Campaign scope: an identical promoted match in a SECOND campaign of the
	// same tenant never leaks into this campaign's results.
	var otherCampaign uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name, system, language)
		 VALUES ($1, 'Second Front', 'dnd5e', 'en') RETURNING id`, tenantID).
		Scan(&otherCampaign); err != nil {
		t.Fatalf("insert second campaign: %v", err)
	}
	otherVS, err := st.CreateVoiceSession(ctx, otherCampaign)
	if err != nil {
		t.Fatalf("CreateVoiceSession (other): %v", err)
	}
	seedSearchHighlight(t, st, tenantID, otherCampaign, otherVS.ID,
		storage.HighlightPromoted, "the dragon hoard negotiation", "identical text", at)

	got, err = st.SearchPromotedHighlights(ctx, tenantID, campaignID, "dragon", 10)
	if err != nil {
		t.Fatalf("SearchPromotedHighlights (scope): %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("cross-campaign leak: got %d rows, want 2", len(got))
	}
	for _, h := range got {
		if h.CampaignID != campaignID {
			t.Fatalf("row %s belongs to campaign %s, not the searched %s", h.ID, h.CampaignID, campaignID)
		}
	}

	// Foreign tenant: same campaign id under a different tenant id yields nothing.
	if got, err := st.SearchPromotedHighlights(ctx, uuid.New(), campaignID, "dragon", 10); err != nil || len(got) != 0 {
		t.Fatalf("foreign-tenant search = (%v, %v), want empty", got, err)
	}

	// Sanitizer no-op: an all-punctuation query yields (nil, nil).
	if got, err := st.SearchPromotedHighlights(ctx, tenantID, campaignID, "&&& ((( |||", 10); err != nil || got != nil {
		t.Fatalf("punctuation query = (%v, %v), want (nil, nil)", got, err)
	}
}

// TestFirstLineIDAtOrAfter is the semantic hit's deep-link anchor (#591): the
// earliest Line at/after the chunk's start, (ts, seq)-ordered for a
// deterministic pick among equal timestamps, ErrNotFound past the last Line,
// and never a line from ANOTHER session.
func TestFirstLineIDAtOrAfter(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}
	other, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession (other): %v", err)
	}

	ts := func(sec int) time.Time { return time.Date(2026, 8, 2, 19, 0, sec, 0, time.UTC) }
	seedLine(t, st, vs.ID, campaignID, "u:1", 1, ts(0), "before the chunk")
	seedLine(t, st, vs.ID, campaignID, "u:2", 2, ts(10), "first line of the chunk")
	seedLine(t, st, vs.ID, campaignID, "a:1", 3, ts(10), "same-timestamp agent reply")
	// The other session's line sits exactly at the anchor time — it must never win.
	seedLine(t, st, other.ID, campaignID, "u:1", 1, ts(10), "another session's line")

	got, err := st.FirstLineIDAtOrAfter(ctx, vs.ID, ts(5))
	if err != nil {
		t.Fatalf("FirstLineIDAtOrAfter: %v", err)
	}
	if got != "u:2" {
		t.Fatalf("anchor = %q, want u:2 (earliest at/after, lowest seq among equals)", got)
	}

	// Past the last Line: ErrNotFound, the caller renders without a scroll target.
	if _, err := st.FirstLineIDAtOrAfter(ctx, vs.ID, ts(60)); err != storage.ErrNotFound {
		t.Fatalf("past-the-end anchor err = %v, want ErrNotFound", err)
	}
}
