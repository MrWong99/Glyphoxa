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

// TestListVoiceSessions is #270's archive read against a real Postgres:
// ListVoiceSessions returns a Campaign's Voice Sessions newest-first (started_at
// DESC, id DESC — the same tiebreak as GetLatestVoiceSession), includes the still-
// running row, honours the LIMIT, and never leaks another Campaign's sessions.
func TestListVoiceSessions(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// A second campaign whose sessions must never surface in campaignID's list.
	var otherCampaign uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name) VALUES ($1, 'Other') RETURNING id`, tenantID).
		Scan(&otherCampaign); err != nil {
		t.Fatalf("insert other campaign: %v", err)
	}

	// No session yet → empty, not an error (the never-run picker state).
	empty, err := st.ListVoiceSessions(ctx, campaignID, 50)
	if err != nil {
		t.Fatalf("ListVoiceSessions on empty: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("ListVoiceSessions on empty = %d rows, want 0", len(empty))
	}

	// Three sessions for campaignID at explicit, increasing started_at instants:
	// the first two ended, the newest still running (line_count stays 0 until close).
	base := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	mk := func(campaign uuid.UUID, started time.Time, running bool) uuid.UUID {
		var id uuid.UUID
		status := storage.VoiceSessionEnded
		if running {
			status = storage.VoiceSessionRunning
		}
		if err := pool.QueryRow(ctx,
			`INSERT INTO voice_sessions (campaign_id, started_at, status)
			 VALUES ($1, $2, $3) RETURNING id`, campaign, started, status).Scan(&id); err != nil {
			t.Fatalf("insert voice session: %v", err)
		}
		return id
	}
	oldest := mk(campaignID, base, false)
	middle := mk(campaignID, base.Add(1*time.Hour), false)
	newest := mk(campaignID, base.Add(2*time.Hour), true) // the running row
	// A newer session in the OTHER campaign — must be excluded.
	mk(otherCampaign, base.Add(3*time.Hour), false)

	// Full list: newest-first, the running row included, other campaign excluded.
	all, err := st.ListVoiceSessions(ctx, campaignID, 50)
	if err != nil {
		t.Fatalf("ListVoiceSessions: %v", err)
	}
	gotIDs := []uuid.UUID{}
	for _, v := range all {
		if v.CampaignID != campaignID {
			t.Errorf("session %s belongs to campaign %s, want %s (cross-campaign leak)", v.ID, v.CampaignID, campaignID)
		}
		gotIDs = append(gotIDs, v.ID)
	}
	want := []uuid.UUID{newest, middle, oldest}
	if len(gotIDs) != len(want) {
		t.Fatalf("ListVoiceSessions = %v, want newest-first %v", gotIDs, want)
	}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Errorf("ListVoiceSessions[%d] = %s, want %s (newest-first)", i, gotIDs[i], want[i])
		}
	}
	// The running row is present and still marked running.
	if all[0].ID != newest || all[0].Status != storage.VoiceSessionRunning || all[0].EndedAt != nil {
		t.Errorf("newest = %+v, want the running row with no ended_at", all[0])
	}

	// Limit honoured: only the two newest.
	capped, err := st.ListVoiceSessions(ctx, campaignID, 2)
	if err != nil {
		t.Fatalf("ListVoiceSessions(limit=2): %v", err)
	}
	if len(capped) != 2 || capped[0].ID != newest || capped[1].ID != middle {
		t.Errorf("ListVoiceSessions(limit=2) = %v, want [%s %s]", capped, newest, middle)
	}

	// id DESC tiebreak: two rows sharing the SAME started_at must order by id DESC
	// (deterministic, same tiebreak as GetLatestVoiceSession) so paging never
	// duplicates or drops a row. A dedicated campaign isolates this from the above.
	var tieCampaign uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name) VALUES ($1, 'Tie') RETURNING id`, tenantID).
		Scan(&tieCampaign); err != nil {
		t.Fatalf("insert tie campaign: %v", err)
	}
	tie := base.Add(9 * time.Hour)
	a := mk(tieCampaign, tie, false)
	b := mk(tieCampaign, tie, false)
	hi, lo := a, b
	if b.String() > a.String() { // uuid hex-string order matches Postgres uuid byte order
		hi, lo = b, a
	}
	tied, err := st.ListVoiceSessions(ctx, tieCampaign, 50)
	if err != nil {
		t.Fatalf("ListVoiceSessions(tie): %v", err)
	}
	if len(tied) != 2 || tied[0].ID != hi || tied[1].ID != lo {
		t.Errorf("equal started_at order = %v, want id DESC [%s %s]", tied, hi, lo)
	}
}

