package sessionorch

import (
	"context"

	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/internal/gateway"
)

// Compile-time interface assertion.
var _ gateway.SessionOrchestrator = (*OrchestratorAdapter)(nil)

// OrchestratorAdapter wraps an [Orchestrator] to satisfy the
// [gateway.SessionOrchestrator] interface, which uses flat parameters
// instead of package-local types to avoid import cycles.
type OrchestratorAdapter struct {
	orch Orchestrator
}

// NewOrchestratorAdapter wraps the given Orchestrator.
func NewOrchestratorAdapter(orch Orchestrator) *OrchestratorAdapter {
	return &OrchestratorAdapter{orch: orch}
}

// ValidateAndCreate delegates to the underlying orchestrator.
func (a *OrchestratorAdapter) ValidateAndCreate(ctx context.Context, tenantID, campaignID, guildID, channelID string, tier config.LicenseTier) (string, error) {
	return a.orch.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    tenantID,
		CampaignID:  campaignID,
		GuildID:     guildID,
		ChannelID:   channelID,
		LicenseTier: tier,
	})
}

// Transition delegates to the underlying orchestrator.
func (a *OrchestratorAdapter) Transition(ctx context.Context, sessionID string, state gateway.SessionState, errMsg string) error {
	return a.orch.Transition(ctx, sessionID, state, errMsg)
}

// GetSessionInfo maps a Session to a gateway.SessionInfo.
func (a *OrchestratorAdapter) GetSessionInfo(ctx context.Context, sessionID string) (gateway.SessionInfo, error) {
	sess, err := a.orch.GetSession(ctx, sessionID)
	if err != nil {
		return gateway.SessionInfo{}, err
	}
	return gateway.SessionInfo{
		SessionID:    sess.ID,
		GuildID:      sess.GuildID,
		ChannelID:    sess.ChannelID,
		CampaignName: sess.CampaignID,
		StartedAt:    sess.StartedAt,
		State:        sess.State,
	}, nil
}

// ListActiveSessionIDs returns IDs of non-ended sessions for a tenant.
func (a *OrchestratorAdapter) ListActiveSessionIDs(ctx context.Context, tenantID string) ([]string, error) {
	sessions, err := a.orch.ActiveSessions(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(sessions))
	for i, s := range sessions {
		ids[i] = s.ID
	}
	return ids, nil
}
