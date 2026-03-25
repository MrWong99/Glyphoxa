package grpctransport

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
	"github.com/MrWong99/glyphoxa/internal/gateway"
)

// ── Mock WorkerHandler ──────────────────────────────────────────────────────

type mockWorkerHandler struct {
	startErr      error
	stopErr       error
	getStatusErr  error
	listNPCsErr   error
	muteErr       error
	unmuteErr     error
	muteAllErr    error
	unmuteAllErr  error
	speakErr      error
	statuses      []gateway.SessionStatus
	npcs          []gateway.NPCStatus
	muteAllCount  int
	unmuteAllCnt  int
	lastStartReq  gateway.StartSessionRequest
	lastSessionID string
	lastNPCName   string
	lastSpeakText string
}

func (m *mockWorkerHandler) StartSession(_ context.Context, req gateway.StartSessionRequest) error {
	m.lastStartReq = req
	return m.startErr
}

func (m *mockWorkerHandler) StopSession(_ context.Context, sessionID string) error {
	m.lastSessionID = sessionID
	return m.stopErr
}

func (m *mockWorkerHandler) GetStatus(_ context.Context) ([]gateway.SessionStatus, error) {
	return m.statuses, m.getStatusErr
}

func (m *mockWorkerHandler) ListNPCs(_ context.Context, sessionID string) ([]gateway.NPCStatus, error) {
	m.lastSessionID = sessionID
	return m.npcs, m.listNPCsErr
}

func (m *mockWorkerHandler) MuteNPC(_ context.Context, sessionID, npcName string) error {
	m.lastSessionID = sessionID
	m.lastNPCName = npcName
	return m.muteErr
}

func (m *mockWorkerHandler) UnmuteNPC(_ context.Context, sessionID, npcName string) error {
	m.lastSessionID = sessionID
	m.lastNPCName = npcName
	return m.unmuteErr
}

func (m *mockWorkerHandler) MuteAllNPCs(_ context.Context, sessionID string) (int, error) {
	m.lastSessionID = sessionID
	return m.muteAllCount, m.muteAllErr
}

func (m *mockWorkerHandler) UnmuteAllNPCs(_ context.Context, sessionID string) (int, error) {
	m.lastSessionID = sessionID
	return m.unmuteAllCnt, m.unmuteAllErr
}

func (m *mockWorkerHandler) SpeakNPC(_ context.Context, sessionID, npcName, text string) error {
	m.lastSessionID = sessionID
	m.lastNPCName = npcName
	m.lastSpeakText = text
	return m.speakErr
}

// ── Mock GatewayCallback ────────────────────────────────────────────────────

type mockGatewayCallback struct {
	reportStateErr error
	heartbeatErr   error
	lastSessionID  string
	lastState      gateway.SessionState
	lastErrMsg     string
}

func (m *mockGatewayCallback) ReportState(_ context.Context, sessionID string, state gateway.SessionState, errMsg string) error {
	m.lastSessionID = sessionID
	m.lastState = state
	m.lastErrMsg = errMsg
	return m.reportStateErr
}

func (m *mockGatewayCallback) Heartbeat(_ context.Context, sessionID string) error {
	m.lastSessionID = sessionID
	return m.heartbeatErr
}

// ── WorkerServer Tests ──────────────────────────────────────────────────────

