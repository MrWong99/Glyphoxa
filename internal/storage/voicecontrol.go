package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Cross-pod live voice controls (#503, ADR-0057): the requested-control queue
// hanging off the voice_session_intents claim plane. A requester (the presence
// owner dispatching a slash command, or the split web tier's RPC) writes a
// pending row; the worker HOSTING the intent's session drains all pending rows
// in (created_at, id) order on its heartbeat tick, executes each against its
// LOCAL Manager, and writes the terminal status (done/failed) the requester
// polls to. DB-write-then-poll only — no LISTEN/NOTIFY (ADR-0057 (b)); a
// control row never re-targets another instance (ADR-0006/0057 (e)). Terminal
// writes are fenced WHERE status='pending' so a lost race yields ErrNotFound,
// mirroring the intent-row idiom.

// VoiceSessionControlKind names one control verb.
type VoiceSessionControlKind string

const (
	// VoiceControlMuteAgent mutes/unmutes one voiced Agent (Manager.SetAgentMute).
	VoiceControlMuteAgent VoiceSessionControlKind = "mute_agent"
	// VoiceControlMuteAll mutes/unmutes every voiced Agent (Manager.SetAllMute).
	VoiceControlMuteAll VoiceSessionControlKind = "mute_all"
	// VoiceControlSay makes one voiced Agent speak SayText (Manager.SayAs).
	VoiceControlSay VoiceSessionControlKind = "say"
	// VoiceControlButlerSay makes the Butler speak SayText (Manager.SpeakAsButler)
	// — the voiced-recap relay.
	VoiceControlButlerSay VoiceSessionControlKind = "butler_say"
)

// VoiceSessionControlStatus is a control row's lifecycle state.
type VoiceSessionControlStatus string

const (
	// VoiceControlPending: written by the requester, not yet executed.
	VoiceControlPending VoiceSessionControlStatus = "pending"
	// VoiceControlDone: the hosting worker executed it successfully.
	VoiceControlDone VoiceSessionControlStatus = "done"
	// VoiceControlFailed: execution failed (LastError carries the encoded cause),
	// the requester timed out, or the session ended with the row still pending.
	VoiceControlFailed VoiceSessionControlStatus = "failed"
)

// VoiceSessionControl is one row of the requested-control queue (#503).
type VoiceSessionControl struct {
	ID        uuid.UUID
	IntentID  uuid.UUID
	TenantID  uuid.UUID
	Kind      VoiceSessionControlKind
	AgentID   string
	SayText   string
	Muted     bool
	Status    VoiceSessionControlStatus
	ResultIDs []string
	LastError string
	CreatedAt time.Time
	EndedAt   *time.Time
}

const voiceControlColumns = `
	id, intent_id, tenant_id, kind, agent_id, say_text, muted,
	status, result_ids, last_error, created_at, ended_at`

func scanVoiceSessionControl(row pgx.Row) (VoiceSessionControl, error) {
	var c VoiceSessionControl
	err := row.Scan(
		&c.ID, &c.IntentID, &c.TenantID, &c.Kind, &c.AgentID, &c.SayText, &c.Muted,
		&c.Status, &c.ResultIDs, &c.LastError, &c.CreatedAt, &c.EndedAt,
	)
	return c, err
}

// CreateVoiceSessionControl writes a pending control row for an intent and
// returns it. The requester then polls GetVoiceSessionControl until the hosting
// worker writes a terminal status.
func (s *Store) CreateVoiceSessionControl(ctx context.Context, c VoiceSessionControl) (VoiceSessionControl, error) {
	row := s.db.QueryRow(ctx,
		`INSERT INTO voice_session_controls (intent_id, tenant_id, kind, agent_id, say_text, muted)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING `+voiceControlColumns,
		c.IntentID, c.TenantID, c.Kind, c.AgentID, c.SayText, c.Muted)
	out, err := scanVoiceSessionControl(row)
	if err != nil {
		return VoiceSessionControl{}, fmt.Errorf("storage: create voice session control for intent %s: %w", c.IntentID, err)
	}
	return out, nil
}

// ListPendingVoiceSessionControls returns an intent's pending control rows in
// (created_at, id) order — the hosting worker's per-heartbeat drain scan. The
// order matters for 'say' (utterances must land in request order); mutes are
// idempotent so the queue is harmless for them.
func (s *Store) ListPendingVoiceSessionControls(ctx context.Context, intentID uuid.UUID) ([]VoiceSessionControl, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+voiceControlColumns+`
		   FROM voice_session_controls
		  WHERE intent_id = $1 AND status = 'pending'
		  ORDER BY created_at, id`, intentID)
	if err != nil {
		return nil, fmt.Errorf("storage: list pending voice session controls for intent %s: %w", intentID, err)
	}
	defer rows.Close()
	var out []VoiceSessionControl
	for rows.Next() {
		c, err := scanVoiceSessionControl(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan voice session control: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list pending voice session controls for intent %s: %w", intentID, err)
	}
	return out, nil
}

// FinishVoiceSessionControl writes a control's terminal state (done/failed) with
// its result ids / last_error and ended_at = now(). Fenced WHERE status='pending'
// so a lost race (the requester's timeout cancel, or the sweep, won) yields
// ErrNotFound and the caller does not overwrite a settled row.
func (s *Store) FinishVoiceSessionControl(ctx context.Context, id uuid.UUID, status VoiceSessionControlStatus, resultIDs []string, lastError string) (VoiceSessionControl, error) {
	if resultIDs == nil {
		resultIDs = []string{}
	}
	row := s.db.QueryRow(ctx,
		`UPDATE voice_session_controls
		    SET status = $2, result_ids = $3, last_error = $4, ended_at = now()
		  WHERE id = $1 AND status = 'pending'
		 RETURNING `+voiceControlColumns,
		id, status, resultIDs, lastError)
	c, err := scanVoiceSessionControl(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return VoiceSessionControl{}, ErrNotFound
	}
	if err != nil {
		return VoiceSessionControl{}, fmt.Errorf("storage: finish voice session control %s: %w", id, err)
	}
	return c, nil
}

// GetVoiceSessionControl loads one control row by id, or ErrNotFound — the
// requester's poll read.
func (s *Store) GetVoiceSessionControl(ctx context.Context, id uuid.UUID) (VoiceSessionControl, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+voiceControlColumns+` FROM voice_session_controls WHERE id = $1`, id)
	c, err := scanVoiceSessionControl(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return VoiceSessionControl{}, ErrNotFound
	}
	if err != nil {
		return VoiceSessionControl{}, fmt.Errorf("storage: get voice session control %s: %w", id, err)
	}
	return c, nil
}

