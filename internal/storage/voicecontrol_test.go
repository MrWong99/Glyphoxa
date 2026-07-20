//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// seedControlIntent creates a pending intent for the seeded tenant/campaign —
// the parent row every voice_session_controls row hangs off.
func seedControlIntent(t *testing.T, st *storage.Store, tenantID, campaignID uuid.UUID) storage.VoiceSessionIntent {
	t.Helper()
	intent, err := st.CreateVoiceSessionIntent(context.Background(), tenantID, campaignID)
	if err != nil {
		t.Fatalf("create intent: %v", err)
	}
	return intent
}

// TestVoiceSessionControl_CreateListFinish covers sequence (1): create + list
// pending returns rows in (created_at, id) order, and Finish is fenced — a
// second terminal write on the same row yields ErrNotFound.
func TestVoiceSessionControl_CreateListFinish(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	intent := seedControlIntent(t, st, tenantID, campaignID)

	first, err := st.CreateVoiceSessionControl(ctx, storage.VoiceSessionControl{
		IntentID: intent.ID,
		TenantID: tenantID,
		Kind:     storage.VoiceControlSay,
		AgentID:  "agent-1",
		SayText:  "first line",
	})
	if err != nil {
		t.Fatalf("create first control: %v", err)
	}
	if first.Status != storage.VoiceControlPending {
		t.Fatalf("fresh control status = %q, want pending", first.Status)
	}
	if first.Kind != storage.VoiceControlSay || first.AgentID != "agent-1" || first.SayText != "first line" {
		t.Fatalf("fresh control fields = %+v", first)
	}

	second, err := st.CreateVoiceSessionControl(ctx, storage.VoiceSessionControl{
		IntentID: intent.ID,
		TenantID: tenantID,
		Kind:     storage.VoiceControlMuteAgent,
		AgentID:  "agent-2",
		Muted:    true,
	})
	if err != nil {
		t.Fatalf("create second control: %v", err)
	}

	pending, err := st.ListPendingVoiceSessionControls(ctx, intent.ID)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %d rows, want 2", len(pending))
	}
	if pending[0].ID != first.ID || pending[1].ID != second.ID {
		t.Fatalf("pending order = [%s %s], want [%s %s] (created_at, id)",
			pending[0].ID, pending[1].ID, first.ID, second.ID)
	}
	if !pending[1].Muted {
		t.Fatalf("second control muted = false, want true")
	}

	// The worker claims pending→executing BEFORE running the verb (#503 FIX1):
	// Finish is fenced on 'executing', so a bare pending row cannot be finished.
	if _, err := st.FinishVoiceSessionControl(ctx, first.ID, storage.VoiceControlDone, nil, ""); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("finish of a pending (unclaimed) row err = %v, want ErrNotFound", err)
	}
	started, err := st.StartVoiceSessionControl(ctx, first.ID)
	if err != nil {
		t.Fatalf("start control: %v", err)
	}
	if started.Status != storage.VoiceControlExecuting || started.StartedAt == nil {
		t.Fatalf("started control = %+v, want executing with started_at", started)
	}
	// A second claim finds no pending row (fenced) — no re-dispatch.
	if _, err := st.StartVoiceSessionControl(ctx, first.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("second start err = %v, want ErrNotFound (fenced)", err)
	}
	// An executing row falls out of the pending drain list.
	pending, err = st.ListPendingVoiceSessionControls(ctx, intent.ID)
	if err != nil || len(pending) != 1 || pending[0].ID != second.ID {
		t.Fatalf("pending after claim = %+v (err %v), want just the second row", pending, err)
	}

	done, err := st.FinishVoiceSessionControl(ctx, first.ID, storage.VoiceControlDone, []string{"a", "b"}, "")
	if err != nil {
		t.Fatalf("finish control: %v", err)
	}
	if done.Status != storage.VoiceControlDone || len(done.ResultIDs) != 2 || done.EndedAt == nil {
		t.Fatalf("finished control = %+v, want done with 2 result ids and ended_at", done)
	}

	// The fence: a second terminal write finds no executing row.
	if _, err := st.FinishVoiceSessionControl(ctx, first.ID, storage.VoiceControlFailed, nil, "late"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("second finish err = %v, want ErrNotFound (fenced)", err)
	}

	// The finished row fell out of the pending list; a Get still loads it.
	pending, err = st.ListPendingVoiceSessionControls(ctx, intent.ID)
	if err != nil || len(pending) != 1 || pending[0].ID != second.ID {
		t.Fatalf("pending after finish = %+v (err %v), want just the second row", pending, err)
	}
	got, err := st.GetVoiceSessionControl(ctx, first.ID)
	if err != nil || got.Status != storage.VoiceControlDone {
		t.Fatalf("get finished control = %+v (err %v), want done", got, err)
	}
}

