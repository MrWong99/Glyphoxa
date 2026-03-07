package gateway

import (
	"context"
	"time"
)

// SessionState represents the lifecycle state of a session.
type SessionState int

const (
	// SessionPending means the session has been validated and a worker is being provisioned.
	SessionPending SessionState = iota

	// SessionActive means the voice pipeline is running.
	SessionActive

	// SessionEnded means the session is complete or has failed.
	SessionEnded
)

// String returns the string representation of the session state.
func (s SessionState) String() string {
	switch s {
	case SessionPending:
		return "pending"
	case SessionActive:
		return "active"
	case SessionEnded:
		return "ended"
	default:
		return "unknown"
	}
}

// ParseSessionState converts a string to a SessionState.
func ParseSessionState(s string) (SessionState, bool) {
	switch s {
	case "pending":
		return SessionPending, true
	case "active":
		return SessionActive, true
	case "ended":
		return SessionEnded, true
	default:
		return 0, false
	}
}

// StartSessionRequest contains the parameters needed to start a session on a worker.
type StartSessionRequest struct {
	SessionID   string
	TenantID    string
	CampaignID  string
	GuildID     string
	ChannelID   string
	LicenseTier string
}

// SessionStatus describes the current state of a session.
type SessionStatus struct {
	SessionID string
	State     SessionState
	StartedAt time.Time
	Error     string
}

// WorkerClient is the interface for gateway-to-worker communication.
// The gateway uses this to start/stop sessions and query worker status.
//
// Two implementations exist:
//   - grpc.Client wraps a gRPC client connection (distributed mode)
//   - local.Client calls session functions directly (full mode)
type WorkerClient interface {
	StartSession(ctx context.Context, req StartSessionRequest) error
	StopSession(ctx context.Context, sessionID string) error
	GetStatus(ctx context.Context) ([]SessionStatus, error)
}

// GatewayCallback is the interface for worker-to-gateway communication.
// Workers use this to report state changes and send heartbeats.
//
// Two implementations exist:
//   - grpc.GatewayServer implements a gRPC server (distributed mode)
//   - local.GatewayCallback calls orchestrator functions directly (full mode)
type GatewayCallback interface {
	ReportState(ctx context.Context, sessionID string, state SessionState) error
	Heartbeat(ctx context.Context, sessionID string) error
}