// CancelPendingVoiceSessionControl fails a still-pending control with
// 'requester timed out' — the requester's budget-expiry best-effort cancel.
// Fenced WHERE status='pending': a row the worker already finished is left
// untouched and false is returned (the worker won the race; the requester's
// honest "not confirmed" reply stands either way).
func (s *Store) CancelPendingVoiceSessionControl(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE voice_session_controls
		    SET status = 'failed', last_error = 'requester timed out', ended_at = now()
		  WHERE id = $1 AND status = 'pending'`, id)
	if err != nil {
		return false, fmt.Errorf("storage: cancel pending voice session control %s: %w", id, err)
	}
	return tag.RowsAffected() > 0, nil
}

// SweepOrphanedVoiceSessionControls fails every pending control whose intent is
// already terminal (done/dead/failed) with 'session ended' — controls are never
// dispatched during a wind-down (dispatch is gated on the session being live),
// so a stopping session's stragglers die here (or via the requester's own
// budget cancel) instead of sitting pending forever. Run each claim-loop tick.
func (s *Store) SweepOrphanedVoiceSessionControls(ctx context.Context) (int64, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE voice_session_controls c
		    SET status = 'failed', last_error = 'session ended', ended_at = now()
		  WHERE c.status = 'pending'
		    AND EXISTS (
		          SELECT 1 FROM voice_session_intents i
		           WHERE i.id = c.intent_id
		             AND i.status IN ('done', 'dead', 'failed')
		    )`)
	if err != nil {
		return 0, fmt.Errorf("storage: sweep orphaned voice session controls: %w", err)
	}
	return tag.RowsAffected(), nil
}
