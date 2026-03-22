package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/internal/gateway/dispatch"
)

// Compile-time interface assertion.
var _ SessionController = (*GatewaySessionController)(nil)

// SessionOrchestrator is the subset of sessionorch.Orchestrator needed by
// GatewaySessionController. Defined here to avoid an import cycle between
// gateway and sessionorch.
type SessionOrchestrator interface {
	ValidateAndCreate(ctx context.Context, tenantID, campaignID, guildID, channelID string, tier config.LicenseTier) (string, error)
	Transition(ctx context.Context, sessionID string, state SessionState, errMsg string) error
	GetSessionInfo(ctx context.Context, sessionID string) (SessionInfo, error)
	ListActiveSessionIDs(ctx context.Context, tenantID string) ([]string, error)
}

// GatewaySessionController implements [SessionController] for gateway mode
// by composing a [SessionOrchestrator] with a K8s [dispatch.Dispatcher].
//
// All methods are safe for concurrent use.
type GatewaySessionController struct {
	orch       SessionOrchestrator
	dispatcher *dispatch.Dispatcher
	tenantID   string
	campaignID string
	tier       config.LicenseTier

	mu     sync.Mutex
	active map[string]string // guildID -> sessionID
}

// NewGatewaySessionController creates a GatewaySessionController.
func NewGatewaySessionController(
	orch SessionOrchestrator,
	dispatcher *dispatch.Dispatcher,
	tenantID, campaignID string,
	tier config.LicenseTier,
) *GatewaySessionController {
	return &GatewaySessionController{
		orch:       orch,
		dispatcher: dispatcher,
		tenantID:   tenantID,
		campaignID: campaignID,
		tier:       tier,
		active:     make(map[string]string),
	}
}

// Start begins a new voice session.
func (gc *GatewaySessionController) Start(ctx context.Context, req SessionStartRequest) error {
	gc.mu.Lock()
	if _, ok := gc.active[req.GuildID]; ok {
		gc.mu.Unlock()
		return fmt.Errorf("gateway: session already active for guild %s", req.GuildID)
	}
	gc.mu.Unlock()

	sessionID, err := gc.orch.ValidateAndCreate(ctx, gc.tenantID, gc.campaignID, req.GuildID, req.ChannelID, gc.tier)
	if err != nil {
		return fmt.Errorf("gateway: validate session: %w", err)
	}

	if gc.dispatcher != nil {
		result, dispErr := gc.dispatcher.Dispatch(ctx, sessionID, gc.tenantID)
		if dispErr != nil {
			if transErr := gc.orch.Transition(ctx, sessionID, SessionEnded, dispErr.Error()); transErr != nil {
				slog.Error("gateway: failed to transition session after dispatch failure",
					"session_id", sessionID, "err", transErr)
			}
			return fmt.Errorf("gateway: dispatch worker: %w", dispErr)
		}
		slog.Info("gateway: worker dispatched",
			"session_id", sessionID,
			"tenant_id", gc.tenantID,
			"address", result.Address,
		)
	}

	gc.mu.Lock()
	gc.active[req.GuildID] = sessionID
	gc.mu.Unlock()
	return nil
}

// Stop gracefully ends the session.
func (gc *GatewaySessionController) Stop(ctx context.Context, sessionID string) error {
	if gc.dispatcher != nil {
		if err := gc.dispatcher.Stop(ctx, sessionID); err != nil {
			slog.Warn("gateway: dispatcher stop error",
				"session_id", sessionID, "err", err)
		}
	}
	if err := gc.orch.Transition(ctx, sessionID, SessionEnded, ""); err != nil {
		return fmt.Errorf("gateway: transition session to ended: %w", err)
	}
	gc.mu.Lock()
	for guildID, sid := range gc.active {
		if sid == sessionID {
			delete(gc.active, guildID)
			break
		}
	}
	gc.mu.Unlock()
	return nil
}

// IsActive reports whether a session is running for the guild.
func (gc *GatewaySessionController) IsActive(guildID string) bool {
	gc.mu.Lock()
	_, ok := gc.active[guildID]
	gc.mu.Unlock()
	return ok
}

// Info returns metadata about the active session for the guild.
func (gc *GatewaySessionController) Info(guildID string) (SessionInfo, bool) {
	gc.mu.Lock()
	sessionID, ok := gc.active[guildID]
	gc.mu.Unlock()

	if !ok {
		return SessionInfo{}, false
	}

	info, err := gc.orch.GetSessionInfo(context.Background(), sessionID)
	if err != nil {
		slog.Warn("gateway: failed to get session info",
			"session_id", sessionID, "err", err)
		return SessionInfo{
			SessionID: sessionID,
			GuildID:   guildID,
			StartedAt: time.Time{},
		}, true
	}
	return info, true
}
