package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Background job persistence (#286, ADR-0049): one generic `job` table backs a
// minimal DB-backed runner. A job is a row; Postgres is the coordinator, so the
// claim is safe across web/all replicas by construction — the same spirit as the
// voice_sessions claim (ADR-0039) and the "rows are the source of truth, sweep on
// boot" posture (ADR-0043). Semantics are at-least-once: a crashed worker's lease
// expires and the job is re-claimed, so handlers must be idempotent.

// JobStatus is the lifecycle state of a job row (CHECK-constrained in the schema).
type JobStatus string

const (
	// JobPending: not yet claimed; runnable once run_after <= now().
	JobPending JobStatus = "pending"
	// JobRunning: claimed by a worker, leased_until in the future.
	JobRunning JobStatus = "running"
	// JobDone: handler succeeded; terminal.
	JobDone JobStatus = "done"
	// JobDead: exhausted its attempts (or swept); terminal, last_error kept.
	JobDead JobStatus = "dead"
)

// DefaultJobMaxAttempts is the retry budget applied when a job is enqueued with a
// non-positive maxAttempts. After this many attempts a failing job goes to
// JobDead rather than retrying forever (ADR-0049 dead-letter policy).
const DefaultJobMaxAttempts = 5

// Job is one row of the background job queue (#286, ADR-0049).
type Job struct {
	ID          uuid.UUID
	Kind        string
	Payload     []byte // jsonb; the handler-scoped payload (carries its own scope, no tenant_id column)
	Status      JobStatus
	Attempts    int
	MaxAttempts int
	RunAfter    time.Time
	LeasedUntil *time.Time
	LastError   *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const jobColumns = `
	id, kind, payload, status, attempts, max_attempts,
	run_after, leased_until, last_error, created_at, updated_at`

func scanJob(row pgx.Row) (Job, error) {
	var j Job
	err := row.Scan(
		&j.ID, &j.Kind, &j.Payload, &j.Status, &j.Attempts, &j.MaxAttempts,
		&j.RunAfter, &j.LeasedUntil, &j.LastError, &j.CreatedAt, &j.UpdatedAt,
	)
	return j, err
}

// EnqueueJob inserts a pending job of the given kind carrying payload (jsonb),
// returning its id. A non-positive maxAttempts takes DefaultJobMaxAttempts. The
// job is immediately runnable (run_after defaults to now()).
func (s *Store) EnqueueJob(ctx context.Context, kind string, payload []byte, maxAttempts int) (uuid.UUID, error) {
	if maxAttempts <= 0 {
		maxAttempts = DefaultJobMaxAttempts
	}
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	var id uuid.UUID
	err := s.db.QueryRow(ctx,
		`INSERT INTO job (kind, payload, max_attempts)
		 VALUES ($1, $2, $3)
		 RETURNING id`,
		kind, payload, maxAttempts).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("storage: enqueue job %q: %w", kind, err)
	}
	return id, nil
}

// ClaimJob atomically claims the single oldest runnable job in any of kinds and
// returns it in its post-claim state (status='running', attempts incremented,
// leased_until = now()+lease). Runnable means a pending job whose run_after has
// arrived, OR a running job whose lease has expired and still has attempts left
// (a crashed worker's abandoned lease). No runnable row yields ErrNotFound.
//
// The claim is ONE atomic UPDATE whose target row is chosen by a `SELECT … FOR
// UPDATE SKIP LOCKED LIMIT 1` subquery: concurrent workers skip each other's
// locked candidates rather than block, so two workers claim two distinct jobs.
// The SKIP LOCKED sits on the SELECT subquery, never on the UPDATE.
func (s *Store) ClaimJob(ctx context.Context, kinds []string, lease time.Duration) (Job, error) {
	row := s.db.QueryRow(ctx,
		`UPDATE job
		    SET status = 'running',
		        attempts = attempts + 1,
		        leased_until = now() + make_interval(secs => $2),
		        updated_at = now()
		  WHERE id = (
		        SELECT id FROM job
		         WHERE kind = ANY($1)
		           AND (
		                 (status = 'pending' AND run_after <= now())
		              OR (status = 'running' AND leased_until < now() AND attempts < max_attempts)
		           )
		         ORDER BY run_after, created_at, id
		         FOR UPDATE SKIP LOCKED
		         LIMIT 1
		  )
		 RETURNING `+jobColumns,
		kinds, lease.Seconds())
	j, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("storage: claim job: %w", err)
	}
	return j, nil
}