func TestWorkerServer_StartSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		handler   *mockWorkerHandler
		req       *pb.StartSessionRequest
		wantErr   bool
		wantSesID string
	}{
		{
			name:    "success",
			handler: &mockWorkerHandler{},
			req: &pb.StartSessionRequest{
				SessionId:   "sess-1",
				TenantId:    "tenant_a",
				CampaignId:  "camp-1",
				GuildId:     "guild-1",
				ChannelId:   "chan-1",
				LicenseTier: "shared",
				BotToken:    "tok-123",
				NpcConfigs: []*pb.NPCConfig{
					{
						Name:        "Gandalf",
						Personality: "wise wizard",
						Engine:      "cascade",
						VoiceId:     "voice-1",
					},
				},
			},
			wantSesID: "sess-1",
		},
		{
			name:    "handler returns error",
			handler: &mockWorkerHandler{startErr: errors.New("boom")},
			req: &pb.StartSessionRequest{
				SessionId: "sess-2",
				TenantId:  "tenant_b",
				GuildId:   "guild-2",
				ChannelId: "chan-2",
			},
			wantErr:   true,
			wantSesID: "sess-2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := NewWorkerServer(tt.handler)
			resp, err := srv.StartSession(context.Background(), tt.req)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				// Even on error the response should contain the session ID.
				if resp.GetSessionId() != tt.wantSesID {
					t.Errorf("resp.SessionId = %q, want %q", resp.GetSessionId(), tt.wantSesID)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.GetSessionId() != tt.wantSesID {
				t.Errorf("resp.SessionId = %q, want %q", resp.GetSessionId(), tt.wantSesID)
			}
			// Verify handler received the correct request fields.
			if tt.handler.lastStartReq.SessionID != tt.req.GetSessionId() {
				t.Errorf("handler.SessionID = %q, want %q", tt.handler.lastStartReq.SessionID, tt.req.GetSessionId())
			}
			if tt.handler.lastStartReq.TenantID != tt.req.GetTenantId() {
				t.Errorf("handler.TenantID = %q, want %q", tt.handler.lastStartReq.TenantID, tt.req.GetTenantId())
			}
			if len(tt.handler.lastStartReq.NPCConfigs) != len(tt.req.GetNpcConfigs()) {
				t.Errorf("handler.NPCConfigs len = %d, want %d", len(tt.handler.lastStartReq.NPCConfigs), len(tt.req.GetNpcConfigs()))
			}
		})
	}
}

func TestWorkerServer_StopSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler *mockWorkerHandler
		req     *pb.StopSessionRequest
		wantErr bool
	}{
		{
			name:    "success",
			handler: &mockWorkerHandler{},
			req:     &pb.StopSessionRequest{SessionId: "sess-1"},
		},
		{
			name:    "handler returns error",
			handler: &mockWorkerHandler{stopErr: errors.New("stop failed")},
			req:     &pb.StopSessionRequest{SessionId: "sess-2"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := NewWorkerServer(tt.handler)
			_, err := srv.StopSession(context.Background(), tt.req)

			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.handler.lastSessionID != tt.req.GetSessionId() {
				t.Errorf("handler.lastSessionID = %q, want %q", tt.handler.lastSessionID, tt.req.GetSessionId())
			}
		})
	}
}

func TestWorkerServer_GetStatus(t *testing.T) {
	t.Parallel()

	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		handler *mockWorkerHandler
		wantErr bool
		wantLen int
	}{
		{
			name: "returns statuses",
			handler: &mockWorkerHandler{
				statuses: []gateway.SessionStatus{
					{SessionID: "s1", State: gateway.SessionActive, StartedAt: now},
					{SessionID: "s2", State: gateway.SessionPending, StartedAt: now},
				},
			},
			wantLen: 2,
		},
		{
			name:    "empty statuses",
			handler: &mockWorkerHandler{},
			wantLen: 0,
		},
		{
			name:    "handler error",
			handler: &mockWorkerHandler{getStatusErr: errors.New("fail")},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := NewWorkerServer(tt.handler)
			resp, err := srv.GetStatus(context.Background(), &pb.GetStatusRequest{})

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(resp.GetSessions()) != tt.wantLen {
				t.Errorf("sessions len = %d, want %d", len(resp.GetSessions()), tt.wantLen)
			}
		})
	}
}

