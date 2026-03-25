package local_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/gateway"
	"github.com/MrWong99/glyphoxa/internal/gateway/local"
)

func TestClient_StartSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		startFn local.SessionStartFunc
		wantErr bool
	}{
		{
			name:    "success",
			startFn: func(_ context.Context, _ gateway.StartSessionRequest) error { return nil },
		},
		{
			name:    "start error",
			startFn: func(_ context.Context, _ gateway.StartSessionRequest) error { return fmt.Errorf("boom") },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := local.NewClient(tt.startFn, func(_ context.Context, _ string) error { return nil })

			err := c.StartSession(context.Background(), gateway.StartSessionRequest{
				SessionID: "test-session",
				TenantID:  "local",
			})

			if (err != nil) != tt.wantErr {
				t.Errorf("StartSession() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestClient_StopSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		stopFn  local.SessionStopFunc
		wantErr bool
	}{
		{
			name:   "success",
			stopFn: func(_ context.Context, _ string) error { return nil },
		},
		{
			name:    "stop error",
			stopFn:  func(_ context.Context, _ string) error { return fmt.Errorf("boom") },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := local.NewClient(
				func(_ context.Context, _ gateway.StartSessionRequest) error { return nil },
				tt.stopFn,
			)

			err := c.StopSession(context.Background(), "test-session")
			if (err != nil) != tt.wantErr {
				t.Errorf("StopSession() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestClient_GetStatus(t *testing.T) {
	t.Parallel()

	c := local.NewClient(
		func(_ context.Context, _ gateway.StartSessionRequest) error { return nil },
		func(_ context.Context, _ string) error { return nil },
	)

	// Start a session.
	err := c.StartSession(context.Background(), gateway.StartSessionRequest{
		SessionID: "s1",
		TenantID:  "local",
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	statuses, err := c.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("got %d statuses, want 1", len(statuses))
	}
	if statuses[0].State != gateway.SessionActive {
		t.Errorf("got state %v, want active", statuses[0].State)
	}

	// Stop it.
	if err := c.StopSession(context.Background(), "s1"); err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	statuses, err = c.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus after stop: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("got %d statuses, want 1", len(statuses))
	}
	if statuses[0].State != gateway.SessionEnded {
		t.Errorf("got state %v, want ended", statuses[0].State)
	}
}

func TestClient_Concurrent(t *testing.T) {
	t.Parallel()

	c := local.NewClient(
		func(_ context.Context, _ gateway.StartSessionRequest) error { return nil },
		func(_ context.Context, _ string) error { return nil },
	)

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Go(func() {
			sid := fmt.Sprintf("session-%d", i)
			_ = c.StartSession(context.Background(), gateway.StartSessionRequest{SessionID: sid})
			_ = c.StopSession(context.Background(), sid)
			_, _ = c.GetStatus(context.Background())
		})
	}
	wg.Wait()
}

// mockNPCController is a test double for gateway.NPCController.
// Each field is a function that gets called by the corresponding method.
type mockNPCController struct {
	listNPCsFn    func(ctx context.Context, sessionID string) ([]gateway.NPCStatus, error)
	muteNPCFn     func(ctx context.Context, sessionID, npcName string) error
	unmuteNPCFn   func(ctx context.Context, sessionID, npcName string) error
	muteAllFn     func(ctx context.Context, sessionID string) (int, error)
	unmuteAllFn   func(ctx context.Context, sessionID string) (int, error)
	speakNPCFn    func(ctx context.Context, sessionID, npcName, text string) error
}

func (m *mockNPCController) ListNPCs(ctx context.Context, sessionID string) ([]gateway.NPCStatus, error) {
	return m.listNPCsFn(ctx, sessionID)
}

func (m *mockNPCController) MuteNPC(ctx context.Context, sessionID, npcName string) error {
	return m.muteNPCFn(ctx, sessionID, npcName)
}

func (m *mockNPCController) UnmuteNPC(ctx context.Context, sessionID, npcName string) error {
	return m.unmuteNPCFn(ctx, sessionID, npcName)
}

func (m *mockNPCController) MuteAllNPCs(ctx context.Context, sessionID string) (int, error) {
	return m.muteAllFn(ctx, sessionID)
}

func (m *mockNPCController) UnmuteAllNPCs(ctx context.Context, sessionID string) (int, error) {
	return m.unmuteAllFn(ctx, sessionID)
}

func (m *mockNPCController) SpeakNPC(ctx context.Context, sessionID, npcName, text string) error {
	return m.speakNPCFn(ctx, sessionID, npcName, text)
}

func TestNewNPCClient(t *testing.T) {
	t.Parallel()

	mock := &mockNPCController{}
	client := local.NewNPCClient(mock)
	if client == nil {
		t.Fatal("NewNPCClient returned nil")
	}
}

func TestNPCClient_ListNPCs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		handler   *mockNPCController
		wantCount int
		wantErr   bool
	}{
		{
			name: "success",
			handler: &mockNPCController{
				listNPCsFn: func(_ context.Context, _ string) ([]gateway.NPCStatus, error) {
					return []gateway.NPCStatus{
						{ID: "npc-1", Name: "Bartender", Muted: false},
						{ID: "npc-2", Name: "Guard", Muted: true},
					}, nil
				},
			},
			wantCount: 2,
		},
		{
			name: "error",
			handler: &mockNPCController{
				listNPCsFn: func(_ context.Context, _ string) ([]gateway.NPCStatus, error) {
					return nil, fmt.Errorf("list failed")
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := local.NewNPCClient(tt.handler)
			npcs, err := client.ListNPCs(context.Background(), "session-1")
			if (err != nil) != tt.wantErr {
				t.Errorf("ListNPCs() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && len(npcs) != tt.wantCount {
				t.Errorf("ListNPCs() returned %d npcs, want %d", len(npcs), tt.wantCount)
			}
		})
	}
}

func TestNPCClient_MuteNPC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler *mockNPCController
		wantErr bool
	}{
		{
			name: "success",
			handler: &mockNPCController{
				muteNPCFn: func(_ context.Context, _, _ string) error { return nil },
			},
		},
		{
			name: "error",
			handler: &mockNPCController{
				muteNPCFn: func(_ context.Context, _, _ string) error { return fmt.Errorf("mute failed") },
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := local.NewNPCClient(tt.handler)
			err := client.MuteNPC(context.Background(), "session-1", "Bartender")
			if (err != nil) != tt.wantErr {
				t.Errorf("MuteNPC() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNPCClient_UnmuteNPC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler *mockNPCController
		wantErr bool
	}{
		{
			name: "success",
			handler: &mockNPCController{
				unmuteNPCFn: func(_ context.Context, _, _ string) error { return nil },
			},
		},
		{
			name: "error",
			handler: &mockNPCController{
				unmuteNPCFn: func(_ context.Context, _, _ string) error { return fmt.Errorf("unmute failed") },
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := local.NewNPCClient(tt.handler)
			err := client.UnmuteNPC(context.Background(), "session-1", "Guard")
			if (err != nil) != tt.wantErr {
				t.Errorf("UnmuteNPC() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNPCClient_MuteAllNPCs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		handler   *mockNPCController
		wantCount int
		wantErr   bool
	}{
		{
			name: "success",
			handler: &mockNPCController{
				muteAllFn: func(_ context.Context, _ string) (int, error) { return 3, nil },
			},
			wantCount: 3,
		},
		{
			name: "error",
			handler: &mockNPCController{
				muteAllFn: func(_ context.Context, _ string) (int, error) { return 0, fmt.Errorf("mute all failed") },
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := local.NewNPCClient(tt.handler)
			count, err := client.MuteAllNPCs(context.Background(), "session-1")
			if (err != nil) != tt.wantErr {
				t.Errorf("MuteAllNPCs() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && count != tt.wantCount {
				t.Errorf("MuteAllNPCs() = %d, want %d", count, tt.wantCount)
			}
		})
	}
}

func TestNPCClient_UnmuteAllNPCs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		handler   *mockNPCController
		wantCount int
		wantErr   bool
	}{
		{
			name: "success",
			handler: &mockNPCController{
				unmuteAllFn: func(_ context.Context, _ string) (int, error) { return 5, nil },
			},
			wantCount: 5,
		},
		{
			name: "error",
			handler: &mockNPCController{
				unmuteAllFn: func(_ context.Context, _ string) (int, error) { return 0, fmt.Errorf("unmute all failed") },
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := local.NewNPCClient(tt.handler)
			count, err := client.UnmuteAllNPCs(context.Background(), "session-1")
			if (err != nil) != tt.wantErr {
				t.Errorf("UnmuteAllNPCs() error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && count != tt.wantCount {
				t.Errorf("UnmuteAllNPCs() = %d, want %d", count, tt.wantCount)
			}
		})
	}
}

func TestNPCClient_SpeakNPC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler *mockNPCController
		wantErr bool
	}{
		{
			name: "success",
			handler: &mockNPCController{
				speakNPCFn: func(_ context.Context, _, _, _ string) error { return nil },
			},
		},
		{
			name: "error",
			handler: &mockNPCController{
				speakNPCFn: func(_ context.Context, _, _, _ string) error { return fmt.Errorf("speak failed") },
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := local.NewNPCClient(tt.handler)
			err := client.SpeakNPC(context.Background(), "session-1", "Bartender", "Welcome, traveler!")
			if (err != nil) != tt.wantErr {
				t.Errorf("SpeakNPC() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCallback_NoOp(t *testing.T) {
	t.Parallel()

	cb := &local.Callback{}

	if err := cb.ReportState(context.Background(), "s1", gateway.SessionActive, ""); err != nil {
		t.Errorf("ReportState: %v", err)
	}
	if err := cb.Heartbeat(context.Background(), "s1"); err != nil {
		t.Errorf("Heartbeat: %v", err)
	}
}
