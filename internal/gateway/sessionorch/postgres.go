package sessionorch

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/internal/dbutil"
	"github.com/MrWong99/glyphoxa/internal/gateway"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Compile-time interface assertion.
var _ Orchestrator = (*PostgresOrchestrator)(nil)

// PostgresOrchestrator manages session state in PostgreSQL.
// It uses database constraints for atomic enforcement of license limits.
//
// All methods are safe for concurrent use (backed by pgxpool).
type PostgresOrchestrator struct {
	pool *pgxpool.Pool
}

// NewPostgresOrchestrator creates a PostgreSQL-backed session orchestrator.
// It runs migrations on the gateway database to ensure the sessions table exists.
func NewPostgresOrchestrator(ctx context.Context, pool *pgxpool.Pool) (*PostgresOrchestrator, error) {
	if err := dbutil.RunMigrations(ctx, pool, migrationFiles, migrationsFS, "sessionorch"); err != nil {
		return nil, fmt.Errorf("sessionorch: run migrations: %w", err)
	}
	return &PostgresOrchestrator{pool: pool}, nil
}

// migrationFiles lists the up-migration files in order.
var migrationFiles = []string{
	"migrations/000001_sessions.up.sql",
	"migrations/000002_usage_records.up.sql",
	"migrations/000003_unique_active_guild.up.sql",
}

// ValidateAndCreate atomically creates a session, relying on database
// constraints to enforce license limits. Uses INSERT with ON CONFLICT
// detection.
func (p *PostgresOrchestrator) ValidateAndCreate(ctx context.Context, req SessionRequest) (string, error) {
	id := uuid.NewString()

	_, err := p.pool.Exec(ctx, `
		INSERT INTO sessions (id, tenant_id, campaign_id, guild_id, channel_id, license_tier, state, started_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'pending', now())
	`, id, req.TenantID, req.CampaignID, req.GuildID, req.ChannelID, req.LicenseTier.String())

	if err != nil {
		return "", fmt.Errorf("sessionorch: create session: %w", err)
	}

	return id, nil
}

// Transition moves a session to the given state.
// Invalid transitions (e.g., ended→active) are rejected. Transitions from
// ended are silently ignored to make idempotent stop calls safe.
func (p *PostgresOrchestrator) Transition(ctx context.Context, sessionID string, state gateway.SessionState, errMsg string) error {
	if state == gateway.SessionEnded {
		// Use WHERE state != 'ended' to prevent re-opening ended sessions
		// and to make idempotent stop calls safe (no error on double-end).
		tag, err := p.pool.Exec(ctx, `
			UPDATE sessions SET state = 'ended', error = $2, ended_at = now()
			WHERE id = $1 AND state != 'ended'
		`, sessionID, errMsg)
		if err != nil {
			return fmt.Errorf("sessionorch: transition session %q to ended: %w", sessionID, err)
		}
		_ = tag
		return nil
	}

	// For non-ended transitions, validate the transition is allowed.
	tag, err := p.pool.Exec(ctx, `
		UPDATE sessions SET state = $2, last_heartbeat = CASE WHEN $2 = 'active' THEN now() ELSE last_heartbeat END
		WHERE id = $1 AND state != 'ended'
	`, sessionID, state.String())
	if err != nil {
		return fmt.Errorf("sessionorch: transition session %q to %s: %w", sessionID, state, err)
	}
	_ = tag
	return nil
}

// RecordHeartbeat updates the last_heartbeat timestamp.
func (p *PostgresOrchestrator) RecordHeartbeat(ctx context.Context, sessionID string) error {
	_, err := p.pool.Exec(ctx, `
		UPDATE sessions SET last_heartbeat = now()
		WHERE id = $1 AND state != 'ended'
	`, sessionID)
	if err != nil {
		return fmt.Errorf("sessionorch: record heartbeat for %q: %w", sessionID, err)
	}
	return nil
}

// ActiveSessions returns all non-ended sessions for a tenant.
func (p *PostgresOrchestrator) ActiveSessions(ctx context.Context, tenantID string) ([]Session, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, tenant_id, campaign_id, guild_id, channel_id, license_tier,
		       state, error, worker_pod, started_at, ended_at, last_heartbeat
		FROM sessions
		WHERE tenant_id = $1 AND state != 'ended'
		ORDER BY started_at DESC
	`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("sessionorch: query active sessions: %w", err)
	}
	defer rows.Close()

	return scanSessions(rows)
}

// GetSession returns a single session by ID.
func (p *PostgresOrchestrator) GetSession(ctx context.Context, sessionID string) (Session, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, tenant_id, campaign_id, guild_id, channel_id, license_tier,
		       state, error, worker_pod, started_at, ended_at, last_heartbeat
		FROM sessions
		WHERE id = $1
	`, sessionID)
	if err != nil {
		return Session{}, fmt.Errorf("sessionorch: query session %q: %w", sessionID, err)
	}
	defer rows.Close()

	sessions, err := scanSessions(rows)
	if err != nil {
		return Session{}, err
	}
	if len(sessions) == 0 {
		return Session{}, fmt.Errorf("sessionorch: session %q not found", sessionID)
	}
	return sessions[0], nil
}

