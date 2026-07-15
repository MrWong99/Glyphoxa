package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Voice-session persistence (#72, ADR-0039): the SessionManager INSERTs a row on
// Start (status='running'), UPDATEs it on Stop / loop exit (ended_at + status),
// and the Session screen reads the active or last session back. The queries are
// thin and domain-neutral, mirroring the rest of the storage layer.

const voiceSessionColumns = `
	id, campaign_id, started_at, ended_at, status, line_count, end_reason`

func scanVoiceSession(row pgx.Row) (VoiceSession, error) {
	var v VoiceSession
	err := row.Scan(
		&v.ID, &v.CampaignID, &v.StartedAt, &v.EndedAt, &v.Status, &v.LineCount, &v.EndReason,
	)
	return v, err
}

// CreateVoiceSession opens a Voice Session for a Campaign: it INSERTs a row with
// status='running' and started_at=now() and returns it. The SessionManager holds
// the returned id to End the session on Stop.
func (s *Store) CreateVoiceSession(ctx context.Context, campaignID uuid.UUID) (VoiceSession, error) {
	row := s.db.QueryRow(ctx,
		`INSERT INTO voice_sessions (campaign_id, status)
		 VALUES ($1, $2)
		 RETURNING `+voiceSessionColumns,
		campaignID, VoiceSessionRunning)
	v, err := scanVoiceSession(row)
	if err != nil {
		return VoiceSession{}, fmt.Errorf("storage: create voice session for campaign %s: %w", campaignID, err)
	}
	return v, nil
}

// ImportVoiceSession inserts a historical Voice Session from a Campaign Bundle
// (#292, ADR-0053): unlike [CreateVoiceSession], which mints a fresh 'running'
// row with started_at=now(), this writes the bundle's started_at, ended_at,
// status, line_count and end_reason VERBATIM and returns the freshly minted id.
// The caller (bundle.Import) guarantees a terminal status (a non-terminal one is
// coerced to 'ended' upstream) and a non-nil ended_at, so no live loop ever owns
// an imported row. end_reason is written NULL when nil. It runs inside the import
// transaction, so a later failure rolls the row back with the rest.
func (s *Store) ImportVoiceSession(ctx context.Context, v VoiceSession) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx,
		`INSERT INTO voice_sessions
		   (campaign_id, started_at, ended_at, status, line_count, end_reason)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING id`,
		v.CampaignID, v.StartedAt, v.EndedAt, v.Status, v.LineCount, v.EndReason).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("storage: import voice session for campaign %s: %w", v.CampaignID, err)
	}
	return id, nil
}

// CloseVoiceSession closes a running Voice Session with an explicit terminal
// status and end_reason: it sets ended_at=now(), status, the final line_count, and
// end_reason (NULL when endReason is nil), returning the updated row. A missing id
// yields ErrNotFound. It is the single terminal-write seam (#123): [EndVoiceSession]
// delegates to it for a clean stop ('ended', NULL reason), and the session Manager
// calls it directly with 'failed' + the readable cause on a fatal gateway rejection.
func (s *Store) CloseVoiceSession(ctx context.Context, id uuid.UUID, status VoiceSessionStatus, lineCount int, endReason *string) (VoiceSession, error) {
	row := s.db.QueryRow(ctx,
		`UPDATE voice_sessions
		    SET ended_at = now(), status = $2, line_count = $3, end_reason = $4
		  WHERE id = $1
		 RETURNING `+voiceSessionColumns,
		id, status, lineCount, endReason)
	v, err := scanVoiceSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return VoiceSession{}, ErrNotFound
	}
	if err != nil {
		return VoiceSession{}, fmt.Errorf("storage: close voice session %s: %w", id, err)
	}
	return v, nil
}

// EndVoiceSession closes a running Voice Session cleanly: status='ended' with a
// NULL end_reason and the final line_count, returning the updated row. A missing
// id yields ErrNotFound. It is a thin wrapper over [CloseVoiceSession] — the
// clean-stop path that leaves end_reason NULL (distinct from orphaned/failed).
func (s *Store) EndVoiceSession(ctx context.Context, id uuid.UUID, lineCount int) (VoiceSession, error) {
	return s.CloseVoiceSession(ctx, id, VoiceSessionEnded, lineCount, nil)
}

