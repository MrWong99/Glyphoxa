package grpctransport

import (
	"context"
	"net"
	"testing"
	"time"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
	"github.com/MrWong99/glyphoxa/internal/gateway"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestPbStateToString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state pb.SessionState
		want  string
	}{
		{
			name:  "pending",
			state: pb.SessionState_SESSION_STATE_PENDING,
			want:  "pending",
		},
		{
			name:  "active",
			state: pb.SessionState_SESSION_STATE_ACTIVE,
			want:  "active",
		},
		{
			name:  "ended",
			state: pb.SessionState_SESSION_STATE_ENDED,
			want:  "ended",
		},
		{
			name:  "unspecified falls through to default",
			state: pb.SessionState_SESSION_STATE_UNSPECIFIED,
			want:  "unknown",
		},
		{
			name:  "unknown numeric value",
			state: pb.SessionState(999),
			want:  "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := pbStateToString(tt.state)
			if got != tt.want {
				t.Errorf("pbStateToString(%v) = %q, want %q", tt.state, got, tt.want)
			}
		})
	}
}

func TestStringToPBState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state gateway.SessionState
		want  pb.SessionState
	}{
		{
			name:  "pending",
			state: gateway.SessionPending,
			want:  pb.SessionState_SESSION_STATE_PENDING,
		},
		{
			name:  "active",
			state: gateway.SessionActive,
			want:  pb.SessionState_SESSION_STATE_ACTIVE,
		},
		{
			name:  "ended",
			state: gateway.SessionEnded,
			want:  pb.SessionState_SESSION_STATE_ENDED,
		},
		{
			name:  "unknown state defaults to unspecified",
			state: gateway.SessionState(42),
			want:  pb.SessionState_SESSION_STATE_UNSPECIFIED,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := stringToPBState(tt.state)
			if got != tt.want {
				t.Errorf("stringToPBState(%v) = %v, want %v", tt.state, got, tt.want)
			}
		})
	}
}

func TestStatusToPB(t *testing.T) {
	t.Parallel()

	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		status gateway.SessionStatus
	}{
		{
			name: "active session without error",
			status: gateway.SessionStatus{
				SessionID: "sess-123",
				State:     gateway.SessionActive,
				StartedAt: now,
				Error:     "",
			},
		},
		{
			name: "ended session with error",
			status: gateway.SessionStatus{
				SessionID: "sess-456",
				State:     gateway.SessionEnded,
				StartedAt: now,
				Error:     "connection lost",
			},
		},
		{
			name: "pending session",
			status: gateway.SessionStatus{
				SessionID: "sess-789",
				State:     gateway.SessionPending,
				StartedAt: now,
				Error:     "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := statusToPB(tt.status)

			if got.GetSessionId() != tt.status.SessionID {
				t.Errorf("SessionId = %q, want %q", got.GetSessionId(), tt.status.SessionID)
			}
			if got.GetState() != stringToPBState(tt.status.State) {
				t.Errorf("State = %v, want %v", got.GetState(), stringToPBState(tt.status.State))
			}
			if got.GetError() != tt.status.Error {
				t.Errorf("Error = %q, want %q", got.GetError(), tt.status.Error)
			}
			if !got.GetStartedAt().AsTime().Equal(tt.status.StartedAt) {
				t.Errorf("StartedAt = %v, want %v", got.GetStartedAt().AsTime(), tt.status.StartedAt)
			}
		})
	}
}

func TestPbStateRoundTrip(t *testing.T) {
	t.Parallel()

	// Verify that converting gateway state -> pb state -> string -> gateway state
	// round-trips correctly for all known states.
	states := []gateway.SessionState{
		gateway.SessionPending,
		gateway.SessionActive,
		gateway.SessionEnded,
	}

	for _, state := range states {
		t.Run(state.String(), func(t *testing.T) {
			t.Parallel()

			pbState := stringToPBState(state)
			str := pbStateToString(pbState)
			roundTripped, ok := gateway.ParseSessionState(str)
			if !ok {
				t.Fatalf("ParseSessionState(%q) failed", str)
			}
			if roundTripped != state {
				t.Errorf("round-trip: got %v, want %v", roundTripped, state)
			}
		})
	}
}

func TestTimestampPB(t *testing.T) {
	t.Parallel()

	now := time.Date(2025, 6, 15, 12, 30, 45, 0, time.UTC)
	st := gateway.SessionStatus{
		SessionID: "test",
		StartedAt: now,
	}

	got := TimestampPB(st)
	if got == nil {
		t.Fatal("expected non-nil timestamp")
	}
	if !got.AsTime().Equal(now) {
		t.Errorf("TimestampPB = %v, want %v", got.AsTime(), now)
	}
}

// ── Client + Server loopback tests ──────────────────────────────────────────

