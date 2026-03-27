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
// Invalid transitions (e.g., ended→active) are rejected. Transitions from
// ended are silently ignored to make idempotent stop calls safe.
func (m *MemoryOrchestrator) Transition(_ context.Context, sessionID string, state gateway.SessionState, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("sessionorch: session %q not found", sessionID)
	}

	// Prevent re-opening ended sessions (idempotent stop is OK).
	if s.State == gateway.SessionEnded {
		return nil
	}

	if !gateway.ValidTransition(s.State, state) {
		return fmt.Errorf("sessionorch: invalid transition %s → %s for session %q", s.State, state, sessionID)
	}

	s.State = state
	if state == gateway.SessionEnded {
		now := time.Now().UTC()
		s.EndedAt = &now
		s.Error = errMsg
	}
	// Set initial heartbeat when transitioning to active (prevents NULL heartbeat zombies).
	if state == gateway.SessionActive {
		now := time.Now().UTC()
		s.LastHeartbeat = &now
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
// Also catches active sessions with NULL heartbeat (worker died before first
// heartbeat tick) that are older than the timeout.
// Returns the IDs of cleaned-up sessions.
func (m *MemoryOrchestrator) CleanupZombies(_ context.Context, timeout time.Duration) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cutoff := time.Now().UTC().Add(-timeout)
	var ids []string

	for _, s := range m.sessions {
		if s.State == gateway.SessionEnded {
			continue
		}

		stale := s.LastHeartbeat != nil && s.LastHeartbeat.Before(cutoff)
		// Catch sessions in active state with NULL heartbeat (worker died
		// before first heartbeat tick).
		nullHBZombie := s.LastHeartbeat == nil && s.State != gateway.SessionPending && s.StartedAt.Before(cutoff)

		if stale || nullHBZombie {
			s.State = gateway.SessionEnded
			now := time.Now().UTC()
			s.EndedAt = &now
			s.Error = "heartbeat timeout"
			ids = append(ids, s.ID)
		}
	}

	return ids, nil
}

// CleanupStalePending transitions sessions stuck in 'pending' state
// older than maxAge to ended.
// Returns the IDs of cleaned-up sessions.
func (m *MemoryOrchestrator) CleanupStalePending(_ context.Context, maxAge time.Duration) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cutoff := time.Now().UTC().Add(-maxAge)
	var ids []string

	for _, s := range m.sessions {
		if s.State != gateway.SessionPending {
			continue
		}
		if s.StartedAt.Before(cutoff) {
			s.State = gateway.SessionEnded
			now := time.Now().UTC()
			s.EndedAt = &now
			s.Error = "stale pending: dispatch timeout"
			ids = append(ids, s.ID)
		}
	}

	return ids, nil
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
