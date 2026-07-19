package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Voice-session claim-plane persistence (#491, ADR-0057 (b)): the web tier writes
// an INTENT row on Start, a -mode voice worker claims the oldest pending one with
// the FOR UPDATE SKIP LOCKED idiom the job runner proves (ADR-0049,
// internal/storage/job.go), heartbeats while its loop is live, and finishes it on
// end. No mid-session takeover (ADR-0006/0057 (e)): a stale heartbeat marks the
// row 'dead' — the Tenant restarts, the session is never handed to another
// instance. These queries mirror the job.go idiom: SKIP-LOCKED claim, instance
// fencing on every terminal/heartbeat write, ErrNotFound for a superseded caller.

// ErrIntentActive is returned by CreateVoiceSessionIntent when the Tenant already
// has a non-terminal (pending/claimed/live) intent — the one-live-per-tenant
// partial UNIQUE index (23505). The RPC layer maps it to CodeAlreadyExists,
// mirroring session.ErrSessionActive.
var ErrIntentActive = errors.New("storage: a voice session intent is already active for this tenant")

const voiceIntentColumns = `
	id, tenant_id, campaign_id, status, instance_id, voice_session_id,
	stop_requested, last_error, created_at, claimed_at, heartbeat_at, ended_at`

func scanVoiceSessionIntent(row pgx.Row) (VoiceSessionIntent, error) {
	var v VoiceSessionIntent
	err := row.Scan(
		&v.ID, &v.TenantID, &v.CampaignID, &v.Status, &v.InstanceID, &v.VoiceSessionID,
		&v.StopRequested, &v.LastError, &v.CreatedAt, &v.ClaimedAt, &v.HeartbeatAt, &v.EndedAt,
	)
	return v, err
}

// CreateVoiceSessionIntent writes a 'pending' claim-plane row for a Tenant's
// Campaign and returns it. A second create while the Tenant already has a
// non-terminal (pending/claimed/live) intent trips the one-live-per-tenant
// partial UNIQUE index (23505) and yields ErrIntentActive — the per-Tenant
// single-active guard, now durable in the DB rather than the in-process Manager.
func (s *Store) CreateVoiceSessionIntent(ctx context.Context, tenantID, campaignID uuid.UUID) (VoiceSessionIntent, error) {
	row := s.db.QueryRow(ctx,
		`INSERT INTO voice_session_intents (tenant_id, campaign_id)
		 VALUES ($1, $2)
		 RETURNING `+voiceIntentColumns,
		tenantID, campaignID)
	v, err := scanVoiceSessionIntent(row)
	if err != nil {
		if code, ok := pgErrCode(err); ok && code == "23505" {
			return VoiceSessionIntent{}, ErrIntentActive
		}
		return VoiceSessionIntent{}, fmt.Errorf("storage: create voice session intent for tenant %s: %w", tenantID, err)
	}
	return v, nil
}

// ClaimVoiceSessionIntent atomically claims the single oldest PENDING intent for
// instanceID and returns it in its post-claim state (status='claimed',
// instance_id set, claimed_at/heartbeat_at = now()). No pending intent yields
// ErrNotFound.
//
// The claim is ONE atomic UPDATE whose target is chosen by a `SELECT … FOR UPDATE
// SKIP LOCKED LIMIT 1` subquery, so two concurrent workers skip each other's
// locked candidates and claim two DISTINCT intents (never the same one) — exactly
// the job-runner idiom (ADR-0049). It claims 'pending' ONLY: a 'claimed'/'live'
// row whose worker crashed is NEVER re-claimed here (ADR-0006/0057 (e) — no
// mid-session takeover); ReapDeadVoiceSessionIntents marks such a row 'dead'
// instead, and the Tenant restarts.
func (s *Store) ClaimVoiceSessionIntent(ctx context.Context, instanceID string) (VoiceSessionIntent, error) {
	row := s.db.QueryRow(ctx,
		`UPDATE voice_session_intents
		    SET status = 'claimed',
		        instance_id = $1,
		        claimed_at = now(),
		        heartbeat_at = now()
		  WHERE id = (
		        SELECT id FROM voice_session_intents
		         WHERE status = 'pending'
		         ORDER BY created_at, id
		         FOR UPDATE SKIP LOCKED
		         LIMIT 1
		  )
		 RETURNING `+voiceIntentColumns,
		instanceID)
	v, err := scanVoiceSessionIntent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return VoiceSessionIntent{}, ErrNotFound
	}
	if err != nil {
		return VoiceSessionIntent{}, fmt.Errorf("storage: claim voice session intent: %w", err)
	}
	return v, nil
}

