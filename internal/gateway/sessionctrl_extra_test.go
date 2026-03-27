package gateway

import (
	"context"
	"fmt"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/internal/gateway/audiobridge"
)

func TestGatewaySessionController_StopAll(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared)

	ctx := context.Background()

	// Start two sessions.
	orch.sessionID = "session-1"
	err := ctrl.Start(ctx, SessionStartRequest{GuildID: "guild-1", ChannelID: "chan-1", UserID: "user-1"})
	if err != nil {
		t.Fatalf("Start session-1 failed: %v", err)
	}

	orch.sessionID = "session-2"
	err = ctrl.Start(ctx, SessionStartRequest{GuildID: "guild-2", ChannelID: "chan-2", UserID: "user-2"})
	if err != nil {
		t.Fatalf("Start session-2 failed: %v", err)
	}

	// Verify both are active.
	if !ctrl.IsActive("guild-1") {
		t.Error("expected guild-1 to be active")
	}
	if !ctrl.IsActive("guild-2") {
		t.Error("expected guild-2 to be active")
	}

	// StopAll should stop both.
	ctrl.StopAll(ctx)

	if ctrl.IsActive("guild-1") {
		t.Error("expected guild-1 to be inactive after StopAll")
	}
	if ctrl.IsActive("guild-2") {
		t.Error("expected guild-2 to be inactive after StopAll")
	}
}

func TestGatewaySessionController_StopNonexistent(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	orch.transitionErr = fmt.Errorf("session not found")
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared)

	err := ctrl.Stop(context.Background(), "nonexistent-session")
	if err == nil {
		t.Error("expected error when stopping nonexistent session")
	}
}

func TestGatewaySessionController_WithBotToken(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared,
		WithBotToken("my-token"),
	)

	if ctrl.botToken != "my-token" {
		t.Errorf("got botToken %q, want %q", ctrl.botToken, "my-token")
	}
}

func TestGatewaySessionController_WithNPCConfigs(t *testing.T) {
	t.Parallel()

	configs := []NPCConfigMsg{
		{Name: "Bartender", Personality: "grumpy"},
		{Name: "Guard", Personality: "alert"},
	}

	orch := newMockOrch()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared,
		WithNPCConfigs(configs),
	)

	if len(ctrl.npcConfigs) != 2 {
		t.Fatalf("got %d NPC configs, want 2", len(ctrl.npcConfigs))
	}
	if ctrl.npcConfigs[0].Name != "Bartender" {
		t.Errorf("got NPC name %q, want %q", ctrl.npcConfigs[0].Name, "Bartender")
	}
}

func TestGatewaySessionController_WithWorkerDialer(t *testing.T) {
	t.Parallel()

	dialer := func(addr string) (WorkerClient, error) {
		return nil, fmt.Errorf("not implemented")
	}

	orch := newMockOrch()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared,
		WithWorkerDialer(dialer),
	)

	if ctrl.dialer == nil {
		t.Error("expected dialer to be set")
	}
}

func TestGatewaySessionController_InfoOrchestratorError(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared)

	ctx := context.Background()

	// Start a session.
	orch.sessionID = "session-1"
	err := ctrl.Start(ctx, SessionStartRequest{GuildID: "guild-1", ChannelID: "chan-1", UserID: "user-1"})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Now remove the session from the orchestrator to simulate a lookup error.
	delete(orch.sessions, "session-1")

	// Info should still return true (session is in active map), but with minimal info.
	info, ok := ctrl.Info("guild-1")
	if !ok {
		t.Fatal("expected Info to return true for active session")
	}
	if info.SessionID != "session-1" {
		t.Errorf("got session ID %q, want %q", info.SessionID, "session-1")
	}
}

func TestGatewaySessionController_IsActive_NotFound(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared)

	if ctrl.IsActive("nonexistent") {
		t.Error("expected IsActive to return false for unknown guild")
	}
}

func TestGatewaySessionController_MultipleOptions(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared,
		WithBotToken("tok"),
		WithNPCConfigs([]NPCConfigMsg{{Name: "NPC1"}}),
	)

	if ctrl.botToken != "tok" {
		t.Errorf("botToken = %q, want %q", ctrl.botToken, "tok")
	}
	if len(ctrl.npcConfigs) != 1 {
		t.Errorf("npcConfigs length = %d, want 1", len(ctrl.npcConfigs))
	}
}

func TestGatewaySessionController_StopCleansUpActiveMap(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared)

	ctx := context.Background()

	// Start a session.
	orch.sessionID = "session-stop"
	err := ctrl.Start(ctx, SessionStartRequest{GuildID: "guild-1", ChannelID: "chan-1", UserID: "user-1"})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Stop and verify cleanup.
	err = ctrl.Stop(ctx, "session-stop")
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if ctrl.IsActive("guild-1") {
		t.Error("guild should not be active after Stop")
	}

	// Starting a new session for the same guild should now work.
	orch.sessionID = "session-new"
	err = ctrl.Start(ctx, SessionStartRequest{GuildID: "guild-1", ChannelID: "chan-2", UserID: "user-2"})
	if err != nil {
		t.Errorf("re-start after stop failed: %v", err)
	}
}

