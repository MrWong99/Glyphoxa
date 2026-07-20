//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

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

	done, err := st.FinishVoiceSessionControl(ctx, first.ID, storage.VoiceControlDone, []string{"a", "b"}, "")
	if err != nil {
		t.Fatalf("finish control: %v", err)
	}
	if done.Status != storage.VoiceControlDone || len(done.ResultIDs) != 2 || done.EndedAt == nil {
		t.Fatalf("finished control = %+v, want done with 2 result ids and ended_at", done)
	}

	// The fence: a second terminal write finds no pending row.
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

// TestVoiceSessionControl_SweepOrphaned covers sequence (3): pendings of a
// TERMINAL intent are failed 'session ended'; a live intent's pendings survive.
func TestVoiceSessionControl_SweepOrphaned(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// Intent A: claimed then finished (terminal). Its pending control is orphaned.
	intentA := seedControlIntent(t, st, tenantID, campaignID)
	if _, err := st.ClaimVoiceSessionIntent(ctx, "worker-a"); err != nil {
		t.Fatalf("claim intent A: %v", err)
	}
	orphan, err := st.CreateVoiceSessionControl(ctx, storage.VoiceSessionControl{
		IntentID: intentA.ID, TenantID: tenantID, Kind: storage.VoiceControlMuteAll, Muted: true,
	})
	if err != nil {
		t.Fatalf("create orphan control: %v", err)
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

	n, err := st.SweepOrphanedVoiceSessionControls(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("sweep closed %d rows, want 1", n)
	}
	got, err := st.GetVoiceSessionControl(ctx, orphan.ID)
	if err != nil || got.Status != storage.VoiceControlFailed || got.LastError != "session ended" {
		t.Fatalf("orphan after sweep = %+v (err %v), want failed 'session ended'", got, err)
	}
	still, err := st.GetVoiceSessionControl(ctx, alive.ID)
	if err != nil || still.Status != storage.VoiceControlPending {
		t.Fatalf("live intent's control after sweep = %+v (err %v), want still pending", still, err)
	}
}
