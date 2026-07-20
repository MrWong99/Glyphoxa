//go:build integration

package storage_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// secondCampaign inserts a second tenant + campaign alongside seedCampaign's, so
// the two-tenant claim tests exercise distinct tenants (the one-live-per-tenant
// index is per-tenant, so two tenants each hold their own live intent).
func secondCampaign(t *testing.T, pool *pgxpool.Pool) (tenantID, campaignID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx,
		`INSERT INTO tenant (name) VALUES ('Beta TTRPG') RETURNING id`).Scan(&tenantID); err != nil {
		t.Fatalf("insert second tenant: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name, system, language)
		 VALUES ($1, 'Second Camp', 'dnd5e', 'en') RETURNING id`, tenantID).Scan(&campaignID); err != nil {
		t.Fatalf("insert second campaign: %v", err)
	}
	return tenantID, campaignID
}

// TestCreateVoiceSessionIntent covers sequence (1): a pending intent is created,
// and a duplicate live-per-tenant create trips ErrIntentActive.
func TestCreateVoiceSessionIntent(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	intent, err := st.CreateVoiceSessionIntent(ctx, tenantID, campaignID)
	if err != nil {
		t.Fatalf("create intent: %v", err)
	}
	if intent.Status != storage.VoiceIntentPending {
		t.Fatalf("status = %q, want pending", intent.Status)
	}
	if intent.TenantID != tenantID || intent.CampaignID != campaignID {
		t.Fatalf("owning ids = (%s,%s), want (%s,%s)", intent.TenantID, intent.CampaignID, tenantID, campaignID)
	}
	if intent.InstanceID != "" || intent.ClaimedAt != nil || intent.HeartbeatAt != nil {
		t.Fatalf("fresh intent should be unclaimed: %+v", intent)
	}

	// A second create for the SAME tenant while the first is non-terminal collides.
	if _, err := st.CreateVoiceSessionIntent(ctx, tenantID, campaignID); !errors.Is(err, storage.ErrIntentActive) {
		t.Fatalf("duplicate create err = %v, want ErrIntentActive", err)
	}

	// Once the first finishes, the tenant can start again.
	if _, err := st.RequestVoiceSessionStop(ctx, intent.ID); err != nil {
		t.Fatalf("stop pending intent: %v", err)
	}
	if _, err := st.CreateVoiceSessionIntent(ctx, tenantID, campaignID); err != nil {
		t.Fatalf("create after prior done: %v", err)
	}
}

// TestConcurrentClaimersDistinctIntents covers sequence (2): two concurrent
// claimers get DISTINCT pending intents and never the same one, and neither ever
// re-claims a claimed/live intent (no takeover, ADR-0006).
func TestConcurrentClaimersDistinctIntents(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantA, campA := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	tenantB, campB := secondCampaign(t, pool)

	iA, err := st.CreateVoiceSessionIntent(ctx, tenantA, campA)
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	iB, err := st.CreateVoiceSessionIntent(ctx, tenantB, campB)
	if err != nil {
		t.Fatalf("create B: %v", err)
	}

	// Two workers claim concurrently; each must win a distinct intent.
	var wg sync.WaitGroup
	claimed := make([]storage.VoiceSessionIntent, 2)
	errs := make([]error, 2)
	for i, inst := range []string{"worker-1", "worker-2"} {
		wg.Add(1)
		go func(i int, inst string) {
			defer wg.Done()
			claimed[i], errs[i] = st.ClaimVoiceSessionIntent(ctx, inst)
		}(i, inst)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("claim %d: %v", i, e)
		}
	}
	if claimed[0].ID == claimed[1].ID {
		t.Fatalf("both workers claimed the same intent %s", claimed[0].ID)
	}
	got := map[uuid.UUID]bool{claimed[0].ID: true, claimed[1].ID: true}
	if !got[iA.ID] || !got[iB.ID] {
		t.Fatalf("claimed set %v does not cover both intents %s,%s", got, iA.ID, iB.ID)
	}
	for _, c := range claimed {
		if c.Status != storage.VoiceIntentClaimed || c.InstanceID == "" || c.ClaimedAt == nil || c.HeartbeatAt == nil {
			t.Fatalf("claimed intent not stamped: %+v", c)
		}
	}

	// No pending intents remain: a third claim finds nothing (never re-claims a
	// claimed/live row — no takeover).
	if _, err := st.ClaimVoiceSessionIntent(ctx, "worker-3"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("third claim err = %v, want ErrNotFound (no takeover of claimed rows)", err)
	}
}

// TestHeartbeatFencing covers sequence (3): heartbeat is fenced by instance +
// status, and after the row is marked dead a heartbeat returns ErrNotFound.
func TestHeartbeatFencing(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	intent, err := st.CreateVoiceSessionIntent(ctx, tenantID, campaignID)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	claimed, err := st.ClaimVoiceSessionIntent(ctx, "worker-1")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	// The owning instance heartbeats fine; stop not requested.
	stop, err := st.HeartbeatVoiceSessionIntent(ctx, claimed.ID, "worker-1")
	if err != nil || stop {
		t.Fatalf("heartbeat by owner = (%v,%v), want (false,nil)", stop, err)
	}

	// A DIFFERENT instance is fenced out.
	if _, err := st.HeartbeatVoiceSessionIntent(ctx, claimed.ID, "worker-2"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("heartbeat by foreign instance err = %v, want ErrNotFound", err)
	}

	// Mark the row dead (reap with zero expiry), then the owner's heartbeat is
	// superseded → ErrNotFound (caller kills its local session).
	if _, err := st.ReapDeadVoiceSessionIntents(ctx, 0); err != nil {
		t.Fatalf("reap: %v", err)
	}
	if _, err := st.HeartbeatVoiceSessionIntent(ctx, claimed.ID, "worker-1"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("heartbeat after dead err = %v, want ErrNotFound", err)
	}
	_ = intent
}

// TestReapMarksStaleDead covers sequence (4): reap marks stale claimed/live
// intents dead and leaves a fresh one untouched.
func TestReapMarksStaleDead(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantA, campA := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	tenantB, campB := secondCampaign(t, pool)

	// Stale: claimed, then heartbeat forced into the past.
	if _, err := st.CreateVoiceSessionIntent(ctx, tenantA, campA); err != nil {
		t.Fatalf("create A: %v", err)
	}
	stale, err := st.ClaimVoiceSessionIntent(ctx, "dead-worker")
	if err != nil {
		t.Fatalf("claim A: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE voice_session_intents SET heartbeat_at = now() - interval '10 minutes' WHERE id = $1`, stale.ID); err != nil {
		t.Fatalf("age heartbeat: %v", err)
	}

	// Fresh: claimed just now.
	if _, err := st.CreateVoiceSessionIntent(ctx, tenantB, campB); err != nil {
		t.Fatalf("create B: %v", err)
	}
	fresh, err := st.ClaimVoiceSessionIntent(ctx, "live-worker")
	if err != nil {
		t.Fatalf("claim B: %v", err)
	}

	n, err := st.ReapDeadVoiceSessionIntents(ctx, 30*time.Second)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if n != 1 {
		t.Fatalf("reaped %d, want 1 (only the stale one)", n)
	}

	gotStale, err := st.GetVoiceSessionIntent(ctx, stale.ID)
	if err != nil {
		t.Fatalf("get stale: %v", err)
	}
	if gotStale.Status != storage.VoiceIntentDead || gotStale.LastError == "" || gotStale.EndedAt == nil {
		t.Fatalf("stale not reaped: %+v", gotStale)
	}
	gotFresh, err := st.GetVoiceSessionIntent(ctx, fresh.ID)
	if err != nil {
		t.Fatalf("get fresh: %v", err)
	}
	if gotFresh.Status != storage.VoiceIntentClaimed {
		t.Fatalf("fresh intent disturbed: %+v", gotFresh)
	}
}

