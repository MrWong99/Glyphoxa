package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Planning thread persistence (#592, ADR-0062): the Butler planning chat's
// per-Campaign threads and their prose messages. Threads are full data
// citizens from day one — they cascade with the campaign delete (the 00053
// FKs) and join the Campaign Bundle — so nothing here needs a sweep. Tool-call
// intermediates are deliberately not persisted: the thread is the prose
// conversation; world knowledge re-arrives through the tool belt.

// PlanningThread is one persisted Butler planning chat conversation.
type PlanningThread struct {
	ID         uuid.UUID
	CampaignID uuid.UUID
	// Title is the thread's display name; auto-filled from the first user
	// message when the GM did not name it.
	Title     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PlanningMessage is one prose turn of a planning thread. Role is the two-value
// chat vocabulary ('user' / 'assistant'); Seq is the thread-local order the
// exchange loop appends under.
type PlanningMessage struct {
	ID         uuid.UUID
	ThreadID   uuid.UUID
	CampaignID uuid.UUID
	Seq        int64
	Role       string
	Content    string
	CreatedAt  time.Time
}

// The two planning message roles. The CHECK constraint in 00053 mirrors them.
const (
	PlanningRoleUser      = "user"
	PlanningRoleAssistant = "assistant"
)

const planningThreadColumns = `
	id, campaign_id, title, created_at, updated_at`

func scanPlanningThread(row pgx.Row) (PlanningThread, error) {
	var t PlanningThread
	err := row.Scan(&t.ID, &t.CampaignID, &t.Title, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

// CreatePlanningThread inserts a thread for the campaign and returns it. title
// may be empty — the first exchange auto-fills it.
func (s *Store) CreatePlanningThread(ctx context.Context, campaignID uuid.UUID, title string) (PlanningThread, error) {
	row := s.db.QueryRow(ctx,
		`INSERT INTO planning_thread (campaign_id, title)
		 VALUES ($1, $2)
		 RETURNING `+planningThreadColumns,
		campaignID, title)
	t, err := scanPlanningThread(row)
	if err != nil {
		return PlanningThread{}, fmt.Errorf("storage: create planning thread: %w", err)
	}
	return t, nil
}

// GetPlanningThread loads one thread scoped to its campaign; a thread in
// another campaign yields ErrNotFound, never a permission error.
func (s *Store) GetPlanningThread(ctx context.Context, campaignID, id uuid.UUID) (PlanningThread, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+planningThreadColumns+`
		   FROM planning_thread
		  WHERE id = $1 AND campaign_id = $2`, id, campaignID)
	t, err := scanPlanningThread(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return PlanningThread{}, ErrNotFound
	}
	if err != nil {
		return PlanningThread{}, fmt.Errorf("storage: get planning thread %s: %w", id, err)
	}
	return t, nil
}

// ListPlanningThreads returns the campaign's threads, most recently touched
// first (an exchange bumps updated_at), id tie-broken for a stable order.
func (s *Store) ListPlanningThreads(ctx context.Context, campaignID uuid.UUID) ([]PlanningThread, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+planningThreadColumns+`
		   FROM planning_thread
		  WHERE campaign_id = $1
		  ORDER BY updated_at DESC, id`, campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: list planning threads for %s: %w", campaignID, err)
	}
	defer rows.Close()

	var out []PlanningThread
	for rows.Next() {
		t, err := scanPlanningThread(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan planning thread: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list planning threads for %s: %w", campaignID, err)
	}
	return out, nil
}

// RenamePlanningThread sets a thread's title and returns the updated row. A
// thread outside the campaign yields ErrNotFound.
func (s *Store) RenamePlanningThread(ctx context.Context, campaignID, id uuid.UUID, title string) (PlanningThread, error) {
	row := s.db.QueryRow(ctx,
		`UPDATE planning_thread SET title = $3, updated_at = now()
		  WHERE id = $1 AND campaign_id = $2
		 RETURNING `+planningThreadColumns,
		id, campaignID, title)
	t, err := scanPlanningThread(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return PlanningThread{}, ErrNotFound
	}
	if err != nil {
		return PlanningThread{}, fmt.Errorf("storage: rename planning thread %s: %w", id, err)
	}
	return t, nil
}

// DeletePlanningThread removes a thread and (via the composite FK cascade) its
// messages. A thread outside the campaign yields ErrNotFound.
func (s *Store) DeletePlanningThread(ctx context.Context, campaignID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM planning_thread WHERE id = $1 AND campaign_id = $2`, id, campaignID)
	if err != nil {
		return fmt.Errorf("storage: delete planning thread %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const planningMessageColumns = `
	id, thread_id, campaign_id, seq, role, content, created_at`

func scanPlanningMessage(row pgx.Row) (PlanningMessage, error) {
	var m PlanningMessage
	err := row.Scan(&m.ID, &m.ThreadID, &m.CampaignID, &m.Seq, &m.Role, &m.Content, &m.CreatedAt)
	return m, err
}

// ListPlanningMessages returns a thread's messages in seq order. The thread's
// existence is not checked here — an unknown thread yields an empty slice; the
// caller resolves the thread first (GetPlanningThread) when it matters.
func (s *Store) ListPlanningMessages(ctx context.Context, campaignID, threadID uuid.UUID) ([]PlanningMessage, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+planningMessageColumns+`
		   FROM planning_message
		  WHERE thread_id = $1 AND campaign_id = $2
		  ORDER BY seq`, threadID, campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: list planning messages for %s: %w", threadID, err)
	}
	defer rows.Close()

	var out []PlanningMessage
	for rows.Next() {
		m, err := scanPlanningMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan planning message: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list planning messages for %s: %w", threadID, err)
	}
	return out, nil
}

// appendMessageAttempts bounds the seq-race retry in AppendPlanningMessage. The
// chat surface is GM-only and effectively single-writer per thread, so a
// collision is a rarity, not a steady state; two retries cover a concurrent
// pair without hiding a systematic bug behind an unbounded loop.
const appendMessageAttempts = 3

// AppendPlanningMessage appends one prose turn at the thread's next seq and
// bumps the thread's updated_at (the thread list orders by it). The seq is
// computed as max+1 in the INSERT; the UNIQUE (thread_id, seq) key turns a
// concurrent append into a retry rather than a duplicate. An unknown thread
// (or one outside the campaign) yields ErrNotFound via the composite FK.
func (s *Store) AppendPlanningMessage(ctx context.Context, campaignID, threadID uuid.UUID, role, content string) (PlanningMessage, error) {
	var lastErr error
	for attempt := 0; attempt < appendMessageAttempts; attempt++ {
		var m PlanningMessage
		err := s.InTx(ctx, func(tx *Store) error {
			row := tx.db.QueryRow(ctx,
				`INSERT INTO planning_message (thread_id, campaign_id, seq, role, content)
				 VALUES ($1, $2,
				         (SELECT COALESCE(MAX(seq), 0) + 1 FROM planning_message
				           WHERE thread_id = $1 AND campaign_id = $2),
				         $3, $4)
				 RETURNING `+planningMessageColumns,
				threadID, campaignID, role, content)
			var err error
			if m, err = scanPlanningMessage(row); err != nil {
				return err
			}
			_, err = tx.db.Exec(ctx,
				`UPDATE planning_thread SET updated_at = now()
				  WHERE id = $1 AND campaign_id = $2`, threadID, campaignID)
			return err
		})
		switch {
		case err == nil:
			return m, nil
		case isPGError(err, pgForeignKeyViolation):
			return PlanningMessage{}, ErrNotFound
		case isPGError(err, pgUniqueViolation):
			lastErr = err // a concurrent append took our seq; retry
			continue
		default:
			return PlanningMessage{}, fmt.Errorf("storage: append planning message: %w", err)
		}
	}
	return PlanningMessage{}, fmt.Errorf("storage: append planning message: seq contention persisted: %w", lastErr)
}

// isPGError reports whether err carries the given SQLSTATE code.
func isPGError(err error, code string) bool {
	got, ok := pgErrCode(err)
	return ok && got == code
}
