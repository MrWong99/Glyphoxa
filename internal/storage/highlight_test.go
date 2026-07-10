//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// seedHighlight creates a candidate highlight row for the given session and
// returns its id + clip key.
func seedHighlight(t *testing.T, st *storage.Store, tenantID, sessionID, campaignID uuid.UUID, status string) (uuid.UUID, string) {
	t.Helper()
	id := uuid.New()
	key := "t/" + tenantID.String() + "/highlight/" + id.String() + "/clip.wav"
	h := storage.Highlight{
		ID:              id,
		TenantID:        tenantID,
		VoiceSessionID:  sessionID,
		CampaignID:      campaignID,
		Status:          status,
		StartsAt:        time.Now().Add(-15 * time.Second),
		EndsAt:          time.Now().Add(5 * time.Second),
		Score:           9.0,
		Excerpt:         "natural 20 against the dragon",
		Reason:          "critical hit",
		SpeakerIDs:      []string{"111", "222"},
		ClipKey:         key,
		ClipContentType: "audio/wav",
		ClipSizeBytes:   4444,
	}
	if err := st.CreateHighlight(context.Background(), h); err != nil {
		t.Fatalf("create highlight: %v", err)
	}
	return id, key
}

func TestHighlight_CreateGetList_TenantScoped(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("create voice session: %v", err)
	}

	id, key := seedHighlight(t, st, tenantID, vs.ID, campaignID, storage.HighlightCandidate)

	got, err := st.GetHighlight(ctx, tenantID, id)
	if err != nil {
		t.Fatalf("get highlight: %v", err)
	}
	if got.Status != storage.HighlightCandidate || got.ClipKey != key {
		t.Fatalf("round-trip mismatch: status=%q key=%q", got.Status, got.ClipKey)
	}
	if len(got.SpeakerIDs) != 2 || got.Excerpt != "natural 20 against the dragon" {
		t.Fatalf("field mismatch: %+v", got)
	}
	if got.PromotedAt != nil {
		t.Fatalf("candidate should have nil promoted_at, got %v", got.PromotedAt)
	}

	// Foreign tenant reads as absent.
	if _, err := st.GetHighlight(ctx, uuid.New(), id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("foreign tenant get: want ErrNotFound, got %v", err)
	}

	list, err := st.ListHighlights(ctx, tenantID, vs.ID)
	if err != nil {
		t.Fatalf("list highlights: %v", err)
	}
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("list mismatch: %+v", list)
	}
	// Foreign tenant lists empty.
	if l, err := st.ListHighlights(ctx, uuid.New(), vs.ID); err != nil || len(l) != 0 {
		t.Fatalf("foreign tenant list: want empty, got %v err=%v", l, err)
	}
}

func TestHighlight_Promote_Idempotent(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, _ := st.CreateVoiceSession(ctx, campaignID)
	id, _ := seedHighlight(t, st, tenantID, vs.ID, campaignID, storage.HighlightCandidate)

	promoted, err := st.PromoteHighlight(ctx, tenantID, id)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if promoted.Status != storage.HighlightPromoted || promoted.PromotedAt == nil {
		t.Fatalf("promote did not flip status/stamp: %+v", promoted)
	}
	firstStamp := *promoted.PromotedAt

	// Double-promote keeps the original stamp (idempotent).
	again, err := st.PromoteHighlight(ctx, tenantID, id)
	if err != nil {
		t.Fatalf("re-promote: %v", err)
	}
	if !again.PromotedAt.Equal(firstStamp) {
		t.Fatalf("re-promote changed the stamp: %v != %v", again.PromotedAt, firstStamp)
	}

	// Missing id is ErrNotFound.
	if _, err := st.PromoteHighlight(ctx, tenantID, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("promote missing: want ErrNotFound, got %v", err)
	}
}

func TestHighlight_Delete_ReturnsClipKey(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, _ := st.CreateVoiceSession(ctx, campaignID)
	id, key := seedHighlight(t, st, tenantID, vs.ID, campaignID, storage.HighlightCandidate)

	gotKey, err := st.DeleteHighlight(ctx, tenantID, id)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if gotKey != key {
		t.Fatalf("delete returned key %q, want %q", gotKey, key)
	}
	if _, err := st.GetHighlight(ctx, tenantID, id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("get after delete: want ErrNotFound, got %v", err)
	}
	// Double-delete is ErrNotFound.
	if _, err := st.DeleteHighlight(ctx, tenantID, id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("double delete: want ErrNotFound, got %v", err)
	}
}