// MarkVoiceSessionIntentLive flips instanceID's claimed intent to 'live' and
// binds the voice_sessions row the worker created, stamping a fresh heartbeat. It
// is fenced by (id, instance_id, status='claimed'): a superseded caller (a reaper
// already marked it dead, or a different instance owns it) matches no row and
// yields ErrNotFound.
func (s *Store) MarkVoiceSessionIntentLive(ctx context.Context, id uuid.UUID, instanceID string, voiceSessionID uuid.UUID) (VoiceSessionIntent, error) {
	row := s.db.QueryRow(ctx,
		`UPDATE voice_session_intents
		    SET status = 'live',
		        voice_session_id = $3,
		        heartbeat_at = now()
		  WHERE id = $1 AND instance_id = $2 AND status = 'claimed'
		 RETURNING `+voiceIntentColumns,
		id, instanceID, voiceSessionID)
	v, err := scanVoiceSessionIntent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return VoiceSessionIntent{}, ErrNotFound
	}
	if err != nil {
		return VoiceSessionIntent{}, fmt.Errorf("storage: mark voice session intent %s live: %w", id, err)
	}
	return v, nil
}

// HeartbeatVoiceSessionIntent stamps heartbeat_at = now() for instanceID's live
// (or claimed) intent and reports whether a stop was requested, so the owning
// worker learns on each beat whether the web tier asked it to wind down. It is
// fenced WHERE instance_id=$2 AND status IN ('claimed','live'): a row the reaper
// already marked dead (worker declared stale), or one now owned by another
// instance, matches nothing and yields ErrNotFound — the caller reads that as
// "my claim was superseded" and kills its local session (ADR-0006: it must not
// keep running a session the plane believes is dead).
func (s *Store) HeartbeatVoiceSessionIntent(ctx context.Context, id uuid.UUID, instanceID string) (stopRequested bool, err error) {
	row := s.db.QueryRow(ctx,
		`UPDATE voice_session_intents
		    SET heartbeat_at = now()
		  WHERE id = $1 AND instance_id = $2 AND status IN ('claimed', 'live')
		 RETURNING stop_requested`,
		id, instanceID)
	if serr := row.Scan(&stopRequested); serr != nil {
		if errors.Is(serr, pgx.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, fmt.Errorf("storage: heartbeat voice session intent %s: %w", id, serr)
	}
	return stopRequested, nil
}

// RequestVoiceSessionStop asks the claim plane to wind an intent down. A still
// 'pending' intent (no worker has claimed it) is taken straight to 'done' with
// ended_at set — there is no worker to honor a flag, so the stop resolves
// immediately. A claimed/live intent instead sets stop_requested=true, which the
// owning worker sees on its next heartbeat and acts on. A missing id, or an
// already-terminal intent, yields ErrNotFound. Returns the updated row.
func (s *Store) RequestVoiceSessionStop(ctx context.Context, id uuid.UUID) (VoiceSessionIntent, error) {
	row := s.db.QueryRow(ctx,
		`UPDATE voice_session_intents
		    SET status = CASE WHEN status = 'pending' THEN 'done' ELSE status END,
		        stop_requested = true,
		        ended_at = CASE WHEN status = 'pending' THEN now() ELSE ended_at END
		  WHERE id = $1 AND status IN ('pending', 'claimed', 'live')
		 RETURNING `+voiceIntentColumns,
		id)
	v, err := scanVoiceSessionIntent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return VoiceSessionIntent{}, ErrNotFound
	}
	if err != nil {
		return VoiceSessionIntent{}, fmt.Errorf("storage: request voice session intent %s stop: %w", id, err)
	}
	return v, nil
}