// TestRequestStop covers sequence (5): a pending intent stops directly to done;
// a live intent only gets the flag set (its worker winds it down).
func TestRequestStop(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantA, campA := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	tenantB, campB := secondCampaign(t, pool)

	// pending → done directly.
	pending, err := st.CreateVoiceSessionIntent(ctx, tenantA, campA)
	if err != nil {
		t.Fatalf("create pending: %v", err)
	}
	stopped, err := st.RequestVoiceSessionStop(ctx, pending.ID)
	if err != nil {
		t.Fatalf("stop pending: %v", err)
	}
	if stopped.Status != storage.VoiceIntentDone || stopped.EndedAt == nil {
		t.Fatalf("pending stop = %+v, want done+ended", stopped)
	}

	// live → flag only.
	if _, err := st.CreateVoiceSessionIntent(ctx, tenantB, campB); err != nil {
		t.Fatalf("create live: %v", err)
	}
	claimed, err := st.ClaimVoiceSessionIntent(ctx, "worker-1")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	vsID := uuid.New()
	if _, err := pool.Exec(ctx,
		`INSERT INTO voice_sessions (id, campaign_id, status) VALUES ($1, $2, 'running')`, vsID, campB); err != nil {
		t.Fatalf("insert voice session: %v", err)
	}
	if _, err := st.MarkVoiceSessionIntentLive(ctx, claimed.ID, "worker-1", vsID); err != nil {
		t.Fatalf("mark live: %v", err)
	}
	flagged, err := st.RequestVoiceSessionStop(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("stop live: %v", err)
	}
	if flagged.Status != storage.VoiceIntentLive || !flagged.StopRequested {
		t.Fatalf("live stop = %+v, want still-live + stop_requested", flagged)
	}
	// The worker's next heartbeat reports the requested stop.
	stop, err := st.HeartbeatVoiceSessionIntent(ctx, claimed.ID, "worker-1")
	if err != nil || !stop {
		t.Fatalf("heartbeat after stop req = (%v,%v), want (true,nil)", stop, err)
	}
}