// TestReconcileOrphanedVoiceSessions is #143's boot reconciliation against a
// real Postgres: a row stranded 'running' (crash / failed end-write) is closed
// with ended_at + the distinguishing end_reason; a cleanly ended row keeps its
// NULL end_reason; a second reconcile finds nothing (idempotent).
func TestReconcileOrphanedVoiceSessions(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// A cleanly ended session: reconciliation must not touch it.
	clean, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession (clean): %v", err)
	}
	if _, err := st.EndVoiceSession(ctx, clean.ID, 3); err != nil {
		t.Fatalf("EndVoiceSession (clean): %v", err)
	}

	// The orphan: still 'running', no live loop (this "process" just booted).
	orphan, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession (orphan): %v", err)
	}

	n, err := st.ReconcileOrphanedVoiceSessions(ctx)
	if err != nil {
		t.Fatalf("ReconcileOrphanedVoiceSessions: %v", err)
	}
	if n != 1 {
		t.Errorf("reconciled = %d, want 1", n)
	}

	got, err := st.GetVoiceSession(ctx, orphan.ID)
	if err != nil {
		t.Fatalf("GetVoiceSession (orphan): %v", err)
	}
	if got.Status != storage.VoiceSessionEnded || got.EndedAt == nil {
		t.Errorf("orphan after reconcile = %+v, want ended with ended_at", got)
	}
	if got.EndReason == nil || *got.EndReason != storage.VoiceSessionReasonOrphaned {
		t.Errorf("orphan end_reason = %v, want %q", got.EndReason, storage.VoiceSessionReasonOrphaned)
	}

	// The clean end stays distinguishable: end_reason NULL.
	cleanGot, err := st.GetVoiceSession(ctx, clean.ID)
	if err != nil {
		t.Fatalf("GetVoiceSession (clean): %v", err)
	}
	if cleanGot.EndReason != nil {
		t.Errorf("clean end_reason = %q, want NULL", *cleanGot.EndReason)
	}

	// Idempotent: nothing left to close.
	if n, err := st.ReconcileOrphanedVoiceSessions(ctx); err != nil || n != 0 {
		t.Errorf("second reconcile = %d, %v; want 0, nil", n, err)
	}
}

// TestCloseVoiceSessionFailed is #123: a fatal gateway rejection closes the row
// with status='failed', ended_at set, and a readable end_reason — it round-trips
// through Get/GetLatest, and the boot reconciliation (which targets only 'running')
// leaves the terminal failed row untouched.
func TestCloseVoiceSessionFailed(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}

	reason := "invalid_bot_token: wirenpc: open gateway: websocket: close 4004: Authentication failed"
	failed, err := st.CloseVoiceSession(ctx, vs.ID, storage.VoiceSessionFailed, 0, &reason)
	if err != nil {
		t.Fatalf("CloseVoiceSession(failed): %v", err)
	}
	if failed.Status != storage.VoiceSessionFailed {
		t.Errorf("status = %q, want failed", failed.Status)
	}
	if failed.EndedAt == nil {
		t.Fatal("ended_at is nil after a fatal close")
	}
	if failed.EndReason == nil || *failed.EndReason != reason {
		t.Errorf("end_reason = %v, want %q", failed.EndReason, reason)
	}

	// The failed session is the latest — the idle Session screen surfaces it with
	// its reason (AC1/AC3 reload truth).
	latest, err := st.GetLatestVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("GetLatestVoiceSession: %v", err)
	}
	if latest.ID != vs.ID || latest.Status != storage.VoiceSessionFailed ||
		latest.EndReason == nil || *latest.EndReason != reason {
		t.Errorf("latest = %+v, want failed %s with reason", latest, vs.ID)
	}

	// A failed row is already terminal: boot reconciliation only closes 'running'
	// rows, so it must count zero and leave this row exactly as it is (AC4).
	n, err := st.ReconcileOrphanedVoiceSessions(ctx)
	if err != nil {
		t.Fatalf("ReconcileOrphanedVoiceSessions: %v", err)
	}
	if n != 0 {
		t.Errorf("reconciled = %d, want 0 (a failed row is not an orphan)", n)
	}
	after, err := st.GetVoiceSession(ctx, vs.ID)
	if err != nil {
		t.Fatalf("GetVoiceSession: %v", err)
	}
	if after.Status != storage.VoiceSessionFailed || after.EndReason == nil || *after.EndReason != reason {
		t.Errorf("failed row after reconcile = %+v, want unchanged failed with reason", after)
	}
}

// TestEndVoiceSessionDelegatesToEnded is #123: EndVoiceSession stays a thin
// delegating wrapper over CloseVoiceSession — a normal stop still lands 'ended'
// with a NULL end_reason (distinguishable from both orphaned and failed).
func TestEndVoiceSessionDelegatesToEnded(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}
	ended, err := st.EndVoiceSession(ctx, vs.ID, 4)
	if err != nil {
		t.Fatalf("EndVoiceSession: %v", err)
	}
	if ended.Status != storage.VoiceSessionEnded || ended.EndedAt == nil {
		t.Errorf("ended = %+v, want ended with ended_at", ended)
	}
	if ended.LineCount != 4 {
		t.Errorf("line_count = %d, want 4", ended.LineCount)
	}
	if ended.EndReason != nil {
		t.Errorf("end_reason = %q, want NULL for a clean end", *ended.EndReason)
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