// ReconcileOrphanedVoiceSessions closes every Voice Session row still marked
// 'running' — at startup no live loop exists, so any such row is an orphan from
// a crash or a failed end-write (#143). Each is stamped ended_at=now(),
// status='ended' and the distinguishing VoiceSessionReasonOrphaned end_reason
// (a clean end leaves end_reason NULL). Returns how many rows were closed.
// Called by the SessionManager at boot, before any session can start.
func (s *Store) ReconcileOrphanedVoiceSessions(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE voice_sessions
		    SET ended_at = now(), status = $1, end_reason = $2
		  WHERE status = $3`,
		VoiceSessionEnded, VoiceSessionReasonOrphaned, VoiceSessionRunning)
	if err != nil {
		return 0, fmt.Errorf("storage: reconcile orphaned voice sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// GetVoiceSession loads one Voice Session by id, or ErrNotFound.
func (s *Store) GetVoiceSession(ctx context.Context, id uuid.UUID) (VoiceSession, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+voiceSessionColumns+` FROM voice_sessions WHERE id = $1`, id)
	v, err := scanVoiceSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return VoiceSession{}, ErrNotFound
	}
	if err != nil {
		return VoiceSession{}, fmt.Errorf("storage: get voice session %s: %w", id, err)
	}
	return v, nil
}

// VoiceSessionInTenant reports whether the Voice Session belongs to the Tenant
// (session → campaign → tenant). It backs the transcript snapshot/SSE mounts'
// tenant-scoped 404 posture (#439, via transcript.TenantScope): false covers
// both a foreign-tenant session and one that does not exist at all, so the
// caller's 404 never reveals which.
func (s *Store) VoiceSessionInTenant(ctx context.Context, tenantID, sessionID uuid.UUID) (bool, error) {
	var one int
	err := s.db.QueryRow(ctx,
		`SELECT 1
		   FROM voice_sessions v
		   JOIN campaign c ON c.id = v.campaign_id
		  WHERE v.id = $1 AND c.tenant_id = $2`, sessionID, tenantID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("storage: voice session %s in tenant %s: %w", sessionID, tenantID, err)
	}
	return true, nil
}

// ListVoiceSessions returns a Campaign's Voice Sessions newest-first (started_at
// DESC, id DESC — the same tiebreak as GetLatestVoiceSession), the running row
// included, capped at limit. It backs the Session screen's past-session picker
// (#270): the operator picks a prior session to replay its persisted transcript.
// It reuses voiceSessionColumns/scanVoiceSession and is served by
// voice_sessions_campaign_idx (no migration). An empty result is not an error
// (the never-run picker state).
func (s *Store) ListVoiceSessions(ctx context.Context, campaignID uuid.UUID, limit int) ([]VoiceSession, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+voiceSessionColumns+`
		   FROM voice_sessions
		  WHERE campaign_id = $1
		  ORDER BY started_at DESC, id DESC
		  LIMIT $2`, campaignID, limit)
	if err != nil {
		return nil, fmt.Errorf("storage: list voice sessions for campaign %s: %w", campaignID, err)
	}
	defer rows.Close()

	sessions := make([]VoiceSession, 0)
	for rows.Next() {
		v, err := scanVoiceSession(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan voice session for campaign %s: %w", campaignID, err)
		}
		sessions = append(sessions, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list voice sessions for campaign %s: %w", campaignID, err)
	}
	return sessions, nil
}

// GetLatestVoiceSession returns a Campaign's most-recently-started Voice Session,
// or ErrNotFound when none has ever run. It backs the Session screen's idle
// last-session summary (#72): when no session is active, the screen shows when
// the prior session ended and its line count.
func (s *Store) GetLatestVoiceSession(ctx context.Context, campaignID uuid.UUID) (VoiceSession, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+voiceSessionColumns+`
		   FROM voice_sessions
		  WHERE campaign_id = $1
		  ORDER BY started_at DESC, id DESC
		  LIMIT 1`, campaignID)
	v, err := scanVoiceSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return VoiceSession{}, ErrNotFound
	}
	if err != nil {
		return VoiceSession{}, fmt.Errorf("storage: get latest voice session for campaign %s: %w", campaignID, err)
	}
	return v, nil
}