// CleanupZombies transitions sessions with stale heartbeats to ended.
// Also catches active sessions with NULL heartbeat (worker died before first
// heartbeat tick) that are older than the timeout.
// Returns the IDs of cleaned-up sessions.
func (p *PostgresOrchestrator) CleanupZombies(ctx context.Context, timeout time.Duration) ([]string, error) {
	rows, err := p.pool.Query(ctx, `
		UPDATE sessions
		SET state = 'ended', error = 'heartbeat timeout', ended_at = now()
		WHERE state != 'ended'
		  AND (
		    (last_heartbeat IS NOT NULL AND last_heartbeat < now() - $1::interval)
		    OR (last_heartbeat IS NULL AND state != 'pending' AND started_at < now() - $1::interval)
		  )
		RETURNING id
	`, timeout.String())
	if err != nil {
		return nil, fmt.Errorf("sessionorch: cleanup zombies: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("sessionorch: scan zombie id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sessionorch: cleanup zombies rows: %w", err)
	}

	if len(ids) > 0 {
		slog.Warn("sessionorch: cleaned up zombie sessions", "count", len(ids), "ids", ids)
	}
	return ids, nil
}

// CleanupStalePending transitions sessions stuck in 'pending' state
// older than maxAge to ended.
// Returns the IDs of cleaned-up sessions.
func (p *PostgresOrchestrator) CleanupStalePending(ctx context.Context, maxAge time.Duration) ([]string, error) {
	rows, err := p.pool.Query(ctx, `
		UPDATE sessions
		SET state = 'ended', error = 'stale pending: dispatch timeout', ended_at = now()
		WHERE state = 'pending'
		  AND started_at < now() - $1::interval
		RETURNING id
	`, maxAge.String())
	if err != nil {
		return nil, fmt.Errorf("sessionorch: cleanup stale pending: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("sessionorch: scan stale pending id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sessionorch: cleanup stale pending rows: %w", err)
	}

	if len(ids) > 0 {
		slog.Warn("sessionorch: cleaned up stale pending sessions", "count", len(ids), "ids", ids)
	}
	return ids, nil
}

// scanSessions reads session rows into a slice.
func scanSessions(rows pgx.Rows) ([]Session, error) {
	var sessions []Session
	for rows.Next() {
		var s Session
		var tierStr, stateStr string
		var errMsg, workerPod *string

		err := rows.Scan(
			&s.ID, &s.TenantID, &s.CampaignID, &s.GuildID, &s.ChannelID,
			&tierStr, &stateStr, &errMsg, &workerPod,
			&s.StartedAt, &s.EndedAt, &s.LastHeartbeat,
		)
		if err != nil {
			return nil, fmt.Errorf("sessionorch: scan session: %w", err)
		}

		tier, err := config.ParseLicenseTier(tierStr)
		if err != nil {
			return nil, fmt.Errorf("sessionorch: parse tier %q: %w", tierStr, err)
		}
		s.LicenseTier = tier

		state, ok := gateway.ParseSessionState(stateStr)
		if !ok {
			return nil, fmt.Errorf("sessionorch: unknown state %q", stateStr)
		}
		s.State = state

		if errMsg != nil {
			s.Error = *errMsg
		}
		if workerPod != nil {
			s.WorkerPod = *workerPod
		}

		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// AllNonEndedSessions returns all non-ended sessions across all tenants.
func (p *PostgresOrchestrator) AllNonEndedSessions(ctx context.Context) ([]Session, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT id, tenant_id, campaign_id, guild_id, channel_id, license_tier,
		       state, error, worker_pod, started_at, ended_at, last_heartbeat
		FROM sessions
		WHERE state != 'ended'
		ORDER BY started_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("sessionorch: query all non-ended sessions: %w", err)
	}
	defer rows.Close()
	var sessions []Session
	for rows.Next() {
		var s Session
		var tierStr, stateStr string
		if err := rows.Scan(
			&s.ID, &s.TenantID, &s.CampaignID, &s.GuildID, &s.ChannelID,
			&tierStr, &stateStr, &s.Error, &s.WorkerPod,
			&s.StartedAt, &s.EndedAt, &s.LastHeartbeat,
		); err != nil {
			return nil, fmt.Errorf("sessionorch: scan session: %w", err)
		}
		tier, tierErr := config.ParseLicenseTier(tierStr)
		if tierErr != nil {
			return nil, fmt.Errorf("sessionorch: parse tier %q for session %s: %w", tierStr, s.ID, tierErr)
		}
		s.LicenseTier = tier

		state, stateOK := gateway.ParseSessionState(stateStr)
		if !stateOK {
			return nil, fmt.Errorf("sessionorch: unknown state %q for session %s", stateStr, s.ID)
		}
		s.State = state
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}
