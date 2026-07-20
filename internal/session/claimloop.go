package session

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// The -mode voice claim loop (#491, ADR-0057 (b)): a Voice Instance polls the
// voice_session_intents claim plane, claims the oldest pending intent with the
// FOR UPDATE SKIP LOCKED idiom (ADR-0049), runs it through the tenant-aware
// Manager (#488), heartbeats while live, and finishes the row on end. No
// mid-session takeover (ADR-0006/0057 (e)): a claim's heartbeat going stale is
// the OWNING worker's death — the reaper marks that row 'dead' and the Tenant
// restarts; this loop never re-claims another worker's claimed/live intent.

// intentEndTimeout bounds a detached terminal write (Finish) after the run ctx
// is cancelled, mirroring the Manager's endTimeout so a shutdown can't hang on a
// slow DB.
const intentEndTimeout = 5 * time.Second

// IntentStore is the claim-plane surface the ClaimLoop needs (#491). *storage.Store
// satisfies it; tests use a fake so the loop is exercised without Postgres.
type IntentStore interface {
	ClaimVoiceSessionIntent(ctx context.Context, instanceID string) (storage.VoiceSessionIntent, error)
	MarkVoiceSessionIntentLive(ctx context.Context, id uuid.UUID, instanceID string, voiceSessionID uuid.UUID) (storage.VoiceSessionIntent, error)
	HeartbeatVoiceSessionIntent(ctx context.Context, id uuid.UUID, instanceID string) (bool, error)
	FinishVoiceSessionIntent(ctx context.Context, id uuid.UUID, instanceID string, status storage.VoiceSessionIntentStatus, lastError string) (storage.VoiceSessionIntent, error)
	ReapDeadVoiceSessionIntents(ctx context.Context, expiry time.Duration) (int64, error)
	// ReconcileWorkerOrphanedVoiceSessions closes 'running' voice_sessions rows a
	// crashed worker left behind (#491): run every tick (#483 L2 — idempotent and
	// cheap) so a leftover row is closed the moment it becomes reconcilable, not
	// only at the next boot.
	ReconcileWorkerOrphanedVoiceSessions(ctx context.Context) (int64, error)
	// GetVoiceSession loads the row an intent bound — the self-exit outcome read
	// (#483 L4): a session that ended 'failed' on its own finishes its intent
	// 'failed' with the row's end_reason, not a clean-looking 'done'.
	GetVoiceSession(ctx context.Context, id uuid.UUID) (storage.VoiceSession, error)
}

// dbOpCap caps a single claim-plane DB call (#483 M1), mirroring the presence
// elector's per-op posture: a black-holed connection otherwise blocks the loop
// for the kernel TCP timeout (minutes) — the zombie window where a live session
// outlives its reaped intent.
const dbOpCap = 3 * time.Second

// dbOpTimeout is min(cadence, dbOpCap): one DB call may never eat more than its
// own tick, and never more than the 3s cap regardless of cadence.
func dbOpTimeout(cadence time.Duration) time.Duration {
	if cadence > 0 && cadence < dbOpCap {
		return cadence
	}
	return dbOpCap
}

// ClaimLoopConfig carries the claim loop's three poll durations (#491), read
// from GLYPHOXA_VOICE_CLAIM_POLL / _HEARTBEAT_INTERVAL / _HEARTBEAT_EXPIRY. A
// non-positive value falls back to its default in NewClaimLoop.
type ClaimLoopConfig struct {
	// Poll is the claim-tick interval: each tick reaps stale claims then claims
	// pending intents while the Manager has capacity. Default 2s.
	Poll time.Duration
	// Heartbeat is how often a live session's goroutine stamps heartbeat_at (and
	// reads stop_requested). Default 5s. Must be well under Expiry.
	Heartbeat time.Duration
	// Expiry is how stale a heartbeat may get before the reaper marks the claim
	// dead (the owning worker is presumed crashed). Default 30s.
	Expiry time.Duration
}

// ClaimLoop drives the -mode voice worker's DB-driven session assignment (#491).
type ClaimLoop struct {
	store      IntentStore
	mgr        *Manager
	instanceID string
	log        *slog.Logger
	cfg        ClaimLoopConfig

	wg sync.WaitGroup // tracks live per-session goroutines for the graceful drain
}