func TestWorkerServer_ListNPCs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler *mockWorkerHandler
		req     *pb.ListNPCsRequest
		wantErr bool
		wantLen int
	}{
		{
			name: "returns NPCs",
			handler: &mockWorkerHandler{
				npcs: []gateway.NPCStatus{
					{ID: "n1", Name: "Gandalf", Muted: false},
					{ID: "n2", Name: "Sauron", Muted: true},
				},
			},
			req:     &pb.ListNPCsRequest{SessionId: "sess-1"},
			wantLen: 2,
		},
		{
			name:    "handler error",
			handler: &mockWorkerHandler{listNPCsErr: errors.New("not found")},
			req:     &pb.ListNPCsRequest{SessionId: "sess-2"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := NewWorkerServer(tt.handler)
			resp, err := srv.ListNPCs(context.Background(), tt.req)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(resp.GetNpcs()) != tt.wantLen {
				t.Errorf("npcs len = %d, want %d", len(resp.GetNpcs()), tt.wantLen)
			}
			// Verify NPC fields are mapped correctly.
			for i, npc := range resp.GetNpcs() {
				if npc.GetName() != tt.handler.npcs[i].Name {
					t.Errorf("npc[%d].Name = %q, want %q", i, npc.GetName(), tt.handler.npcs[i].Name)
				}
				if npc.GetMuted() != tt.handler.npcs[i].Muted {
					t.Errorf("npc[%d].Muted = %v, want %v", i, npc.GetMuted(), tt.handler.npcs[i].Muted)
				}
			}
		})
	}
}

func TestWorkerServer_MuteNPC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler *mockWorkerHandler
		req     *pb.MuteNPCRequest
		wantErr bool
	}{
		{
			name:    "success",
			handler: &mockWorkerHandler{},
			req:     &pb.MuteNPCRequest{SessionId: "sess-1", NpcName: "Gandalf"},
		},
		{
			name:    "handler error",
			handler: &mockWorkerHandler{muteErr: errors.New("not found")},
			req:     &pb.MuteNPCRequest{SessionId: "sess-2", NpcName: "Unknown"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := NewWorkerServer(tt.handler)
			_, err := srv.MuteNPC(context.Background(), tt.req)

			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.handler.lastNPCName != tt.req.GetNpcName() {
				t.Errorf("handler.lastNPCName = %q, want %q", tt.handler.lastNPCName, tt.req.GetNpcName())
			}
		})
	}
}

func TestWorkerServer_UnmuteNPC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler *mockWorkerHandler
		req     *pb.UnmuteNPCRequest
		wantErr bool
	}{
		{
			name:    "success",
			handler: &mockWorkerHandler{},
			req:     &pb.UnmuteNPCRequest{SessionId: "sess-1", NpcName: "Gandalf"},
		},
		{
			name:    "handler error",
			handler: &mockWorkerHandler{unmuteErr: errors.New("not found")},
			req:     &pb.UnmuteNPCRequest{SessionId: "sess-2", NpcName: "Unknown"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := NewWorkerServer(tt.handler)
			_, err := srv.UnmuteNPC(context.Background(), tt.req)

			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.handler.lastNPCName != tt.req.GetNpcName() {
				t.Errorf("handler.lastNPCName = %q, want %q", tt.handler.lastNPCName, tt.req.GetNpcName())
			}
		})
	}
}

func TestWorkerServer_MuteAllNPCs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		handler   *mockWorkerHandler
		req       *pb.MuteAllNPCsRequest
		wantErr   bool
		wantCount int32
	}{
		{
			name:      "mutes 3 NPCs",
			handler:   &mockWorkerHandler{muteAllCount: 3},
			req:       &pb.MuteAllNPCsRequest{SessionId: "sess-1"},
			wantCount: 3,
		},
		{
			name:    "handler error",
			handler: &mockWorkerHandler{muteAllErr: errors.New("fail")},
			req:     &pb.MuteAllNPCsRequest{SessionId: "sess-2"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := NewWorkerServer(tt.handler)
			resp, err := srv.MuteAllNPCs(context.Background(), tt.req)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.GetCount() != tt.wantCount {
				t.Errorf("count = %d, want %d", resp.GetCount(), tt.wantCount)
			}
		})
	}
}