// TestGetLiveVoiceSessionIntentForTenant covers the per-tenant read backing
// IntentControl.Active: it returns the non-terminal intent, ErrNotFound once
// terminal.
func TestGetLiveVoiceSessionIntentForTenant(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	if _, err := st.GetLiveVoiceSessionIntentForTenant(ctx, tenantID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("idle tenant err = %v, want ErrNotFound", err)
	}
	created, err := st.CreateVoiceSessionIntent(ctx, tenantID, campaignID)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := st.GetLiveVoiceSessionIntentForTenant(ctx, tenantID)
	if err != nil || got.ID != created.ID {
		t.Fatalf("live read = (%s,%v), want %s", got.ID, err, created.ID)
	}
	if _, err := st.RequestVoiceSessionStop(ctx, created.ID); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, err := st.GetLiveVoiceSessionIntentForTenant(ctx, tenantID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("after done err = %v, want ErrNotFound", err)
	}
}

// TestReconcileWorkerOrphanedScoping covers sequence (9) orphan-reconcile
// scoping: a worker boot closes a 'running' voice_sessions row whose intent went
// dead, but NEVER one whose intent is still live (another worker owns it) — the
// reviewer-flagged process-blindness fix.
func TestReconcileWorkerOrphanedScoping(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantA, campA := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	tenantB, campB := secondCampaign(t, pool)

	// Crashed worker: a live intent (heartbeat aged) + its running voice_sessions
	// row, then reaped dead — this row is a true orphan to close.
	deadVS := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO voice_sessions (id, campaign_id, status) VALUES ($1,$2,'running')`, deadVS, campA); err != nil {
		t.Fatalf("insert dead vs: %v", err)
	}
	if _, err := st.CreateVoiceSessionIntent(ctx, tenantA, campA); err != nil {
		t.Fatalf("create A: %v", err)
	}
	deadClaim, err := st.ClaimVoiceSessionIntent(ctx, "dead-worker")
	if err != nil {
		t.Fatalf("claim A: %v", err)
	}
	if _, err := st.MarkVoiceSessionIntentLive(ctx, deadClaim.ID, "dead-worker", deadVS); err != nil {
		t.Fatalf("mark A live: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE voice_session_intents SET heartbeat_at = now() - interval '10 minutes' WHERE id=$1`, deadClaim.ID); err != nil {
		t.Fatalf("age A: %v", err)
	}
	if _, err := st.ReapDeadVoiceSessionIntents(ctx, 30*time.Second); err != nil {
		t.Fatalf("reap: %v", err)
	}

	// Live worker: a live intent + its running voice_sessions row, fresh heartbeat
	// — another worker owns it, must NOT be closed.
	liveVS := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO voice_sessions (id, campaign_id, status) VALUES ($1,$2,'running')`, liveVS, campB); err != nil {
		t.Fatalf("insert live vs: %v", err)
	}
	if _, err := st.CreateVoiceSessionIntent(ctx, tenantB, campB); err != nil {
		t.Fatalf("create B: %v", err)
	}
	liveClaim, err := st.ClaimVoiceSessionIntent(ctx, "live-worker")
	if err != nil {
		t.Fatalf("claim B: %v", err)
	}
	if _, err := st.MarkVoiceSessionIntentLive(ctx, liveClaim.ID, "live-worker", liveVS); err != nil {
		t.Fatalf("mark B live: %v", err)
	}

	n, err := st.ReconcileWorkerOrphanedVoiceSessions(ctx)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Fatalf("reconciled %d, want 1 (only the dead-intent row)", n)
	}
	gotDead, err := st.GetVoiceSession(ctx, deadVS)
	if err != nil {
		t.Fatalf("get dead vs: %v", err)
	}
	if gotDead.Status != storage.VoiceSessionEnded {
		t.Fatalf("dead worker's row not closed: %+v", gotDead)
	}
	gotLive, err := st.GetVoiceSession(ctx, liveVS)
	if err != nil {
		t.Fatalf("get live vs: %v", err)
	}
	if gotLive.Status != storage.VoiceSessionRunning {
		t.Fatalf("live worker's row wrongly closed: %+v", gotLive)
	}
}

// TestFinishVoiceSessionIntent covers the terminal write fencing: a foreign
// instance cannot finish, and once dead the owner cannot finish either.
func TestFinishVoiceSessionIntent(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	if _, err := st.CreateVoiceSessionIntent(ctx, tenantID, campaignID); err != nil {
		t.Fatalf("create: %v", err)
	}
	claimed, err := st.ClaimVoiceSessionIntent(ctx, "worker-1")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	if _, err := st.FinishVoiceSessionIntent(ctx, claimed.ID, "worker-2", storage.VoiceIntentDone, ""); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("finish by foreign instance err = %v, want ErrNotFound", err)
	}
	done, err := st.FinishVoiceSessionIntent(ctx, claimed.ID, "worker-1", storage.VoiceIntentDone, "")
	if err != nil {
		t.Fatalf("finish by owner: %v", err)
	}
	if done.Status != storage.VoiceIntentDone || done.EndedAt == nil {
		t.Fatalf("finished = %+v, want done+ended", done)
	}
	// A second finish on a terminal row is a no-op → ErrNotFound.
	if _, err := st.FinishVoiceSessionIntent(ctx, claimed.ID, "worker-1", storage.VoiceIntentFailed, "boom"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("re-finish terminal err = %v, want ErrNotFound", err)
	}
}

// TestReconcileWorkerOrphanedUnboundGap covers #483 M2: a worker dying BETWEEN
// CreateVoiceSession ('running' row written) and MarkVoiceSessionIntentLive (the
// row never bound onto the intent) leaves a reaped 'dead' intent with a NULL
// voice_session_id — the bound-row arm of the reconcile can never match it, so
// the row sat 'running' forever. The no-non-terminal-intent arm must close it —
// and must NOT close it while the claim (the gap itself) is still non-terminal.
func TestReconcileWorkerOrphanedUnboundGap(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// The worker claims the intent and creates the session row, then crashes
	// BEFORE MarkVoiceSessionIntentLive: intent 'claimed', row unbound.
	if _, err := st.CreateVoiceSessionIntent(ctx, tenantID, campaignID); err != nil {
		t.Fatalf("create intent: %v", err)
	}
	claim, err := st.ClaimVoiceSessionIntent(ctx, "gap-worker")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("create voice session: %v", err)
	}

	// While the claim is non-terminal (the live gap), the reconcile must keep the
	// fresh row — another worker's tick racing the gap must not kill a mid-Start.
	if n, err := st.ReconcileWorkerOrphanedVoiceSessions(ctx); err != nil || n != 0 {
		t.Fatalf("reconcile during the gap = (%d, %v), want (0, nil)", n, err)
	}

	// The crash: the heartbeat ages out and the reaper marks the claim dead. Its
	// voice_session_id is NULL — the bound arm can never close the row.
	if _, err := pool.Exec(ctx,
		`UPDATE voice_session_intents SET heartbeat_at = now() - interval '10 minutes' WHERE id=$1`, claim.ID); err != nil {
		t.Fatalf("age claim: %v", err)
	}
	if _, err := st.ReapDeadVoiceSessionIntents(ctx, 30*time.Second); err != nil {
		t.Fatalf("reap: %v", err)
	}
	dead, err := st.GetVoiceSessionIntent(ctx, claim.ID)
	if err != nil {
		t.Fatalf("get intent: %v", err)
	}
	if dead.Status != storage.VoiceIntentDead || dead.VoiceSessionID.Valid {
		t.Fatalf("gap intent = %+v, want dead with a NULL voice_session_id", dead)
	}

	// The next reconcile closes the unbound orphan via the no-intent arm.
	n, err := st.ReconcileWorkerOrphanedVoiceSessions(ctx)
	if err != nil {
		t.Fatalf("reconcile after reap: %v", err)
	}
	if n != 1 {
		t.Fatalf("reconciled %d rows, want 1 (the unbound orphan)", n)
	}
	got, err := st.GetVoiceSession(ctx, vs.ID)
	if err != nil {
		t.Fatalf("get vs: %v", err)
	}
	if got.Status != storage.VoiceSessionEnded {
		t.Fatalf("orphan row status = %q, want ended", got.Status)
	}
}