// TestVoiceSessionControl_CancelPendingOnly covers sequence (2): the requester's
// timeout cancel takes a PENDING row to failed 'requester timed out', but leaves
// an already-done row untouched (returns false — the worker won the race).
func TestVoiceSessionControl_CancelPendingOnly(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	intent := seedControlIntent(t, st, tenantID, campaignID)

	pendingRow, err := st.CreateVoiceSessionControl(ctx, storage.VoiceSessionControl{
		IntentID: intent.ID, TenantID: tenantID, Kind: storage.VoiceControlMuteAll, Muted: true,
	})
	if err != nil {
		t.Fatalf("create control: %v", err)
	}
	cancelled, err := st.CancelPendingVoiceSessionControl(ctx, pendingRow.ID)
	if err != nil || !cancelled {
		t.Fatalf("cancel pending = (%v, %v), want (true, nil)", cancelled, err)
	}
	got, err := st.GetVoiceSessionControl(ctx, pendingRow.ID)
	if err != nil || got.Status != storage.VoiceControlFailed || got.LastError != "requester timed out" {
		t.Fatalf("cancelled row = %+v (err %v), want failed 'requester timed out'", got, err)
	}

	doneRow, err := st.CreateVoiceSessionControl(ctx, storage.VoiceSessionControl{
		IntentID: intent.ID, TenantID: tenantID, Kind: storage.VoiceControlMuteAll, Muted: true,
	})
	if err != nil {
		t.Fatalf("create second control: %v", err)
	}
	if _, err := st.StartVoiceSessionControl(ctx, doneRow.ID); err != nil {
		t.Fatalf("claim second control: %v", err)
	}
	if _, err := st.FinishVoiceSessionControl(ctx, doneRow.ID, storage.VoiceControlDone, nil, ""); err != nil {
		t.Fatalf("finish second control: %v", err)
	}
	cancelled, err = st.CancelPendingVoiceSessionControl(ctx, doneRow.ID)
	if err != nil || cancelled {
		t.Fatalf("cancel done row = (%v, %v), want (false, nil) — worker won", cancelled, err)
	}
	got, err = st.GetVoiceSessionControl(ctx, doneRow.ID)
	if err != nil || got.Status != storage.VoiceControlDone {
		t.Fatalf("done row after cancel attempt = %+v (err %v), want still done", got, err)
	}
}