// NewClaimLoop builds a claim loop over the intent store and the tenant-aware
// Manager. instanceID is this Voice Instance's identity (hostname-uuid8, minted
// per boot in cmd/glyphoxa) — the fence for its heartbeat/finish writes. A
// non-positive config duration takes its default.
func NewClaimLoop(store IntentStore, mgr *Manager, instanceID string, log *slog.Logger, cfg ClaimLoopConfig) *ClaimLoop {
	if log == nil {
		log = slog.Default()
	}
	if cfg.Poll <= 0 {
		cfg.Poll = 2 * time.Second
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = 5 * time.Second
	}
	if cfg.Expiry <= 0 {
		cfg.Expiry = 30 * time.Second
	}
	return &ClaimLoop{store: store, mgr: mgr, instanceID: instanceID, log: log, cfg: cfg}
}

// Run polls the claim plane until ctx is cancelled, then drains: SIGTERM stops
// claiming and waits for every live session's goroutine to end its session
// cleanly and finish its row within the drain window (AC5). Each tick reaps stale
// claims then claims-and-starts while the Manager has capacity.
func (l *ClaimLoop) Run(ctx context.Context) {
	ticker := time.NewTicker(l.cfg.Poll)
	defer ticker.Stop()
	// One immediate tick so a pending intent written just before boot is claimed
	// without waiting a full poll interval.
	l.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			// Stop claiming; the per-session goroutines observe the same ctx and each
			// ends its session cleanly + finishes its row. Wait for them (the drain).
			l.wg.Wait()
			return
		case <-ticker.C:
			l.tick(ctx)
		}
	}
}

// tick reaps stale claims, then claims and starts pending intents while the
// Manager has a free slot (#491). Capacity is checked BEFORE each claim so the
// loop never claims work it cannot run.
func (l *ClaimLoop) tick(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	// Every claim-plane call below is bounded by a per-op timeout (#483 M1,
	// min(Poll, 3s)) so a black-holed connection can never pin the tick — the
	// heartbeat goroutines are independent, but a pinned tick would stop reaping
	// and claiming for the whole worker.
	reapCtx, cancelReap := context.WithTimeout(ctx, dbOpTimeout(l.cfg.Poll))
	n, err := l.store.ReapDeadVoiceSessionIntents(reapCtx, l.cfg.Expiry)
	cancelReap()
	if err != nil {
		l.log.Warn("claim loop: reap dead intents", "err", err)
	} else if n > 0 {
		l.log.Warn("claim loop: reaped dead voice session intents (worker heartbeats expired)", "count", n)
	}
	// Close 'running' voice_sessions rows behind a now-terminal intent EVERY tick
	// (#483 L2; idempotent and cheap): gating this on a reap left a row stranded
	// when its intent finished 'done'/'failed' normally but the Manager's own
	// CloseVoiceSession write failed — no reap would ever fire for that Tenant, so
	// only a worker boot would have repaired it.
	recCtx, cancelRec := context.WithTimeout(ctx, dbOpTimeout(l.cfg.Poll))
	if rn, err := l.store.ReconcileWorkerOrphanedVoiceSessions(recCtx); err != nil {
		l.log.Warn("claim loop: reconcile orphaned sessions", "err", err)
	} else if rn > 0 {
		l.log.Warn("claim loop: closed orphaned voice sessions behind terminal intents", "count", rn)
	}
	cancelRec()

	for l.mgr.HasCapacity() {
		claimCtx, cancelClaim := context.WithTimeout(ctx, dbOpTimeout(l.cfg.Poll))
		intent, err := l.store.ClaimVoiceSessionIntent(claimCtx, l.instanceID)
		cancelClaim()
		if errors.Is(err, storage.ErrNotFound) {
			return // nothing pending to claim this tick
		}
		if err != nil {
			l.log.Warn("claim loop: claim intent", "err", err)
			return
		}
		l.startClaimed(ctx, intent)
	}
}

