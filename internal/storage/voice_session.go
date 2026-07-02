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

// EndVoiceSession closes a running Voice Session: it sets ended_at=now(),
// status='ended' and the final line_count, returning the updated row. A missing
// id yields ErrNotFound.
func (s *Store) EndVoiceSession(ctx context.Context, id uuid.UUID, lineCount int) (VoiceSession, error) {
	row := s.db.QueryRow(ctx,
		`UPDATE voice_sessions
		    SET ended_at = now(), status = $2, line_count = $3
		  WHERE id = $1
		 RETURNING `+voiceSessionColumns,
		id, VoiceSessionEnded, lineCount)
	v, err := scanVoiceSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return VoiceSession{}, ErrNotFound
	}
	if err != nil {
		return VoiceSession{}, fmt.Errorf("storage: end voice session %s: %w", id, err)
	}
	return v, nil
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
