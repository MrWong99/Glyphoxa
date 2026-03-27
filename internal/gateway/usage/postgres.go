package usage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Compile-time interface assertion.
var _ Store = (*PostgresStore)(nil)

// PostgresStore is a PostgreSQL-backed usage store. It uses the usage_records
// table created by migration 000002_usage_records.
//
// All methods are safe for concurrent use (backed by pgxpool).
type PostgresStore struct {
	pool *pgxpool.Pool
}

// NewPostgresStore creates a PostgreSQL-backed usage store.
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// RecordUsage atomically increments usage counters using UPSERT.
func (s *PostgresStore) RecordUsage(ctx context.Context, tenantID string, delta Record) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO usage_records (tenant_id, period, session_hours, llm_tokens, stt_seconds, tts_chars)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, period) DO UPDATE SET
			session_hours = usage_records.session_hours + EXCLUDED.session_hours,
			llm_tokens    = usage_records.llm_tokens    + EXCLUDED.llm_tokens,
			stt_seconds   = usage_records.stt_seconds   + EXCLUDED.stt_seconds,
			tts_chars     = usage_records.tts_chars      + EXCLUDED.tts_chars
	`, tenantID, delta.Period, delta.SessionHours, delta.LLMTokens, delta.STTSeconds, delta.TTSChars)
	if err != nil {
		return fmt.Errorf("usage: record usage for %q: %w", tenantID, err)
	}
	return nil
}

// GetUsage returns the usage record for a tenant in the given period.
func (s *PostgresStore) GetUsage(ctx context.Context, tenantID string, period time.Time) (Record, error) {
	var r Record
	r.TenantID = tenantID
	r.Period = period

	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(session_hours, 0), COALESCE(llm_tokens, 0),
		       COALESCE(stt_seconds, 0), COALESCE(tts_chars, 0)
		FROM usage_records
		WHERE tenant_id = $1 AND period = $2
	`, tenantID, period).Scan(&r.SessionHours, &r.LLMTokens, &r.STTSeconds, &r.TTSChars)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Record{TenantID: tenantID, Period: period}, nil
		}
		return Record{}, fmt.Errorf("usage: get usage for %q: %w", tenantID, err)
	}
	return r, nil
}

// CheckQuota checks whether the tenant can start a new session.
// Uses SELECT FOR UPDATE inside a transaction to serialize concurrent
// quota checks for the same tenant, preventing TOCTOU races where two
// sessions both pass the check before either records usage.
func (s *PostgresStore) CheckQuota(ctx context.Context, tenantID string, quota QuotaConfig) error {
	if quota.MonthlySessionHours <= 0 {
		return nil // unlimited
	}

	period := CurrentPeriod()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("usage: begin quota check: %w", err)
	}
	defer tx.Rollback(ctx)

	// Ensure a row exists for this tenant+period so FOR UPDATE has
	// something to lock.
	if _, err := tx.Exec(ctx, `
		INSERT INTO usage_records (tenant_id, period, session_hours, llm_tokens, stt_seconds, tts_chars)
		VALUES ($1, $2, 0, 0, 0, 0)
		ON CONFLICT (tenant_id, period) DO NOTHING
	`, tenantID, period); err != nil {
		return fmt.Errorf("usage: ensure usage row: %w", err)
	}

	// Lock the row to serialize concurrent quota checks for this tenant.
	var hours float64
	err = tx.QueryRow(ctx, `
		SELECT session_hours FROM usage_records
		WHERE tenant_id = $1 AND period = $2
		FOR UPDATE
	`, tenantID, period).Scan(&hours)
	if err != nil {
		return fmt.Errorf("usage: check quota: %w", err)
	}

	if hours >= quota.MonthlySessionHours {
		return ErrQuotaExceeded
	}

	return tx.Commit(ctx)
}