// FinishVoiceSessionIntent writes a terminal state (done/failed/dead) for
// instanceID's intent with a recorded last_error and ended_at = now(). Fenced by
// (id, instance_id) and a non-terminal current status so a superseded caller
// (the reaper already marked it dead) matches no row and yields ErrNotFound. The
// worker calls it once its local session has fully wound down.
func (s *Store) FinishVoiceSessionIntent(ctx context.Context, id uuid.UUID, instanceID string, status VoiceSessionIntentStatus, lastError string) (VoiceSessionIntent, error) {
	row := s.db.QueryRow(ctx,
		`UPDATE voice_session_intents
		    SET status = $3, last_error = $4, ended_at = now()
		  WHERE id = $1 AND instance_id = $2 AND status IN ('claimed', 'live')
		 RETURNING `+voiceIntentColumns,
		id, instanceID, status, lastError)
	v, err := scanVoiceSessionIntent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return VoiceSessionIntent{}, ErrNotFound
	}
	if err != nil {
		return VoiceSessionIntent{}, fmt.Errorf("storage: finish voice session intent %s: %w", id, err)
	}
	return v, nil
}

// ReapDeadVoiceSessionIntents marks 'dead' every claimed/live intent whose
// heartbeat is older than expiry — the owning Voice Instance is presumed crashed
// (ADR-0006/0057 (e): no takeover, so a stale claim is a death, not a hand-off).
// The Tenant sees the dead state and can restart. A fresh heartbeat (within
// expiry) is untouched. Returns how many rows were reaped. Called once per claim
// loop tick before claiming, mirroring SweepExpiredJobs (ADR-0049).
func (s *Store) ReapDeadVoiceSessionIntents(ctx context.Context, expiry time.Duration) (int64, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE voice_session_intents
		    SET status = 'dead',
		        last_error = CASE WHEN last_error = '' THEN 'worker heartbeat expired' ELSE last_error END,
		        ended_at = now()
		  WHERE status IN ('claimed', 'live')
		    AND heartbeat_at < now() - make_interval(secs => $1)`,
		expiry.Seconds())
	if err != nil {
		return 0, fmt.Errorf("storage: reap dead voice session intents: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ReconcileWorkerOrphanedVoiceSessions closes 'running' voice_sessions rows that
// a -mode voice worker left behind on a crash — but ONLY those whose owning intent
// has gone terminal (dead/done/failed), NEVER a row an intent still holds
// live/claimed (#491, the reviewer-flagged process-blindness): a plain
// ReconcileOrphanedVoiceSessions is process-blind, so two workers booting would
// close each other's live 'running' rows. Scoping the sweep to rows behind a
// terminal intent makes it safe for a pool: a live worker's row (its intent still
// live) is untouched, while a crashed worker's row (its intent reaped dead, or
// finished done/failed) is closed. Returns how many rows were closed. The -mode
// all path keeps the broad [ReconcileOrphanedVoiceSessions] (it writes no intents).
func (s *Store) ReconcileWorkerOrphanedVoiceSessions(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE voice_sessions vs
		    SET ended_at = now(), status = $1, end_reason = $2
		  WHERE vs.status = $3
		    AND EXISTS (
		          SELECT 1 FROM voice_session_intents i
		           WHERE i.voice_session_id = vs.id
		             AND i.status IN ('dead', 'done', 'failed')
		    )`,
		VoiceSessionEnded, VoiceSessionReasonOrphaned, VoiceSessionRunning)
	if err != nil {
		return 0, fmt.Errorf("storage: reconcile worker-orphaned voice sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ReapVoiceSessionIntentIfExpired marks ONE claimed/live intent dead when its
// heartbeat is older than expiry — the zero-worker escape (#491 review item 4):
// the reaper otherwise runs only inside a worker's claim tick, so with NO healthy
// worker a dead claimed/live intent would never expire and its Tenant would stay
// blocked (ErrIntentActive) forever. IntentControl.Start calls this on the exact
// row blocking a retry. Fenced by id + a stale heartbeat, so a live row (fresh
// beat) or a foreign status is untouched. Returns whether it reaped a row.
func (s *Store) ReapVoiceSessionIntentIfExpired(ctx context.Context, id uuid.UUID, expiry time.Duration) (bool, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE voice_session_intents
		    SET status = 'dead',
		        last_error = CASE WHEN last_error = '' THEN 'worker heartbeat expired' ELSE last_error END,
		        ended_at = now()
		  WHERE id = $1
		    AND status IN ('claimed', 'live')
		    AND heartbeat_at < now() - make_interval(secs => $2)`,
		id, expiry.Seconds())
	if err != nil {
		return false, fmt.Errorf("storage: reap voice session intent %s if expired: %w", id, err)
	}
	return tag.RowsAffected() > 0, nil
}

// GetVoiceSessionIntent loads one intent by id, or ErrNotFound. It backs the
// IntentControl poll (the web tier watching a Start it wrote) and tests.
func (s *Store) GetVoiceSessionIntent(ctx context.Context, id uuid.UUID) (VoiceSessionIntent, error) {
	row := s.db.QueryRow(ctx, `SELECT `+voiceIntentColumns+` FROM voice_session_intents WHERE id = $1`, id)
	v, err := scanVoiceSessionIntent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return VoiceSessionIntent{}, ErrNotFound
	}
	if err != nil {
		return VoiceSessionIntent{}, fmt.Errorf("storage: get voice session intent %s: %w", id, err)
	}
	return v, nil
}

// IsCampaignLiveIntent reports whether campaignID has any non-terminal
// (pending/claimed/live) intent — the split-mode archive/delete live-guard
// (#491): the web tier drives no in-process session, so "is this Campaign live"
// is a claim-plane read rather than a Manager scan. Correct across the worker
// pool.
func (s *Store) IsCampaignLiveIntent(ctx context.Context, campaignID uuid.UUID) (bool, error) {
	var one int
	err := s.db.QueryRow(ctx,
		`SELECT 1 FROM voice_session_intents
		  WHERE campaign_id = $1 AND status IN ('pending', 'claimed', 'live')
		  LIMIT 1`, campaignID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("storage: campaign %s live-intent check: %w", campaignID, err)
	}
	return true, nil
}

// AnyLiveVoiceSessionIntent reports whether ANY non-terminal intent exists — the
// split-mode health signal (#491, the claim-plane sibling of Manager.AnyLive) the
// web tier reads for the Discord health short-circuit.
func (s *Store) AnyLiveVoiceSessionIntent(ctx context.Context) (bool, error) {
	var one int
	err := s.db.QueryRow(ctx,
		`SELECT 1 FROM voice_session_intents
		  WHERE status IN ('pending', 'claimed', 'live') LIMIT 1`).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("storage: any-live-intent check: %w", err)
	}
	return true, nil
}

// GetLiveVoiceSessionIntentForTenant returns the Tenant's current non-terminal
// (pending/claimed/live) intent, or ErrNotFound when the Tenant has none — the
// per-Tenant read backing IntentControl.Active (the split-mode sibling of
// Manager.Active). The one-live-per-tenant index guarantees at most one row
// matches.
func (s *Store) GetLiveVoiceSessionIntentForTenant(ctx context.Context, tenantID uuid.UUID) (VoiceSessionIntent, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+voiceIntentColumns+`
		   FROM voice_session_intents
		  WHERE tenant_id = $1 AND status IN ('pending', 'claimed', 'live')
		  ORDER BY created_at DESC
		  LIMIT 1`, tenantID)
	v, err := scanVoiceSessionIntent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return VoiceSessionIntent{}, ErrNotFound
	}
	if err != nil {
		return VoiceSessionIntent{}, fmt.Errorf("storage: get live voice session intent for tenant %s: %w", tenantID, err)
	}
	return v, nil
}
