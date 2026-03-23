package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/glyphoxa/pkg/memory"
)

// SessionStoreImpl is the L1 memory layer backed by a PostgreSQL
// session_entries table with a GIN full-text search index.
//
// Obtain one via [Store.L1] or [NewSessionStore] rather than constructing
// directly. All methods are safe for concurrent use.
type SessionStoreImpl struct {
	pool       *pgxpool.Pool
	schema     SchemaName
	campaignID string
}

// NewSessionStore creates a read-only SessionStoreImpl that shares an existing
// [pgxpool.Pool]. This is intended for components that only need L1 transcript
// access (e.g. the gateway recap command) without standing up the full
// three-layer [Store].
func NewSessionStore(pool *pgxpool.Pool, schema SchemaName, campaignID string) *SessionStoreImpl {
	return &SessionStoreImpl{pool: pool, schema: schema, campaignID: campaignID}
}

// WriteEntry implements [memory.SessionStore]. It appends entry to the
// session_entries table under sessionID.
func (s *SessionStoreImpl) WriteEntry(ctx context.Context, sessionID string, entry memory.TranscriptEntry) error {
	q := fmt.Sprintf(`
		INSERT INTO %s
		    (campaign_id, session_id, speaker_id, speaker_name, text, raw_text, npc_id, timestamp, duration_ns)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		s.schema.TableRef("session_entries"))

	_, err := s.pool.Exec(ctx, q,
		s.campaignID,
		sessionID,
		entry.SpeakerID,
		entry.SpeakerName,
		entry.Text,
		entry.RawText,
		entry.NPCID,
		entry.Timestamp,
		entry.Duration.Nanoseconds(),
	)
	if err != nil {
		return fmt.Errorf("session store: write entry: %w", err)
	}
	return nil
}

// GetRecent implements [memory.SessionStore]. It returns all entries for
// sessionID whose timestamp is no earlier than time.Now()-duration, ordered
// chronologically (oldest first).
func (s *SessionStoreImpl) GetRecent(ctx context.Context, sessionID string, duration time.Duration) ([]memory.TranscriptEntry, error) {
	q := fmt.Sprintf(`
		SELECT speaker_id, speaker_name, text, raw_text, npc_id, timestamp, duration_ns
		FROM   %s
		WHERE  campaign_id = $1
		  AND  session_id = $2
		  AND  timestamp  >= now() - ($3::bigint * interval '1 microsecond')
		ORDER  BY timestamp`,
		s.schema.TableRef("session_entries"))

	rows, err := s.pool.Query(ctx, q, s.campaignID, sessionID, duration.Microseconds())
	if err != nil {
		return nil, fmt.Errorf("session store: get recent: %w", err)
	}
	return collectEntries(rows)
}

// Search implements [memory.SessionStore]. It performs a PostgreSQL full-text
// search over the text column and applies optional filters from opts.
//
// The query is passed to plainto_tsquery so no special operator syntax is required.
func (s *SessionStoreImpl) Search(ctx context.Context, query string, opts memory.SearchOpts) ([]memory.TranscriptEntry, error) {
	args := []any{s.campaignID, query} // $1 = campaign_id, $2 = FTS query string
	next := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	conditions := []string{
		"campaign_id = $1",
		"to_tsvector('english', text) @@ plainto_tsquery('english', $2)",
	}
	if opts.SessionID != "" {
		conditions = append(conditions, "session_id = "+next(opts.SessionID))
	}
	if !opts.After.IsZero() {
		conditions = append(conditions, "timestamp > "+next(opts.After))
	}
	if !opts.Before.IsZero() {
		conditions = append(conditions, "timestamp < "+next(opts.Before))
	}
	if opts.SpeakerID != "" {
		conditions = append(conditions, "speaker_id = "+next(opts.SpeakerID))
	}

	q := fmt.Sprintf("SELECT speaker_id, speaker_name, text, raw_text, npc_id, timestamp, duration_ns\n"+
		"FROM   %s\n"+
		"WHERE  %s\n"+
		"ORDER  BY timestamp",
		s.schema.TableRef("session_entries"),
		strings.Join(conditions, "\n  AND  "))

	if opts.Limit > 0 {
		args = append(args, opts.Limit)
		q += fmt.Sprintf("\nLIMIT $%d", len(args))
	}

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("session store: search: %w", err)
	}
	return collectEntries(rows)
}

// EntryCount implements [memory.SessionStore]. It returns the total number of
// transcript entries for sessionID.
func (s *SessionStoreImpl) EntryCount(ctx context.Context, sessionID string) (int, error) {
	q := fmt.Sprintf(`SELECT count(*) FROM %s WHERE campaign_id = $1 AND session_id = $2`,
		s.schema.TableRef("session_entries"))

	var count int
	if err := s.pool.QueryRow(ctx, q, s.campaignID, sessionID).Scan(&count); err != nil {
		return 0, fmt.Errorf("session store: entry count: %w", err)
	}
	return count, nil
}

// ListSessions implements [memory.SessionStore]. It returns sessions for the
// store's campaign, ordered by started_at descending (newest first).
func (s *SessionStoreImpl) ListSessions(ctx context.Context, limit int) ([]memory.SessionInfo, error) {
	if limit <= 0 {
		limit = 50
	}
	q := fmt.Sprintf(`
		SELECT session_id, campaign_id, started_at, COALESCE(ended_at, '0001-01-01T00:00:00Z')
		FROM   %s
		WHERE  campaign_id = $1
		ORDER  BY started_at DESC
		LIMIT  $2`,
		s.schema.TableRef("sessions"))

	rows, err := s.pool.Query(ctx, q, s.campaignID, limit)
	if err != nil {
		return nil, fmt.Errorf("session store: list sessions: %w", err)
	}
	sessions, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (memory.SessionInfo, error) {
		var si memory.SessionInfo
		if err := row.Scan(&si.SessionID, &si.CampaignID, &si.StartedAt, &si.EndedAt); err != nil {
			return memory.SessionInfo{}, err
		}
		return si, nil
	})
	if err != nil {
		return nil, fmt.Errorf("session store: list sessions scan: %w", err)
	}
	if sessions == nil {
		sessions = []memory.SessionInfo{}
	}
	return sessions, nil
}

// StartSession records a new session in the sessions metadata table.
func (s *SessionStoreImpl) StartSession(ctx context.Context, sessionID string) error {
	q := fmt.Sprintf(`
		INSERT INTO %s (session_id, campaign_id, started_at)
		VALUES ($1, $2, now())
		ON CONFLICT (session_id) DO NOTHING`,
		s.schema.TableRef("sessions"))

	_, err := s.pool.Exec(ctx, q, sessionID, s.campaignID)
	if err != nil {
		return fmt.Errorf("session store: start session: %w", err)
	}
	return nil
}

// EndSession sets the ended_at timestamp for a session.
func (s *SessionStoreImpl) EndSession(ctx context.Context, sessionID string) error {
	q := fmt.Sprintf(`
		UPDATE %s SET ended_at = now()
		WHERE  session_id = $1 AND campaign_id = $2`,
		s.schema.TableRef("sessions"))

	_, err := s.pool.Exec(ctx, q, sessionID, s.campaignID)
	if err != nil {
		return fmt.Errorf("session store: end session: %w", err)
	}
	return nil
}

// collectEntries scans pgx rows into a slice of TranscriptEntry values.
func collectEntries(rows pgx.Rows) ([]memory.TranscriptEntry, error) {
	entries, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (memory.TranscriptEntry, error) {
		var (
			e          memory.TranscriptEntry
			durationNS int64
		)
		if err := row.Scan(
			&e.SpeakerID,
			&e.SpeakerName,
			&e.Text,
			&e.RawText,
			&e.NPCID,
			&e.Timestamp,
			&durationNS,
		); err != nil {
			return memory.TranscriptEntry{}, err
		}
		e.Duration = time.Duration(durationNS)
		return e, nil
	})
	if err != nil {
		return nil, fmt.Errorf("session store: scan rows: %w", err)
	}
	if entries == nil {
		entries = []memory.TranscriptEntry{}
	}
	return entries, nil
}