func TestWorkerServer_UnmuteAllNPCs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		handler   *mockWorkerHandler
		req       *pb.UnmuteAllNPCsRequest
		wantErr   bool
		wantCount int32
	}{
		{
			name:      "unmutes 2 NPCs",
			handler:   &mockWorkerHandler{unmuteAllCnt: 2},
			req:       &pb.UnmuteAllNPCsRequest{SessionId: "sess-1"},
			wantCount: 2,
		},
		{
			name:    "handler error",
			handler: &mockWorkerHandler{unmuteAllErr: errors.New("fail")},
			req:     &pb.UnmuteAllNPCsRequest{SessionId: "sess-2"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := NewWorkerServer(tt.handler)
			resp, err := srv.UnmuteAllNPCs(context.Background(), tt.req)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.GetCount() != tt.wantCount {
				t.Errorf("count = %d, want %d", resp.GetCount(), tt.wantCount)
			}
		})
	}
}

func TestWorkerServer_SpeakNPC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		handler *mockWorkerHandler
		req     *pb.SpeakNPCRequest
		wantErr bool
	}{
		{
			name:    "success",
			handler: &mockWorkerHandler{},
			req:     &pb.SpeakNPCRequest{SessionId: "sess-1", NpcName: "Gandalf", Text: "You shall not pass!"},
		},
		{
			name:    "handler error",
			handler: &mockWorkerHandler{speakErr: errors.New("NPC not found")},
			req:     &pb.SpeakNPCRequest{SessionId: "sess-2", NpcName: "Unknown", Text: "Hello"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := NewWorkerServer(tt.handler)
			_, err := srv.SpeakNPC(context.Background(), tt.req)

			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.handler.lastNPCName != tt.req.GetNpcName() {
				t.Errorf("handler.lastNPCName = %q, want %q", tt.handler.lastNPCName, tt.req.GetNpcName())
			}
			if tt.handler.lastSpeakText != tt.req.GetText() {
				t.Errorf("handler.lastSpeakText = %q, want %q", tt.handler.lastSpeakText, tt.req.GetText())
			}
		})
	}
}

// ── GatewayServer Tests ─────────────────────────────────────────────────────

func TestGatewayServer_ReportState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cb      *mockGatewayCallback
		req     *pb.ReportStateRequest
		wantErr bool
	}{
		{
			name: "active state report",
			cb:   &mockGatewayCallback{},
			req: &pb.ReportStateRequest{
				SessionId: "sess-1",
				State:     pb.SessionState_SESSION_STATE_ACTIVE,
			},
		},
		{
			name: "ended state with error",
			cb:   &mockGatewayCallback{},
			req: &pb.ReportStateRequest{
				SessionId: "sess-2",
				State:     pb.SessionState_SESSION_STATE_ENDED,
				Error:     "timeout",
			},
		},
		{
			name: "callback error",
			cb:   &mockGatewayCallback{reportStateErr: errors.New("store failure")},
			req: &pb.ReportStateRequest{
				SessionId: "sess-3",
				State:     pb.SessionState_SESSION_STATE_ACTIVE,
			},
			wantErr: true,
		},
		{
			name: "unknown state rejected",
			cb:   &mockGatewayCallback{},
			req: &pb.ReportStateRequest{
				SessionId: "sess-4",
				State:     pb.SessionState_SESSION_STATE_UNSPECIFIED,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := NewGatewayServer(tt.cb)
			_, err := srv.ReportState(context.Background(), tt.req)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.cb.lastSessionID != tt.req.GetSessionId() {
				t.Errorf("callback.lastSessionID = %q, want %q", tt.cb.lastSessionID, tt.req.GetSessionId())
			}
		})
	}
}

func TestGatewayServer_Heartbeat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cb      *mockGatewayCallback
		req     *pb.HeartbeatRequest
		wantErr bool
	}{
		{
			name: "success",
			cb:   &mockGatewayCallback{},
			req:  &pb.HeartbeatRequest{SessionId: "sess-1"},
		},
		{
			name:    "callback error",
			cb:      &mockGatewayCallback{heartbeatErr: errors.New("gone")},
			req:     &pb.HeartbeatRequest{SessionId: "sess-2"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := NewGatewayServer(tt.cb)
			_, err := srv.Heartbeat(context.Background(), tt.req)

			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.cb.lastSessionID != tt.req.GetSessionId() {
				t.Errorf("callback.lastSessionID = %q, want %q", tt.cb.lastSessionID, tt.req.GetSessionId())
			}
		})
	}
}
