//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestVoiceSessionLifecycle round-trips the #72 Voice Session lifecycle against a
// real Postgres: Create writes a running row, End sets ended_at + status='ended'
// + line_count, and the reads (Get / GetLatest) reflect each state. It also
// proves the 00006 migration's transcript_chunk FK seam: a transcript chunk can
// point at the session, and dropping is additive (the migration up+down is
// exercised by MigrateUp inside seedCampaign).
func TestVoiceSessionLifecycle(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// No session yet → GetLatest is ErrNotFound (the idle, never-run state).
	if _, err := st.GetLatestVoiceSession(ctx, campaignID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetLatestVoiceSession on empty = %v, want ErrNotFound", err)
	}

	// Start: a running row with no ended_at.
	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}
	if vs.Status != storage.VoiceSessionRunning {
		t.Errorf("status = %q, want running", vs.Status)
	}
	if vs.EndedAt != nil {
		t.Errorf("ended_at = %v, want nil while running", vs.EndedAt)
	}
	if vs.CampaignID != campaignID {
		t.Errorf("campaign_id = %s, want %s", vs.CampaignID, campaignID)
	}

	// While running, it is the latest session and reads back identically.
	got, err := st.GetVoiceSession(ctx, vs.ID)
	if err != nil {
		t.Fatalf("GetVoiceSession: %v", err)
	}
	if got.ID != vs.ID || got.Status != storage.VoiceSessionRunning {
		t.Errorf("GetVoiceSession = %+v, want running %s", got, vs.ID)
	}

	// The transcript_chunk SEAM FK (00006): a chunk may reference the session.
	if _, err := pool.Exec(ctx,
		`INSERT INTO transcript_chunk (campaign_id, voice_session_id, content)
		 VALUES ($1, $2, 'hello')`, campaignID, vs.ID); err != nil {
		t.Fatalf("insert transcript chunk referencing session: %v", err)
	}

	// Stop: ended_at set, status ended, line_count recorded.
	ended, err := st.EndVoiceSession(ctx, vs.ID, 7)
	if err != nil {
		t.Fatalf("EndVoiceSession: %v", err)
	}
	if ended.Status != storage.VoiceSessionEnded {
		t.Errorf("ended status = %q, want ended", ended.Status)
	}
	if ended.EndedAt == nil {
		t.Fatal("ended_at is nil after End")
	}
	if ended.LineCount != 7 {
		t.Errorf("line_count = %d, want 7", ended.LineCount)
	}

	// GetLatest now returns the ended session (the idle summary source).
	latest, err := st.GetLatestVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("GetLatestVoiceSession after end: %v", err)
	}
	if latest.ID != vs.ID || latest.Status != storage.VoiceSessionEnded || latest.LineCount != 7 {
		t.Errorf("GetLatestVoiceSession = %+v, want ended %s with 7 lines", latest, vs.ID)
	}
}

// TestEndVoiceSessionNotFound asserts ending an unknown id is ErrNotFound (the
// RPC maps it accordingly).
func TestEndVoiceSessionNotFound(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	if _, err := st.EndVoiceSession(ctx, uuid.New(), 0); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("EndVoiceSession(random) = %v, want ErrNotFound", err)
	}
	if _, err := st.GetVoiceSession(ctx, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetVoiceSession(random) = %v, want ErrNotFound", err)
	}
}