func TestGatewaySessionController_StopAllEmpty(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared)

	// StopAll with no active sessions should not panic.
	ctrl.StopAll(context.Background())
}

func TestGatewaySessionController_WithGatewayBot(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	gwBot := NewGatewayBot(nil, nil, nil, "tenant-1", nil)
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared,
		WithGatewayBot(gwBot),
	)

	if ctrl.gwBot != gwBot {
		t.Error("expected gwBot to be set")
	}
}

func TestGatewaySessionController_WithAudioBridgeServer(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	srv := audiobridge.NewServer()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared,
		WithAudioBridgeServer(srv),
	)

	if ctrl.bridgeSrv != srv {
		t.Error("expected bridgeSrv to be set")
	}
}

// mockNPCController implements NPCController for dialNPCController tests.
type mockNPCController struct {
	listNPCs     []NPCStatus
	listErr      error
	muteErr      error
	unmuteErr    error
	muteAllN     int
	muteAllErr   error
	unmuteAllN   int
	unmuteAllErr error
	speakErr     error
}

func (m *mockNPCController) ListNPCs(_ context.Context, _ string) ([]NPCStatus, error) {
	return m.listNPCs, m.listErr
}
func (m *mockNPCController) MuteNPC(_ context.Context, _, _ string) error   { return m.muteErr }
func (m *mockNPCController) UnmuteNPC(_ context.Context, _, _ string) error { return m.unmuteErr }
func (m *mockNPCController) MuteAllNPCs(_ context.Context, _ string) (int, error) {
	return m.muteAllN, m.muteAllErr
}
func (m *mockNPCController) UnmuteAllNPCs(_ context.Context, _ string) (int, error) {
	return m.unmuteAllN, m.unmuteAllErr
}
func (m *mockNPCController) SpeakNPC(_ context.Context, _, _, _ string) error { return m.speakErr }

// mockWorkerClient implements both WorkerClient and NPCController for dial tests.
type mockWorkerClient struct {
	mockNPCController
	startErr  error
	stopErr   error
	statuses  []SessionStatus
	statusErr error
	closed    bool
}

func (m *mockWorkerClient) StartSession(_ context.Context, _ StartSessionRequest) error {
	return m.startErr
}
func (m *mockWorkerClient) StopSession(_ context.Context, _ string) error { return m.stopErr }
func (m *mockWorkerClient) GetStatus(_ context.Context) ([]SessionStatus, error) {
	return m.statuses, m.statusErr
}
func (m *mockWorkerClient) Close() error {
	m.closed = true
	return nil
}

func TestGatewaySessionController_ListNPCs_NoWorkerAddress(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared)

	_, err := ctrl.ListNPCs(context.Background(), "nonexistent-session")
	if err == nil {
		t.Error("expected error when no worker address exists")
	}
}

func TestGatewaySessionController_ListNPCs_NoDialer(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared)

	// Manually add a worker address to test the "no dialer" path.
	ctrl.mu.Lock()
	ctrl.workerAddrs["session-1"] = "localhost:1234"
	ctrl.mu.Unlock()

	_, err := ctrl.ListNPCs(context.Background(), "session-1")
	if err == nil {
		t.Error("expected error when no dialer is configured")
	}
}

func TestGatewaySessionController_NPCMethods_WithMockDialer(t *testing.T) {
	t.Parallel()

	// Return a fresh mock each time to avoid data races on Close().
	dialer := func(_ string) (WorkerClient, error) {
		return &mockWorkerClient{
			mockNPCController: mockNPCController{
				listNPCs: []NPCStatus{{ID: "npc-1", Name: "Bartender", Muted: false}},
			},
		}, nil
	}

	orch := newMockOrch()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared,
		WithWorkerDialer(dialer),
	)

	// Add a worker address.
	ctrl.mu.Lock()
	ctrl.workerAddrs["session-1"] = "localhost:5000"
	ctrl.mu.Unlock()

	ctx := context.Background()

	t.Run("ListNPCs", func(t *testing.T) {
		t.Parallel()
		npcs, err := ctrl.ListNPCs(ctx, "session-1")
		if err != nil {
			t.Fatalf("ListNPCs: %v", err)
		}
		if len(npcs) != 1 || npcs[0].Name != "Bartender" {
			t.Errorf("unexpected NPCs: %v", npcs)
		}
	})

	t.Run("MuteNPC", func(t *testing.T) {
		t.Parallel()
		err := ctrl.MuteNPC(ctx, "session-1", "Bartender")
		if err != nil {
			t.Errorf("MuteNPC: %v", err)
		}
	})

	t.Run("UnmuteNPC", func(t *testing.T) {
		t.Parallel()
		err := ctrl.UnmuteNPC(ctx, "session-1", "Bartender")
		if err != nil {
			t.Errorf("UnmuteNPC: %v", err)
		}
	})

	t.Run("MuteAllNPCs", func(t *testing.T) {
		t.Parallel()
		n, err := ctrl.MuteAllNPCs(ctx, "session-1")
		if err != nil {
			t.Errorf("MuteAllNPCs: %v", err)
		}
		_ = n
	})

	t.Run("UnmuteAllNPCs", func(t *testing.T) {
		t.Parallel()
		n, err := ctrl.UnmuteAllNPCs(ctx, "session-1")
		if err != nil {
			t.Errorf("UnmuteAllNPCs: %v", err)
		}
		_ = n
	})

	t.Run("SpeakNPC", func(t *testing.T) {
		t.Parallel()
		err := ctrl.SpeakNPC(ctx, "session-1", "Bartender", "Hello there!")
		if err != nil {
			t.Errorf("SpeakNPC: %v", err)
		}
	})
}

