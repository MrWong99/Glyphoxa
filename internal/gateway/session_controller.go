package gateway

import (
	"context"
	"time"
)

// SessionStartRequest contains the parameters a slash command handler provides
// to start a new voice session. This is the command-facing request struct,
// distinct from [StartSessionRequest] which is the gateway-to-worker contract.
type SessionStartRequest struct {
	// TenantID identifies the tenant owning this session.
	TenantID string

	// CampaignID identifies the campaign being played.
	CampaignID string

	// GuildID is the Discord guild (server) where the session runs.
	GuildID string

	// ChannelID is the voice channel to connect to.
	ChannelID string

	// UserID is the Discord user ID of the DM starting the session.
	UserID string
}

// SessionInfo holds metadata about a session for status queries.
// Returned by [SessionController.Info] to provide slash command handlers
// with display-ready session information.
type SessionInfo struct {
	// SessionID is the unique identifier for this session.
	SessionID string

	// GuildID is the Discord guild where the session is running.
	GuildID string

	// ChannelID is the voice channel the session is connected to.
	ChannelID string

	// CampaignName is the human-readable campaign name.
	CampaignName string

	// StartedAt is when the session was started.
	StartedAt time.Time

	// StartedBy is the Discord user ID of the DM who started the session.
	StartedBy string

	// State is the current lifecycle state of the session.
	State SessionState
}

// SessionController abstracts session lifecycle management for Discord slash
// command handlers. It decouples commands from the underlying mode so the same
// handler code works in both full mode and gateway mode.
//
// Full mode: implemented by a thin adapter over [app.SessionManager], which
// runs the voice pipeline in-process.
//
// Gateway mode: implemented by GatewaySessionController, which delegates to
// the session orchestrator and dispatches work to remote workers.
//
// All methods must be safe for concurrent use.
//
// Compile-time interface assertions (in their respective implementation files):
//
//	var _ SessionController = (*FullModeSessionController)(nil)
//	var _ SessionController = (*GatewaySessionController)(nil)
type SessionController interface {
	// Start begins a new voice session for the given guild and channel.
	// Returns an error if a session is already active for the guild or if
	// the session cannot be started.
	Start(ctx context.Context, req SessionStartRequest) error

	// Stop gracefully ends the session with the given ID. Returns an error
	// if the session does not exist or cannot be stopped.
	Stop(ctx context.Context, sessionID string) error

	// IsActive reports whether a voice session is currently running for the
	// given guild. In full mode this checks the single in-process session;
	// in gateway mode it queries the orchestrator's session state.
	IsActive(guildID string) bool

	// Info returns metadata about the active session for the given guild.
	// Returns zero-value SessionInfo and false if no session is active.
	Info(guildID string) (SessionInfo, bool)
}