// GetJob loads one job by id, or ErrNotFound. It backs dead-letter inspection and
// tests; the runner itself never reads a job back after acting on it.
func (s *Store) GetJob(ctx context.Context, id uuid.UUID) (Job, error) {
	row := s.db.QueryRow(ctx, `SELECT `+jobColumns+` FROM job WHERE id = $1`, id)
	j, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("storage: get job %s: %w", id, err)
	}
	return j, nil
}

// CompleteJob marks a job done and clears its lease. A missing id yields ErrNotFound.
func (s *Store) CompleteJob(ctx context.Context, id uuid.UUID) error {
	return s.updateJobStatus(ctx,
		`UPDATE job SET status = 'done', leased_until = NULL, updated_at = now() WHERE id = $1`,
		"complete", id)
}

// RetryJob returns a job to pending with a recorded last_error and a future
// run_after (the backoff delay), clearing its lease so it is re-claimable once
// run_after arrives. A missing id yields ErrNotFound.
func (s *Store) RetryJob(ctx context.Context, id uuid.UUID, lastError string, runAfter time.Time) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE job
		    SET status = 'pending', last_error = $2, run_after = $3,
		        leased_until = NULL, updated_at = now()
		  WHERE id = $1`,
		id, lastError, runAfter)
	if err != nil {
		return fmt.Errorf("storage: retry job %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkJobDead moves a job to the dead-letter state with a recorded last_error and
// a cleared lease — visible, never silently retried again. A missing id yields
// ErrNotFound.
func (s *Store) MarkJobDead(ctx context.Context, id uuid.UUID, lastError string) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE job
		    SET status = 'dead', last_error = $2, leased_until = NULL, updated_at = now()
		  WHERE id = $1`,
		id, lastError)
	if err != nil {
		return fmt.Errorf("storage: mark job dead %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) updateJobStatus(ctx context.Context, sql, verb string, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, sql, id)
	if err != nil {
		return fmt.Errorf("storage: %s job %s: %w", verb, id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SweepExpiredJobs dead-letters every running job in kinds whose lease has expired
// AND whose attempts have hit max_attempts — the leftover a crash leaves that
// ClaimJob's runnable guard (attempts < max_attempts) will never re-claim. It
// stamps a last_error where none was recorded. Returns how many rows were swept.
// Called once per runner poll before the claim loop.
func (s *Store) SweepExpiredJobs(ctx context.Context, kinds []string) (int, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE job
		    SET status = 'dead',
		        last_error = COALESCE(last_error, 'lease expired after max attempts'),
		        leased_until = NULL,
		        updated_at = now()
		  WHERE kind = ANY($1)
		    AND status = 'running'
		    AND leased_until < now()
		    AND attempts >= max_attempts`,
		kinds)
	if err != nil {
		return 0, fmt.Errorf("storage: sweep expired jobs: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// CountJobBacklog returns, per kind, the number of currently-runnable jobs — the
// same runnable predicate ClaimJob uses (pending-and-due, or running-with-an-
// expired-lease-and-attempts-left). It backs the backlog gauge (ADR-0032). A kind
// with no runnable jobs is absent from the map (the caller reads it as zero).
func (s *Store) CountJobBacklog(ctx context.Context, kinds []string) (map[string]int, error) {
	rows, err := s.db.Query(ctx,
		`SELECT kind, count(*) FROM job
		  WHERE kind = ANY($1)
		    AND (
		          (status = 'pending' AND run_after <= now())
		       OR (status = 'running' AND leased_until < now() AND attempts < max_attempts)
		    )
		  GROUP BY kind`,
		kinds)
	if err != nil {
		return nil, fmt.Errorf("storage: count job backlog: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int)
	for rows.Next() {
		var kind string
		var n int
		if err := rows.Scan(&kind, &n); err != nil {
			return nil, fmt.Errorf("storage: scan job backlog: %w", err)
		}
		out[kind] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: count job backlog: %w", err)
	}
	return out, nil
}
