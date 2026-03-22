package gateway

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/internal/config"
)

// mockOrchestrator implements SessionOrchestrator for testing.
type mockOrchestrator struct {
	validateErr   error
	transitionErr error
	sessionID     string
	sessions      map[string]SessionInfo
}

func newMockOrch() *mockOrchestrator {
	return &mockOrchestrator{
		sessionID: "test-session-1",
		sessions:  make(map[string]SessionInfo),
	}
}

func (m *mockOrchestrator) ValidateAndCreate(_ context.Context, _, _, guildID, channelID string, _ config.LicenseTier) (string, error) {
	if m.validateErr != nil {
		return "", m.validateErr
	}
	m.sessions[m.sessionID] = SessionInfo{
		SessionID: m.sessionID,
		GuildID:   guildID,
		ChannelID: channelID,
		StartedAt: time.Now(),
		State:     SessionActive,
	}
	return m.sessionID, nil
}

func (m *mockOrchestrator) Transition(_ context.Context, sessionID string, state SessionState, _ string) error {
	if m.transitionErr != nil {
		return m.transitionErr
	}
	if info, ok := m.sessions[sessionID]; ok {
		info.State = state
		m.sessions[sessionID] = info
	}
	return nil
}

func (m *mockOrchestrator) GetSessionInfo(_ context.Context, sessionID string) (SessionInfo, error) {
	info, ok := m.sessions[sessionID]
	if !ok {
		return SessionInfo{}, fmt.Errorf("session %s not found", sessionID)
	}
	return info, nil
}

func (m *mockOrchestrator) ListActiveSessionIDs(_ context.Context, _ string) ([]string, error) {
	var ids []string
	for id, info := range m.sessions {
		if info.State != SessionEnded {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func TestGatewaySessionController_StartStop(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared)

	ctx := context.Background()

	// Start a session.
	err := ctrl.Start(ctx, SessionStartRequest{
		GuildID:   "guild-1",
		ChannelID: "chan-1",
		UserID:    "user-1",
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Should be active.
	if !ctrl.IsActive("guild-1") {
		t.Error("expected session to be active")
	}

	// Info should return session data.
	info, ok := ctrl.Info("guild-1")
	if !ok {
		t.Fatal("expected Info to return data")
	}
	if info.SessionID != "test-session-1" {
		t.Errorf("got session ID %q, want %q", info.SessionID, "test-session-1")
	}

	// Start again should fail (already active).
	err = ctrl.Start(ctx, SessionStartRequest{
		GuildID:   "guild-1",
		ChannelID: "chan-2",
		UserID:    "user-2",
	})
	if err == nil {
		t.Error("expected error for duplicate start")
	}

	// Stop the session.
	err = ctrl.Stop(ctx, "test-session-1")
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	// Should no longer be active.
	if ctrl.IsActive("guild-1") {
		t.Error("expected session to not be active after stop")
	}
}

func TestGatewaySessionController_StartValidationFailure(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	orch.validateErr = fmt.Errorf("license constraint violated")

	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared)

	err := ctrl.Start(context.Background(), SessionStartRequest{
		GuildID:   "guild-1",
		ChannelID: "chan-1",
		UserID:    "user-1",
	})
	if err == nil {
		t.Fatal("expected error from validation failure")
	}

	if ctrl.IsActive("guild-1") {
		t.Error("session should not be active after validation failure")
	}
}

func TestGatewaySessionController_InfoNotFound(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared)

	_, ok := ctrl.Info("nonexistent-guild")
	if ok {
		t.Error("expected Info to return false for non-existent guild")
	}
}