// TestVoiceSessionControl_SweepOrphaned covers sequence (3): pending AND
// executing rows of a TERMINAL intent are failed with the ENCODED
// no_active_session cause (#503 FIX3, so the requester decodes it to
// ErrNoActiveSession and the GM sees the plain guard, not a raw chain); a live
// intent's rows survive.
func TestVoiceSessionControl_SweepOrphaned(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// Intent A: claimed then finished (terminal). One pending + one executing
	// control are both orphaned by the terminal intent.
	intentA := seedControlIntent(t, st, tenantID, campaignID)
	if _, err := st.ClaimVoiceSessionIntent(ctx, "worker-a"); err != nil {
		t.Fatalf("claim intent A: %v", err)
	}
	orphanPending, err := st.CreateVoiceSessionControl(ctx, storage.VoiceSessionControl{
		IntentID: intentA.ID, TenantID: tenantID, Kind: storage.VoiceControlMuteAll, Muted: true,
	})
	if err != nil {
		t.Fatalf("create orphan pending control: %v", err)
	}
	orphanExec, err := st.CreateVoiceSessionControl(ctx, storage.VoiceSessionControl{
		IntentID: intentA.ID, TenantID: tenantID, Kind: storage.VoiceControlSay, AgentID: "a", SayText: "hi",
	})
	if err != nil {
		t.Fatalf("create orphan executing control: %v", err)
	}
	if _, err := st.StartVoiceSessionControl(ctx, orphanExec.ID); err != nil {
		t.Fatalf("claim orphan executing control: %v", err)
	}
	if _, err := st.FinishVoiceSessionIntent(ctx, intentA.ID, "worker-a", storage.VoiceIntentDone, ""); err != nil {
		t.Fatalf("finish intent A: %v", err)
	}

	// Intent B (second tenant): still pending (non-terminal). Its control survives.
	tenantB, campaignB := secondCampaign(t, pool)
	intentB := seedControlIntent(t, st, tenantB, campaignB)
	alive, err := st.CreateVoiceSessionControl(ctx, storage.VoiceSessionControl{
		IntentID: intentB.ID, TenantID: tenantB, Kind: storage.VoiceControlMuteAll, Muted: true,
	})
	if err != nil {
		t.Fatalf("create live control: %v", err)
	}

	n, err := st.SweepOrphanedVoiceSessionControls(ctx, time.Hour)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 2 {
		t.Fatalf("sweep closed %d rows, want 2 (pending + executing of the terminal intent)", n)
	}
	for _, id := range []uuid.UUID{orphanPending.ID, orphanExec.ID} {
		got, err := st.GetVoiceSessionControl(ctx, id)
		if err != nil || got.Status != storage.VoiceControlFailed {
			t.Fatalf("orphan %s after sweep = %+v (err %v), want failed", id, got, err)
		}
		if sentinel, ok := session.DecodeControlFailure(got.LastError); !ok || !errors.Is(sentinel, session.ErrNoActiveSession) {
			t.Fatalf("orphan %s last_error = %q, want an encoded no_active_session cause", id, got.LastError)
		}
	}
	still, err := st.GetVoiceSessionControl(ctx, alive.ID)
	if err != nil || still.Status != storage.VoiceControlPending {
		t.Fatalf("live intent's control after sweep = %+v (err %v), want still pending", still, err)
	}
}

// TestVoiceSessionControl_SweepStaleExecuting covers the bounded recovery arm of
// FIX1: an 'executing' row of a still-LIVE intent that stalled past the stale
// cutoff (a finish-write blip with the requester gone) is failed so it never
// sits forever; a fresh executing row of the same live intent survives.
func TestVoiceSessionControl_SweepStaleExecuting(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	intent := seedControlIntent(t, st, tenantID, campaignID)
	if _, err := st.ClaimVoiceSessionIntent(ctx, "worker-a"); err != nil {
		t.Fatalf("claim intent: %v", err)
	}
	stale, err := st.CreateVoiceSessionControl(ctx, storage.VoiceSessionControl{
		IntentID: intent.ID, TenantID: tenantID, Kind: storage.VoiceControlSay, AgentID: "a", SayText: "hi",
	})
	if err != nil {
		t.Fatalf("create stale control: %v", err)
	}
	if _, err := st.StartVoiceSessionControl(ctx, stale.ID); err != nil {
		t.Fatalf("claim stale control: %v", err)
	}
	// Backdate started_at so it is well past any reasonable cutoff.
	if _, err := pool.Exec(ctx,
		`UPDATE voice_session_controls SET started_at = now() - interval '1 hour' WHERE id = $1`, stale.ID); err != nil {
		t.Fatalf("backdate started_at: %v", err)
	}
	fresh, err := st.CreateVoiceSessionControl(ctx, storage.VoiceSessionControl{
		IntentID: intent.ID, TenantID: tenantID, Kind: storage.VoiceControlSay, AgentID: "a", SayText: "yo",
	})
	if err != nil {
		t.Fatalf("create fresh control: %v", err)
	}
	if _, err := st.StartVoiceSessionControl(ctx, fresh.ID); err != nil {
		t.Fatalf("claim fresh control: %v", err)
	}

	n, err := st.SweepOrphanedVoiceSessionControls(ctx, time.Minute)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("sweep closed %d rows, want 1 (only the stale executing)", n)
	}
	got, err := st.GetVoiceSessionControl(ctx, stale.ID)
	if err != nil || got.Status != storage.VoiceControlFailed {
		t.Fatalf("stale executing after sweep = %+v (err %v), want failed", got, err)
	}
	still, err := st.GetVoiceSessionControl(ctx, fresh.ID)
	if err != nil || still.Status != storage.VoiceControlExecuting {
		t.Fatalf("fresh executing after sweep = %+v (err %v), want still executing", still, err)
	}
}