// startLoopbackServer starts a gRPC server with the given handler, creates a
// Client connected to it, and returns the client and a cleanup function.
func startLoopbackServer(t *testing.T, handler WorkerHandler) (*Client, func()) {
	t.Helper()

	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	gs := grpc.NewServer()
	ws := NewWorkerServer(handler)
	ws.Register(gs)

	go func() {
		_ = gs.Serve(lis)
	}()

	client, err := NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		gs.GracefulStop()
		t.Fatalf("NewClient: %v", err)
	}

	return client, func() {
		client.Close()
		gs.GracefulStop()
	}
}

func TestNewClient_DefaultCredentials(t *testing.T) {
	t.Parallel()

	// NewClient with no opts should not fail (it creates a lazy connection).
	client, err := NewClient("localhost:0")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer client.Close()

	if client.conn == nil {
		t.Error("expected non-nil connection")
	}
	if client.breaker == nil {
		t.Error("expected non-nil circuit breaker")
	}
}

func TestClient_StartSession(t *testing.T) {
	t.Parallel()

	handler := &mockWorkerHandler{}
	client, cleanup := startLoopbackServer(t, handler)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	err := client.StartSession(ctx, gateway.StartSessionRequest{
		SessionID:   "sess-client-1",
		TenantID:    "tenant-a",
		CampaignID:  "camp-1",
		GuildID:     "guild-1",
		ChannelID:   "chan-1",
		LicenseTier: "shared",
		BotToken:    "tok-123",
		NPCConfigs: []gateway.NPCConfigMsg{
			{
				Name:           "Gandalf",
				Personality:    "wise wizard",
				Engine:         "cascade",
				VoiceID:        "voice-1",
				KnowledgeScope: []string{"magic", "lore"},
				BudgetTier:     "standard",
				GMHelper:       true,
				AddressOnly:    false,
			},
		},
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	if handler.lastStartReq.SessionID != "sess-client-1" {
		t.Errorf("handler.SessionID = %q, want %q", handler.lastStartReq.SessionID, "sess-client-1")
	}
	if len(handler.lastStartReq.NPCConfigs) != 1 {
		t.Fatalf("handler.NPCConfigs len = %d, want 1", len(handler.lastStartReq.NPCConfigs))
	}
	npc := handler.lastStartReq.NPCConfigs[0]
	if npc.Name != "Gandalf" {
		t.Errorf("NPC Name = %q, want Gandalf", npc.Name)
	}
	if npc.VoiceID != "voice-1" {
		t.Errorf("NPC VoiceID = %q, want voice-1", npc.VoiceID)
	}
	if len(npc.KnowledgeScope) != 2 {
		t.Errorf("NPC KnowledgeScope len = %d, want 2", len(npc.KnowledgeScope))
	}
	if npc.BudgetTier != "standard" {
		t.Errorf("NPC BudgetTier = %q, want standard", npc.BudgetTier)
	}
	if !npc.GMHelper {
		t.Error("NPC GMHelper should be true")
	}
}

func TestClient_StopSession(t *testing.T) {
	t.Parallel()

	handler := &mockWorkerHandler{}
	client, cleanup := startLoopbackServer(t, handler)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	err := client.StopSession(ctx, "sess-stop-1")
	if err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	if handler.lastSessionID != "sess-stop-1" {
		t.Errorf("handler.lastSessionID = %q, want %q", handler.lastSessionID, "sess-stop-1")
	}
}

func TestClient_GetStatus(t *testing.T) {
	t.Parallel()

	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	handler := &mockWorkerHandler{
		statuses: []gateway.SessionStatus{
			{SessionID: "s1", State: gateway.SessionActive, StartedAt: now},
			{SessionID: "s2", State: gateway.SessionPending, StartedAt: now},
		},
	}
	client, cleanup := startLoopbackServer(t, handler)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	statuses, err := client.GetStatus(ctx)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}

	if len(statuses) != 2 {
		t.Fatalf("got %d statuses, want 2", len(statuses))
	}

	// Verify first session.
	if statuses[0].SessionID != "s1" {
		t.Errorf("statuses[0].SessionID = %q, want s1", statuses[0].SessionID)
	}
	if statuses[0].State != gateway.SessionActive {
		t.Errorf("statuses[0].State = %v, want SessionActive", statuses[0].State)
	}
}

func TestClient_ListNPCs(t *testing.T) {
	t.Parallel()

	handler := &mockWorkerHandler{
		npcs: []gateway.NPCStatus{
			{ID: "n1", Name: "Gandalf", Muted: false},
			{ID: "n2", Name: "Sauron", Muted: true},
		},
	}
	client, cleanup := startLoopbackServer(t, handler)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	npcs, err := client.ListNPCs(ctx, "sess-1")
	if err != nil {
		t.Fatalf("ListNPCs: %v", err)
	}

	if len(npcs) != 2 {
		t.Fatalf("got %d NPCs, want 2", len(npcs))
	}
	if npcs[0].Name != "Gandalf" {
		t.Errorf("npcs[0].Name = %q, want Gandalf", npcs[0].Name)
	}
	if npcs[1].Muted != true {
		t.Error("npcs[1].Muted should be true")
	}
}

func TestClient_MuteNPC(t *testing.T) {
	t.Parallel()

	handler := &mockWorkerHandler{}
	client, cleanup := startLoopbackServer(t, handler)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	if err := client.MuteNPC(ctx, "sess-1", "Gandalf"); err != nil {
		t.Fatalf("MuteNPC: %v", err)
	}
	if handler.lastSessionID != "sess-1" {
		t.Errorf("handler.lastSessionID = %q, want sess-1", handler.lastSessionID)
	}
	if handler.lastNPCName != "Gandalf" {
		t.Errorf("handler.lastNPCName = %q, want Gandalf", handler.lastNPCName)
	}
}

func TestClient_UnmuteNPC(t *testing.T) {
	t.Parallel()

	handler := &mockWorkerHandler{}
	client, cleanup := startLoopbackServer(t, handler)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	if err := client.UnmuteNPC(ctx, "sess-1", "Gandalf"); err != nil {
		t.Fatalf("UnmuteNPC: %v", err)
	}
	if handler.lastNPCName != "Gandalf" {
		t.Errorf("handler.lastNPCName = %q, want Gandalf", handler.lastNPCName)
	}
}

func TestClient_MuteAllNPCs(t *testing.T) {
	t.Parallel()

	handler := &mockWorkerHandler{muteAllCount: 3}
	client, cleanup := startLoopbackServer(t, handler)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	count, err := client.MuteAllNPCs(ctx, "sess-1")
	if err != nil {
		t.Fatalf("MuteAllNPCs: %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

func TestClient_UnmuteAllNPCs(t *testing.T) {
	t.Parallel()

	handler := &mockWorkerHandler{unmuteAllCnt: 2}
	client, cleanup := startLoopbackServer(t, handler)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	count, err := client.UnmuteAllNPCs(ctx, "sess-1")
	if err != nil {
		t.Fatalf("UnmuteAllNPCs: %v", err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}

func TestClient_SpeakNPC(t *testing.T) {
	t.Parallel()

	handler := &mockWorkerHandler{}
	client, cleanup := startLoopbackServer(t, handler)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	if err := client.SpeakNPC(ctx, "sess-1", "Gandalf", "You shall not pass!"); err != nil {
		t.Fatalf("SpeakNPC: %v", err)
	}
	if handler.lastNPCName != "Gandalf" {
		t.Errorf("handler.lastNPCName = %q, want Gandalf", handler.lastNPCName)
	}
	if handler.lastSpeakText != "You shall not pass!" {
		t.Errorf("handler.lastSpeakText = %q, want %q", handler.lastSpeakText, "You shall not pass!")
	}
}

func TestClient_Close(t *testing.T) {
	t.Parallel()

	handler := &mockWorkerHandler{}
	client, cleanup := startLoopbackServer(t, handler)
	defer cleanup()

	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ── GatewayClient + GatewayServer loopback tests ────────────────────────────

func startGatewayLoopback(t *testing.T, callback gateway.GatewayCallback) (*GatewayClient, func()) {
	t.Helper()

	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	gs := grpc.NewServer()
	gwSrv := NewGatewayServer(callback)
	gwSrv.Register(gs)

	go func() {
		_ = gs.Serve(lis)
	}()

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		gs.GracefulStop()
		t.Fatalf("grpc.NewClient: %v", err)
	}

	gwClient := NewGatewayClient(conn)

	return gwClient, func() {
		conn.Close()
		gs.GracefulStop()
	}
}

func TestGatewayClient_ReportState(t *testing.T) {
	t.Parallel()

	cb := &mockGatewayCallback{}
	gwClient, cleanup := startGatewayLoopback(t, cb)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	err := gwClient.ReportState(ctx, "sess-gw-1", gateway.SessionActive, "")
	if err != nil {
		t.Fatalf("ReportState: %v", err)
	}

	if cb.lastSessionID != "sess-gw-1" {
		t.Errorf("cb.lastSessionID = %q, want sess-gw-1", cb.lastSessionID)
	}
	if cb.lastState != gateway.SessionActive {
		t.Errorf("cb.lastState = %v, want SessionActive", cb.lastState)
	}
}

func TestGatewayClient_Heartbeat(t *testing.T) {
	t.Parallel()

	cb := &mockGatewayCallback{}
	gwClient, cleanup := startGatewayLoopback(t, cb)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	err := gwClient.Heartbeat(ctx, "sess-gw-hb")
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	if cb.lastSessionID != "sess-gw-hb" {
		t.Errorf("cb.lastSessionID = %q, want sess-gw-hb", cb.lastSessionID)
	}
}

// ── Register tests ──────────────────────────────────────────────────────────

func TestWorkerServer_Register(t *testing.T) {
	t.Parallel()

	handler := &mockWorkerHandler{}
	srv := NewWorkerServer(handler)
	gs := grpc.NewServer()
	// Register should not panic.
	srv.Register(gs)
	gs.Stop()
}

func TestGatewayServer_Register(t *testing.T) {
	t.Parallel()

	cb := &mockGatewayCallback{}
	srv := NewGatewayServer(cb)
	gs := grpc.NewServer()
	// Register should not panic.
	srv.Register(gs)
	gs.Stop()
}