func TestHighlight_SessionCandidateSweep(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, _ := st.CreateVoiceSession(ctx, campaignID)
	candID, candKey := seedHighlight(t, st, tenantID, vs.ID, campaignID, storage.HighlightCandidate)
	promID, _ := seedHighlight(t, st, tenantID, vs.ID, campaignID, storage.HighlightPromoted)

	keys, err := st.ListSessionCandidateClipKeys(ctx, vs.ID)
	if err != nil {
		t.Fatalf("list candidate keys: %v", err)
	}
	if len(keys) != 1 || keys[0] != candKey {
		t.Fatalf("candidate keys mismatch: %v", keys)
	}

	n, err := st.DeleteSessionCandidates(ctx, vs.ID)
	if err != nil {
		t.Fatalf("delete candidates: %v", err)
	}
	if n != 1 {
		t.Fatalf("deleted %d candidates, want 1", n)
	}
	// Candidate gone, promoted untouched.
	if _, err := st.GetHighlight(ctx, tenantID, candID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("candidate should be purged, got %v", err)
	}
	if _, err := st.GetHighlight(ctx, tenantID, promID); err != nil {
		t.Fatalf("promoted should survive: %v", err)
	}
	// Idempotent.
	if n, err := st.DeleteSessionCandidates(ctx, vs.ID); err != nil || n != 0 {
		t.Fatalf("second sweep: want 0/nil, got %d/%v", n, err)
	}
}

// TestHighlight_ListSessionsNeedingCandidatePurge is the boot-backstop query
// (#308, ADR-0051): an ENDED session with candidates and NO live purge job is
// listed; a session that already has a pending purge job is NOT; a still-RUNNING
// session is NOT; a session whose only highlights are promoted is NOT.
func TestHighlight_ListSessionsNeedingCandidatePurge(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	const purgeKind = "highlight.purge_candidates"

	// Orphan: ended, has a candidate, no purge job → must be listed.
	orphan, _ := st.CreateVoiceSession(ctx, campaignID)
	seedHighlight(t, st, tenantID, orphan.ID, campaignID, storage.HighlightCandidate)
	if _, err := st.EndVoiceSession(ctx, orphan.ID, 0); err != nil {
		t.Fatalf("end orphan: %v", err)
	}

	// Already scheduled: ended, has a candidate, but a pending purge job exists → NOT listed.
	scheduled, _ := st.CreateVoiceSession(ctx, campaignID)
	seedHighlight(t, st, tenantID, scheduled.ID, campaignID, storage.HighlightCandidate)
	if _, err := st.EndVoiceSession(ctx, scheduled.ID, 0); err != nil {
		t.Fatalf("end scheduled: %v", err)
	}
	payload := []byte(`{"voice_session_id":"` + scheduled.ID.String() + `"}`)
	if _, err := st.EnqueueJob(ctx, purgeKind, payload, 0); err != nil {
		t.Fatalf("enqueue existing purge: %v", err)
	}

	// Running: has a candidate but not ended → NOT listed (Finalize will schedule it).
	running, _ := st.CreateVoiceSession(ctx, campaignID)
	seedHighlight(t, st, tenantID, running.ID, campaignID, storage.HighlightCandidate)

	// Promoted-only: ended but its only highlight is promoted (kept) → NOT listed.
	promotedOnly, _ := st.CreateVoiceSession(ctx, campaignID)
	seedHighlight(t, st, tenantID, promotedOnly.ID, campaignID, storage.HighlightPromoted)
	if _, err := st.EndVoiceSession(ctx, promotedOnly.ID, 0); err != nil {
		t.Fatalf("end promotedOnly: %v", err)
	}

	got, err := st.ListSessionsNeedingCandidatePurge(ctx, purgeKind)
	if err != nil {
		t.Fatalf("list sessions needing purge: %v", err)
	}
	if len(got) != 1 || got[0].VoiceSessionID != orphan.ID {
		t.Fatalf("want only the orphan session %s, got %+v", orphan.ID, got)
	}
	// ended_at is returned so the boot sweep anchors the 7-day horizon at session end.
	if got[0].EndedAt.IsZero() {
		t.Fatalf("orphan candidate carries no ended_at: %+v", got[0])
	}
}

func TestHighlight_CampaignClipKeySweep(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, _ := st.CreateVoiceSession(ctx, campaignID)
	_, k1 := seedHighlight(t, st, tenantID, vs.ID, campaignID, storage.HighlightCandidate)
	_, k2 := seedHighlight(t, st, tenantID, vs.ID, campaignID, storage.HighlightPromoted)

	keys, err := st.ListCampaignHighlightClipKeys(ctx, campaignID)
	if err != nil {
		t.Fatalf("list campaign keys: %v", err)
	}
	// Both candidate AND promoted (a campaign delete takes them all).
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	if !set[k1] || !set[k2] || len(keys) != 2 {
		t.Fatalf("campaign clip keys mismatch: %v", keys)
	}
}