// startClaimed drives a freshly-claimed intent to live: Manager.Start, MarkLive,
// then spawn the per-session heartbeat goroutine. A Start refusal finishes the
// intent 'failed' with the reason (so it is never stranded 'claimed'); a MarkLive
// that finds no row (the reaper already marked it dead) stops the session it just
// started (ADR-0006 — no running a session the plane believes dead).
func (l *ClaimLoop) startClaimed(ctx context.Context, intent storage.VoiceSessionIntent) {
	vs, err := l.mgr.Start(ctx, intent.TenantID, intent.CampaignID)
	if err != nil {
		// ErrSessionLimit should not occur (tick gates on HasCapacity), but finishing
		// 'failed' rather than leaving the row 'claimed' guarantees no strand either
		// way. ErrSessionActive means the Tenant is somehow already live in THIS
		// worker — also a failed duplicate. The refusal is recorded WITH its typed
		// fail code (#483 M4) so the web tier's IntentControl re-maps it to the same
		// sentinel — and the RPC to the same actionable connect code — the -mode all
		// path produces, instead of flattening it to CodeInternal.
		l.log.Warn("claim loop: manager start refused claimed intent", "intent", intent.ID, "err", err)
		l.finish(intent.ID, storage.VoiceIntentFailed, EncodeStartFailure(err))
		return
	}

	mlCtx, cancelML := context.WithTimeout(ctx, dbOpTimeout(l.cfg.Poll))
	live, err := l.store.MarkVoiceSessionIntentLive(mlCtx, intent.ID, l.instanceID, vs.ID)
	cancelML()
	if err != nil {
		// Stop the just-started session either way. NotFound means the row was
		// already reaped dead (a superseded claim) — the row is terminal, so no
		// finish. Any OTHER error (a DB blip, or a SIGTERM cancelling ctx between
		// Claim and MarkLive) left the row 'claimed' with no heartbeat goroutine, so
		// finish it 'failed' on a detached ctx (mirroring the Start-refusal path) —
		// otherwise it lingers 'claimed' until the reaper marks it the wrong state
		// (review item 5, AC5's claimed-not-yet-live SIGTERM case).
		if _, serr := l.mgr.Stop(l.detached(), intent.TenantID); serr != nil && !errors.Is(serr, ErrNoActiveSession) {
			l.log.Warn("claim loop: stop after failed mark-live", "intent", intent.ID, "err", serr)
		}
		if errors.Is(err, storage.ErrNotFound) {
			l.log.Warn("claim loop: claim superseded before live (reaped); stopped the started session",
				"intent", intent.ID)
			return
		}
		l.log.Warn("claim loop: mark intent live; finishing failed", "intent", intent.ID, "err", err)
		l.finish(intent.ID, storage.VoiceIntentFailed, "mark-live failed: "+err.Error())
		return
	}

	l.wg.Add(1)
	go l.runSession(ctx, live)
}

// runSession owns a live intent's heartbeat lifecycle (#491): each Heartbeat tick
// it stamps the row and reads stop_requested, and detects the session ending on
// its own. It exits — finishing the intent — on a requested stop, a superseded
// claim (reaped: kill the local session, the row is already terminal), the loop
// self-exiting, or ctx cancellation (graceful shutdown).
func (l *ClaimLoop) runSession(ctx context.Context, intent storage.VoiceSessionIntent) {
	defer l.wg.Done()
	tenant := intent.TenantID
	ticker := time.NewTicker(l.cfg.Heartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Graceful shutdown (SIGTERM): end the session cleanly and finish the row
			// on a detached ctx (the run ctx is already cancelled).
			l.endSession(tenant, intent.ID, storage.VoiceIntentDone, "")
			return
		case <-ticker.C:
			if _, live, _ := l.mgr.Active(ctx, tenant); !live {
				// The loop self-exited (or was stopped elsewhere). Wait out the Manager's
				// end window first (#483 L3): between as.ended (Active reporting false)
				// and the active-entry clear, Manager.Start still collides
				// ErrSessionActive — finishing the intent here would let an instant
				// Tenant restart land exactly in that window and misreport 'failed'.
				// The window is bounded by the Manager's endTimeout budgets, so this
				// waits a few ticks at most (and even a stuck end-write clears the
				// entry — runLoop always deletes it).
				if l.mgr.Finalizing(tenant) {
					continue
				}
				l.finishSelfExit(intent)
				return
			}
			hbCtx, cancelHB := context.WithTimeout(ctx, dbOpTimeout(l.cfg.Heartbeat))
			stop, err := l.store.HeartbeatVoiceSessionIntent(hbCtx, intent.ID, l.instanceID)
			cancelHB()
			if errors.Is(err, storage.ErrNotFound) {
				// Superseded — the reaper marked this claim dead (this worker was judged
				// stale). Kill the local session; the row is already terminal, so no
				// finish (ADR-0006: never keep running a session the plane calls dead).
				l.log.Warn("claim loop: heartbeat superseded (claim reaped dead); stopping local session",
					"intent", intent.ID)
				if _, serr := l.mgr.Stop(l.detached(), tenant); serr != nil && !errors.Is(serr, ErrNoActiveSession) {
					l.log.Warn("claim loop: stop after superseded heartbeat", "intent", intent.ID, "err", serr)
				}
				return
			}
			if err != nil {
				l.log.Warn("claim loop: heartbeat", "intent", intent.ID, "err", err)
				continue // a transient DB hiccup: retry next tick, don't kill a live session
			}
			if stop {
				// The web tier flagged a stop: wind the session down and finish 'done'.
				l.endSession(tenant, intent.ID, storage.VoiceIntentDone, "")
				return
			}
		}
	}
}

