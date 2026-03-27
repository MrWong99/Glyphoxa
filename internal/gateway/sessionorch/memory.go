package sessionorch

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/internal/gateway"
)

// Compile-time interface assertion.
var _ Orchestrator = (*MemoryOrchestrator)(nil)

// MemoryOrchestrator is an in-memory Orchestrator for --mode=full.
// It enforces the same constraints as the PostgreSQL version but
// stores everything in a map. Not suitable for distributed deployments.
//
// All methods are safe for concurrent use.
type MemoryOrchestrator struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

// NewMemoryOrchestrator creates a ready-to-use in-memory orchestrator.
func NewMemoryOrchestrator() *MemoryOrchestrator {
	return &MemoryOrchestrator{
		sessions: make(map[string]*Session),
	}
}

// ValidateAndCreate checks license constraints and creates a session.
func (m *MemoryOrchestrator) ValidateAndCreate(_ context.Context, req SessionRequest) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check constraints — filter by tenant first to prevent cross-tenant interference.
	for _, s := range m.sessions {
		if s.State == gateway.SessionEnded {
			continue
		}

		if s.TenantID != req.TenantID {
			continue
		}

		// Prevent two active sessions for the same campaign within the tenant.
		if s.CampaignID == req.CampaignID {
			return "", fmt.Errorf("sessionorch: campaign %q already has an active session %q", req.CampaignID, s.ID)
		}

		// Shared tier: at most 1 active session per tenant.
		if req.LicenseTier == config.TierShared {
			return "", fmt.Errorf("sessionorch: shared tenant %q already has an active session %q", req.TenantID, s.ID)
		}

		// Dedicated tier: at most 1 active session per guild.
		if req.LicenseTier == config.TierDedicated && s.GuildID == req.GuildID {
			return "", fmt.Errorf("sessionorch: dedicated tenant %q guild %q already has an active session %q", req.TenantID, req.GuildID, s.ID)
		}
	}

	id := uuid.NewString()
	now := time.Now().UTC()
	m.sessions[id] = &Session{
		ID:          id,
		TenantID:    req.TenantID,
		CampaignID:  req.CampaignID,
		GuildID:     req.GuildID,
		ChannelID:   req.ChannelID,
		LicenseTier: req.LicenseTier,
		State:       gateway.SessionPending,
		StartedAt:   now,
	}

	return id, nil
}

// Transition moves a session to the given state.
func (m *MemoryOrchestrator) Transition(_ context.Context, sessionID string, state gateway.SessionState, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("sessionorch: session %q not found", sessionID)
	}

	s.State = state
	if state == gateway.SessionEnded {
		now := time.Now().UTC()
		s.EndedAt = &now
		s.Error = errMsg
	}

	return nil
}

// RecordHeartbeat updates the last heartbeat timestamp.
func (m *MemoryOrchestrator) RecordHeartbeat(_ context.Context, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("sessionorch: session %q not found", sessionID)
	}

	now := time.Now().UTC()
	s.LastHeartbeat = &now
	return nil
}

// ActiveSessions returns all non-ended sessions for a tenant.
func (m *MemoryOrchestrator) ActiveSessions(_ context.Context, tenantID string) ([]Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var result []Session
	for _, s := range m.sessions {
		if s.TenantID == tenantID && s.State != gateway.SessionEnded {
			result = append(result, *s)
		}
	}
	return result, nil
}

// GetSession returns a single session by ID.
func (m *MemoryOrchestrator) GetSession(_ context.Context, sessionID string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return Session{}, fmt.Errorf("sessionorch: session %q not found", sessionID)
	}
	return *s, nil
}

// CleanupZombies transitions sessions with stale heartbeats to ended.
func (m *MemoryOrchestrator) CleanupZombies(_ context.Context, timeout time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cutoff := time.Now().UTC().Add(-timeout)
	count := 0

	for _, s := range m.sessions {
		if s.State == gateway.SessionEnded {
			continue
		}
		if s.LastHeartbeat != nil && s.LastHeartbeat.Before(cutoff) {
			s.State = gateway.SessionEnded
			now := time.Now().UTC()
			s.EndedAt = &now
			s.Error = "heartbeat timeout"
			count++
		}
	}

	return count, nil
}

// CleanupStalePending transitions sessions stuck in 'pending' state
// older than maxAge to ended.
func (m *MemoryOrchestrator) CleanupStalePending(_ context.Context, maxAge time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cutoff := time.Now().UTC().Add(-maxAge)
	count := 0

	for _, s := range m.sessions {
		if s.State != gateway.SessionPending {
			continue
		}
		if s.StartedAt.Before(cutoff) {
			s.State = gateway.SessionEnded
			now := time.Now().UTC()
			s.EndedAt = &now
			s.Error = "stale pending: dispatch timeout"
			count++
		}
	}

	return count, nil
}

// AllNonEndedSessions returns all non-ended sessions across all tenants.
func (m *MemoryOrchestrator) AllNonEndedSessions(_ context.Context) ([]Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []Session
	for _, s := range m.sessions {
		if s.State != gateway.SessionEnded {
			result = append(result, *s)
		}
	}
	return result, nil
}