func TestGatewaySessionController_DialNPCController_DialError(t *testing.T) {
	t.Parallel()

	dialer := func(_ string) (WorkerClient, error) {
		return nil, fmt.Errorf("dial failed")
	}

	orch := newMockOrch()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared,
		WithWorkerDialer(dialer),
	)

	ctrl.mu.Lock()
	ctrl.workerAddrs["session-1"] = "localhost:5000"
	ctrl.mu.Unlock()

	_, err := ctrl.ListNPCs(context.Background(), "session-1")
	if err == nil {
		t.Error("expected error when dial fails")
	}
}

func TestGatewaySessionController_DialNPCController_NotNPCController(t *testing.T) {
	t.Parallel()

	// Return a WorkerClient that does NOT implement NPCController.
	dialer := func(_ string) (WorkerClient, error) {
		return &simpleWorkerClient{}, nil
	}

	orch := newMockOrch()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared,
		WithWorkerDialer(dialer),
	)

	ctrl.mu.Lock()
	ctrl.workerAddrs["session-1"] = "localhost:5000"
	ctrl.mu.Unlock()

	_, err := ctrl.ListNPCs(context.Background(), "session-1")
	if err == nil {
		t.Error("expected error when client does not implement NPCController")
	}
}

// simpleWorkerClient implements WorkerClient but NOT NPCController.
type simpleWorkerClient struct{}

func (s *simpleWorkerClient) StartSession(context.Context, StartSessionRequest) error { return nil }
func (s *simpleWorkerClient) StopSession(context.Context, string) error               { return nil }
func (s *simpleWorkerClient) GetStatus(context.Context) ([]SessionStatus, error)      { return nil, nil }
func (s *simpleWorkerClient) Close() error                                            { return nil }

func TestGatewaySessionController_CleanupVoiceBridge(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	srv := audiobridge.NewServer()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared,
		WithAudioBridgeServer(srv),
	)

	// Register a voice cleanup function.
	called := false
	ctrl.voiceCleanupsMu.Lock()
	ctrl.voiceCleanups["session-x"] = func() { called = true }
	ctrl.voiceCleanupsMu.Unlock()

	ctrl.cleanupVoiceBridge("session-x")

	if !called {
		t.Error("expected voice cleanup function to be called")
	}

	// Second cleanup should not panic.
	ctrl.cleanupVoiceBridge("session-x")
}

func TestGatewaySessionController_CleanupVoiceBridge_NoCleanup(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared)

	// Should not panic when no cleanup is registered and no bridgeSrv.
	ctrl.cleanupVoiceBridge("nonexistent")
}

func TestGatewaySessionController_RemoveSession(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared)

	ctx := context.Background()

	// Start a session.
	orch.sessionID = "session-zombie"
	err := ctrl.Start(ctx, SessionStartRequest{GuildID: "guild-1", ChannelID: "chan-1", UserID: "user-1"})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if !ctrl.IsActive("guild-1") {
		t.Fatal("expected session to be active")
	}

	// Simulate zombie cleanup removing the session.
	ctrl.RemoveSession("session-zombie")

	if ctrl.IsActive("guild-1") {
		t.Error("expected session to be inactive after RemoveSession")
	}

	// Should be able to start a new session for the same guild.
	orch.sessionID = "session-new"
	err = ctrl.Start(ctx, SessionStartRequest{GuildID: "guild-1", ChannelID: "chan-2", UserID: "user-2"})
	if err != nil {
		t.Errorf("expected Start to succeed after RemoveSession, got: %v", err)
	}
}

func TestGatewaySessionController_ConcurrentStop(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared)

	ctx := context.Background()

	// Start a session.
	orch.sessionID = "session-concurrent"
	err := ctrl.Start(ctx, SessionStartRequest{GuildID: "guild-1", ChannelID: "chan-1", UserID: "user-1"})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Stop concurrently — both should succeed without panic.
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			errs <- ctrl.Stop(ctx, "session-concurrent")
		}()
	}

	for range 2 {
		if err := <-errs; err != nil {
			t.Errorf("concurrent Stop error: %v", err)
		}
	}

	if ctrl.IsActive("guild-1") {
		t.Error("expected session to be inactive after concurrent stops")
	}
}
