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
	if err := runMigrations(ctx, pool); err != nil {
		return nil, fmt.Errorf("sessionorch: run migrations: %w", err)
	}
	return &PostgresOrchestrator{pool: pool}, nil
}

// runMigrations applies the embedded SQL migration files.
// For launch simplicity this executes the up migration directly;
// golang-migrate integration comes when per-schema version tracking is needed.
func runMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	upSQL, err := migrationsFS.ReadFile("migrations/000001_sessions.up.sql")
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}

	_, err = pool.Exec(ctx, string(upSQL))
	if err != nil {
		return fmt.Errorf("exec migration: %w", err)
	}

	slog.Info("sessionorch: migrations applied")
	return nil
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
func (p *PostgresOrchestrator) Transition(ctx context.Context, sessionID string, state gateway.SessionState, errMsg string) error {
	var tag pgx.Rows
	var err error

	if state == gateway.SessionEnded {
		tag, err = p.pool.Query(ctx, `
			UPDATE sessions SET state = $2, error = $3, ended_at = now()
			WHERE id = $1
		`, sessionID, state.String(), errMsg)
	} else {
		tag, err = p.pool.Query(ctx, `
			UPDATE sessions SET state = $2
			WHERE id = $1
		`, sessionID, state.String())
	}
	if tag != nil {
		tag.Close()
	}
	if err != nil {
		return fmt.Errorf("sessionorch: transition session %q to %s: %w", sessionID, state, err)
	}

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
func (p *PostgresOrchestrator) CleanupZombies(ctx context.Context, timeout time.Duration) (int, error) {
	tag, err := p.pool.Exec(ctx, `
		UPDATE sessions
		SET state = 'ended', error = 'heartbeat timeout', ended_at = now()
		WHERE state != 'ended'
		  AND last_heartbeat IS NOT NULL
		  AND last_heartbeat < now() - $1::interval
	`, timeout.String())
	if err != nil {
		return 0, fmt.Errorf("sessionorch: cleanup zombies: %w", err)
	}

	count := int(tag.RowsAffected())
	if count > 0 {
		slog.Warn("sessionorch: cleaned up zombie sessions", "count", count)
	}
	return count, nil
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
