// Package sessionorch provides session lifecycle orchestration for multi-tenant
// Glyphoxa deployments. It tracks session state, enforces license constraints
// (concurrent session limits per tier), and provides zombie session cleanup.
//
// Two implementations exist:
//   - [PostgresOrchestrator] for distributed mode (--mode=gateway)
//   - [MemoryOrchestrator] for single-process mode (--mode=full)
package sessionorch

import (
	"context"
	"time"

	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/internal/gateway"
)

// SessionRequest contains the parameters for creating a new session.
type SessionRequest struct {
	TenantID    string
	CampaignID  string
	GuildID     string
	ChannelID   string
	LicenseTier config.LicenseTier
}

// Session represents a persisted session record.
type Session struct {
	ID            string
	TenantID      string
	CampaignID    string
	GuildID       string
	ChannelID     string
	LicenseTier   config.LicenseTier
	State         gateway.SessionState
	Error         string
	WorkerPod     string
	StartedAt     time.Time
	EndedAt       *time.Time
	LastHeartbeat *time.Time
}

// Orchestrator manages session lifecycle and constraint enforcement.
// Implementations must be safe for concurrent use.
type Orchestrator interface {
	// ValidateAndCreate atomically validates license constraints and creates
	// a new session in the pending state. Returns the session ID.
	// Returns an error if constraints are violated (e.g., too many active sessions).
	ValidateAndCreate(ctx context.Context, req SessionRequest) (string, error)

	// Transition moves a session to the given state. The error parameter is
	// recorded when transitioning to SessionEnded.
	Transition(ctx context.Context, sessionID string, state gateway.SessionState, errMsg string) error

	// RecordHeartbeat updates the last_heartbeat timestamp for a session.
	RecordHeartbeat(ctx context.Context, sessionID string) error

	// ActiveSessions returns all non-ended sessions for a tenant.
	ActiveSessions(ctx context.Context, tenantID string) ([]Session, error)

	// GetSession returns a single session by ID.
	GetSession(ctx context.Context, sessionID string) (Session, error)

	// CleanupZombies transitions sessions with stale heartbeats to ended.
	// Called periodically by the gateway.
	CleanupZombies(ctx context.Context, timeout time.Duration) (int, error)
}