// finishSelfExit finishes an intent whose session ended on its own: the Manager
// already wrote the terminal voice_sessions row, so carry ITS outcome onto the
// intent (#483 L4) — a row closed 'failed' (a loop fault) finishes the intent
// 'failed' with the recorded end_reason instead of a clean-looking 'done' with an
// empty last_error, so the durable history never misreports a fault as a clean
// stop. A failed outcome read logs and degrades to 'done' (the pre-#483 record).
func (l *ClaimLoop) finishSelfExit(intent storage.VoiceSessionIntent) {
	status, lastError := storage.VoiceIntentDone, ""
	if intent.VoiceSessionID.Valid {
		ctx, cancel := context.WithTimeout(context.Background(), intentEndTimeout)
		vs, err := l.store.GetVoiceSession(ctx, intent.VoiceSessionID.UUID)
		cancel()
		switch {
		case err != nil:
			l.log.Warn("claim loop: read session outcome for self-exited intent", "intent", intent.ID, "err", err)
		case vs.Status == storage.VoiceSessionFailed:
			status = storage.VoiceIntentFailed
			if vs.EndReason != nil {
				lastError = *vs.EndReason
			}
		}
	}
	l.finish(intent.ID, status, lastError)
}

// endSession stops the Manager's session for the tenant (blocking until its loop
// ends and the voice_sessions row lands) and finishes the intent, both on a
// detached ctx so a cancelled run ctx cannot abort the clean wind-down.
func (l *ClaimLoop) endSession(tenant uuid.UUID, intentID uuid.UUID, status storage.VoiceSessionIntentStatus, lastError string) {
	if _, err := l.mgr.Stop(l.detached(), tenant); err != nil && !errors.Is(err, ErrNoActiveSession) {
		l.log.Warn("claim loop: stop session", "intent", intentID, "err", err)
	}
	l.finish(intentID, status, lastError)
}

// finish writes the intent's terminal state on a detached, bounded ctx. A
// superseded write (the reaper won the race) is expected and swallowed.
func (l *ClaimLoop) finish(intentID uuid.UUID, status storage.VoiceSessionIntentStatus, lastError string) {
	ctx, cancel := context.WithTimeout(context.Background(), intentEndTimeout)
	defer cancel()
	if _, err := l.store.FinishVoiceSessionIntent(ctx, intentID, l.instanceID, status, lastError); err != nil && !errors.Is(err, storage.ErrNotFound) {
		l.log.Warn("claim loop: finish intent", "intent", intentID, "status", status, "err", err)
	}
}

// detached returns a background context for the clean wind-down (Manager.Stop)
// that a cancelled run ctx must not abort — Stop blocks until the loop ends and
// the voice_sessions row lands, itself bounded by the Manager's endTimeout
// budget, so no timeout is needed here (mirrors the Manager's WithoutCancel
// end-write posture).
func (l *ClaimLoop) detached() context.Context {
	return context.Background()
}
