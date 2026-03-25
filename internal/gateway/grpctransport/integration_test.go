//go:build integration

package grpctransport_test

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	pb "github.com/MrWong99/glyphoxa/gen/glyphoxa/v1"
	"github.com/MrWong99/glyphoxa/internal/gateway"
	"github.com/MrWong99/glyphoxa/internal/gateway/grpctransport"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// stubWorkerHandler is a minimal WorkerHandler that records calls for
// verification.
type stubWorkerHandler struct {
	mu       sync.Mutex
	started  []gateway.StartSessionRequest
	stopped  []string
	npcs     map[string][]gateway.NPCStatus
	muted    map[string]bool
	speakLog []speakEntry
}

type speakEntry struct {
	SessionID string
	NPCName   string
	Text      string
}

func newStubWorkerHandler() *stubWorkerHandler {
	return &stubWorkerHandler{
		npcs:  make(map[string][]gateway.NPCStatus),
		muted: make(map[string]bool),
	}
}

func (h *stubWorkerHandler) StartSession(_ context.Context, req gateway.StartSessionRequest) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.started = append(h.started, req)
	h.npcs[req.SessionID] = make([]gateway.NPCStatus, len(req.NPCConfigs))
	for i, nc := range req.NPCConfigs {
		h.npcs[req.SessionID][i] = gateway.NPCStatus{
			ID:   fmt.Sprintf("npc-%d", i),
			Name: nc.Name,
		}
	}
	return nil
}

func (h *stubWorkerHandler) StopSession(_ context.Context, sessionID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopped = append(h.stopped, sessionID)
	return nil
}

func (h *stubWorkerHandler) GetStatus(_ context.Context) ([]gateway.SessionStatus, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var statuses []gateway.SessionStatus
	for _, req := range h.started {
		statuses = append(statuses, gateway.SessionStatus{
			SessionID: req.SessionID,
			State:     gateway.SessionActive,
			StartedAt: time.Now(),
		})
	}
	return statuses, nil
}

func (h *stubWorkerHandler) ListNPCs(_ context.Context, sessionID string) ([]gateway.NPCStatus, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.npcs[sessionID], nil
}

func (h *stubWorkerHandler) MuteNPC(_ context.Context, sessionID, npcName string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := sessionID + "/" + npcName
	h.muted[key] = true
	return nil
}

func (h *stubWorkerHandler) UnmuteNPC(_ context.Context, sessionID, npcName string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := sessionID + "/" + npcName
	delete(h.muted, key)
	return nil
}

func (h *stubWorkerHandler) MuteAllNPCs(_ context.Context, sessionID string) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for _, n := range h.npcs[sessionID] {
		key := sessionID + "/" + n.Name
		h.muted[key] = true
		count++
	}
	return count, nil
}

func (h *stubWorkerHandler) UnmuteAllNPCs(_ context.Context, sessionID string) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for _, n := range h.npcs[sessionID] {
		key := sessionID + "/" + n.Name
		if h.muted[key] {
			delete(h.muted, key)
			count++
		}
	}
	return count, nil
}

func (h *stubWorkerHandler) SpeakNPC(_ context.Context, sessionID, npcName, text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.speakLog = append(h.speakLog, speakEntry{sessionID, npcName, text})
	return nil
}

// stubGatewayCallback records gateway callbacks for verification.
type stubGatewayCallback struct {
	mu         sync.Mutex
	states     []stateReport
	heartbeats []string
}

type stateReport struct {
	SessionID string
	State     gateway.SessionState
	Error     string
}

func (g *stubGatewayCallback) ReportState(_ context.Context, sessionID string, state gateway.SessionState, errMsg string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.states = append(g.states, stateReport{sessionID, state, errMsg})
	return nil
}

func (g *stubGatewayCallback) Heartbeat(_ context.Context, sessionID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.heartbeats = append(g.heartbeats, sessionID)
	return nil
}

// startWorkerServer creates a gRPC server, registers the worker service, and
// starts listening on a random port. Returns the client and a cleanup function.
func startWorkerServer(t *testing.T, handler grpctransport.WorkerHandler) (*grpctransport.Client, func()) {
	t.Helper()

	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	gs := grpc.NewServer()
	ws := grpctransport.NewWorkerServer(handler)
	ws.Register(gs)

	go func() {
		if err := gs.Serve(lis); err != nil {
			// Expected on graceful stop.
		}
	}()

	client, err := grpctransport.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		gs.GracefulStop()
		t.Fatalf("new client: %v", err)
	}

	return client, func() {
		client.Close()
		gs.GracefulStop()
	}
}

// startGatewayServer creates a gRPC server with the gateway service and
// returns a GatewayClient connected to it.
func startGatewayServer(t *testing.T, callback gateway.GatewayCallback) (*grpctransport.GatewayClient, func()) {
	t.Helper()

	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	gs := grpc.NewServer()
	gwSrv := grpctransport.NewGatewayServer(callback)
	gwSrv.Register(gs)

	go func() {
		if err := gs.Serve(lis); err != nil {
			// Expected on graceful stop.
		}
	}()

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		gs.GracefulStop()
		t.Fatalf("new gateway client: %v", err)
	}

	gwClient := grpctransport.NewGatewayClient(conn)

	return gwClient, func() {
		conn.Close()
		gs.GracefulStop()
	}
}

// TestIntegration_GRPCWorkerSessionLifecycle tests the full session lifecycle
// over a real gRPC connection: start → get status → list NPCs → mute/unmute →
// speak → stop. Subtests run sequentially because they share the gRPC connection.
func TestIntegration_GRPCWorkerSessionLifecycle(t *testing.T) {
	t.Parallel()

	handler := newStubWorkerHandler()
	client, cleanup := startWorkerServer(t, handler)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	sessionID := "test-session-001"
	req := gateway.StartSessionRequest{
		SessionID:   sessionID,
		TenantID:    "tenant-a",
		CampaignID:  "campaign-x",
		GuildID:     "guild-1",
		ChannelID:   "channel-1",
		LicenseTier: "dedicated",
		NPCConfigs: []gateway.NPCConfigMsg{
			{Name: "Grimjaw", Personality: "A gruff dwarf", Engine: "cascaded", VoiceID: "v1"},
			{Name: "Elara", Personality: "A wise elf", Engine: "cascaded", VoiceID: "v2"},
		},
		BotToken: "test-bot-token",
	}

	// Sequential subtests — they share the same gRPC connection and handler state.
	t.Run("start session over gRPC", func(t *testing.T) {
		err := client.StartSession(ctx, req)
		if err != nil {
			t.Fatalf("StartSession: %v", err)
		}

		handler.mu.Lock()
		defer handler.mu.Unlock()
		if len(handler.started) != 1 {
			t.Fatalf("expected 1 started session, got %d", len(handler.started))
		}
		if handler.started[0].SessionID != sessionID {
			t.Errorf("session ID = %q, want %q", handler.started[0].SessionID, sessionID)
		}
		if len(handler.started[0].NPCConfigs) != 2 {
			t.Errorf("NPC configs = %d, want 2", len(handler.started[0].NPCConfigs))
		}
	})

	t.Run("get status returns active session", func(t *testing.T) {
		statuses, err := client.GetStatus(ctx)
		if err != nil {
			t.Fatalf("GetStatus: %v", err)
		}
		if len(statuses) == 0 {
			t.Fatal("expected at least 1 status")
		}
		found := false
		for _, s := range statuses {
			if s.SessionID == sessionID {
				found = true
				if s.State != gateway.SessionActive {
					t.Errorf("state = %v, want SessionActive", s.State)
				}
			}
		}
		if !found {
			t.Error("session not found in status list")
		}
	})

	t.Run("list NPCs returns configured NPCs", func(t *testing.T) {
		npcs, err := client.ListNPCs(ctx, sessionID)
		if err != nil {
			t.Fatalf("ListNPCs: %v", err)
		}
		if len(npcs) != 2 {
			t.Fatalf("expected 2 NPCs, got %d", len(npcs))
		}
		names := map[string]bool{}
		for _, n := range npcs {
			names[n.Name] = true
		}
		if !names["Grimjaw"] || !names["Elara"] {
			t.Errorf("expected Grimjaw and Elara, got %v", names)
		}
	})

	t.Run("mute and unmute NPC", func(t *testing.T) {
		if err := client.MuteNPC(ctx, sessionID, "Grimjaw"); err != nil {
			t.Fatalf("MuteNPC: %v", err)
		}
		if err := client.UnmuteNPC(ctx, sessionID, "Grimjaw"); err != nil {
			t.Fatalf("UnmuteNPC: %v", err)
		}
	})

	t.Run("mute all and unmute all NPCs", func(t *testing.T) {
		count, err := client.MuteAllNPCs(ctx, sessionID)
		if err != nil {
			t.Fatalf("MuteAllNPCs: %v", err)
		}
		if count != 2 {
			t.Errorf("muted count = %d, want 2", count)
		}

		count, err = client.UnmuteAllNPCs(ctx, sessionID)
		if err != nil {
			t.Fatalf("UnmuteAllNPCs: %v", err)
		}
		if count != 2 {
			t.Errorf("unmuted count = %d, want 2", count)
		}
	})

	t.Run("speak NPC delivers text", func(t *testing.T) {
		if err := client.SpeakNPC(ctx, sessionID, "Elara", "Greetings, adventurer!"); err != nil {
			t.Fatalf("SpeakNPC: %v", err)
		}

		handler.mu.Lock()
		defer handler.mu.Unlock()
		found := false
		for _, e := range handler.speakLog {
			if e.SessionID == sessionID && e.NPCName == "Elara" && e.Text == "Greetings, adventurer!" {
				found = true
			}
		}
		if !found {
			t.Error("speak entry not recorded")
		}
	})

	t.Run("stop session", func(t *testing.T) {
		if err := client.StopSession(ctx, sessionID); err != nil {
			t.Fatalf("StopSession: %v", err)
		}

		handler.mu.Lock()
		defer handler.mu.Unlock()
		found := false
		for _, id := range handler.stopped {
			if id == sessionID {
				found = true
			}
		}
		if !found {
			t.Error("session not recorded as stopped")
		}
	})
}

// TestIntegration_GRPCGatewayCallbacks tests worker→gateway callbacks
// (ReportState and Heartbeat) over a real gRPC connection. Sequential
// subtests sharing the gRPC connection.
func TestIntegration_GRPCGatewayCallbacks(t *testing.T) {
	t.Parallel()

	callback := &stubGatewayCallback{}
	gwClient, cleanup := startGatewayServer(t, callback)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	sessionID := "test-session-gw-001"

	t.Run("report pending state", func(t *testing.T) {
		err := gwClient.ReportState(ctx, sessionID, gateway.SessionPending, "")
		if err != nil {
			t.Fatalf("ReportState(pending): %v", err)
		}
	})

	t.Run("report active state", func(t *testing.T) {
		err := gwClient.ReportState(ctx, sessionID, gateway.SessionActive, "")
		if err != nil {
			t.Fatalf("ReportState(active): %v", err)
		}
	})

	t.Run("report ended state with error", func(t *testing.T) {
		err := gwClient.ReportState(ctx, sessionID, gateway.SessionEnded, "pipeline crashed")
		if err != nil {
			t.Fatalf("ReportState(ended): %v", err)
		}
	})

	t.Run("heartbeat", func(t *testing.T) {
		err := gwClient.Heartbeat(ctx, sessionID)
		if err != nil {
			t.Fatalf("Heartbeat: %v", err)
		}
	})

	// Verify all callbacks were received.
	callback.mu.Lock()
	defer callback.mu.Unlock()
	if len(callback.states) != 3 {
		t.Errorf("expected 3 state reports, got %d", len(callback.states))
	}
	if len(callback.heartbeats) != 1 {
		t.Errorf("expected 1 heartbeat, got %d", len(callback.heartbeats))
	}
}

// TestIntegration_GRPCBidirectionalCommunication tests both directions of
// communication simultaneously: gateway→worker (WorkerClient) and
// worker→gateway (GatewayCallback) using two separate gRPC servers.
func TestIntegration_GRPCBidirectionalCommunication(t *testing.T) {
	t.Parallel()

	handler := newStubWorkerHandler()
	callback := &stubGatewayCallback{}

	// Start worker server (gateway→worker).
	workerClient, cleanupWorker := startWorkerServer(t, handler)
	t.Cleanup(cleanupWorker)

	// Start gateway server (worker→gateway).
	gwClient, cleanupGW := startGatewayServer(t, callback)
	t.Cleanup(cleanupGW)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	sessionID := "bidir-session-001"

	// Gateway starts session on worker.
	err := workerClient.StartSession(ctx, gateway.StartSessionRequest{
		SessionID:   sessionID,
		TenantID:    "tenant-bidir",
		CampaignID:  "campaign-bidir",
		GuildID:     "guild-bidir",
		ChannelID:   "channel-bidir",
		LicenseTier: "shared",
		NPCConfigs:  []gateway.NPCConfigMsg{{Name: "TestNPC", Engine: "cascaded"}},
	})
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	// Worker reports active state to gateway.
	err = gwClient.ReportState(ctx, sessionID, gateway.SessionActive, "")
	if err != nil {
		t.Fatalf("ReportState: %v", err)
	}

	// Worker sends heartbeats.
	for range 3 {
		if err := gwClient.Heartbeat(ctx, sessionID); err != nil {
			t.Fatalf("Heartbeat: %v", err)
		}
	}

	// Gateway stops session.
	err = workerClient.StopSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("StopSession: %v", err)
	}

	// Worker reports ended state.
	err = gwClient.ReportState(ctx, sessionID, gateway.SessionEnded, "")
	if err != nil {
		t.Fatalf("ReportState(ended): %v", err)
	}

	// Verify: worker received the session.
	handler.mu.Lock()
	if len(handler.started) != 1 || handler.started[0].SessionID != sessionID {
		t.Errorf("worker didn't receive expected start request")
	}
	if len(handler.stopped) != 1 || handler.stopped[0] != sessionID {
		t.Errorf("worker didn't receive expected stop request")
	}
	handler.mu.Unlock()

	// Verify: gateway received the callbacks.
	callback.mu.Lock()
	if len(callback.states) != 2 {
		t.Errorf("expected 2 state reports, got %d", len(callback.states))
	}
	if len(callback.heartbeats) != 3 {
		t.Errorf("expected 3 heartbeats, got %d", len(callback.heartbeats))
	}
	callback.mu.Unlock()
}

// TestIntegration_GRPCNPCConfigSerialization verifies that NPC configuration
// fields survive gRPC serialisation/deserialisation intact, including nested
// fields like KnowledgeScope and BudgetTier.
func TestIntegration_GRPCNPCConfigSerialization(t *testing.T) {
	t.Parallel()

	handler := newStubWorkerHandler()
	client, cleanup := startWorkerServer(t, handler)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	req := gateway.StartSessionRequest{
		SessionID:   "serialization-test",
		TenantID:    "tenant-s",
		CampaignID:  "campaign-s",
		GuildID:     "guild-s",
		ChannelID:   "ch-s",
		LicenseTier: "dedicated",
		NPCConfigs: []gateway.NPCConfigMsg{
			{
				Name:           "Heinrich",
				Personality:    "A gruff dwarven blacksmith who speaks in short sentences",
				Engine:         "cascaded",
				VoiceID:        "voice-abc123",
				KnowledgeScope: []string{"blacksmithing", "local_gossip", "dwarven_history"},
				BudgetTier:     "standard",
				GMHelper:       false,
				AddressOnly:    true,
			},
		},
		BotToken: "bot-token-xyz",
	}

	if err := client.StartSession(ctx, req); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()

	if len(handler.started) != 1 {
		t.Fatalf("expected 1 started session, got %d", len(handler.started))
	}
	got := handler.started[0]

	if got.TenantID != "tenant-s" {
		t.Errorf("TenantID = %q, want %q", got.TenantID, "tenant-s")
	}
	if got.BotToken != "bot-token-xyz" {
		t.Errorf("BotToken = %q, want %q", got.BotToken, "bot-token-xyz")
	}
	if len(got.NPCConfigs) != 1 {
		t.Fatalf("NPCConfigs = %d, want 1", len(got.NPCConfigs))
	}

	npc := got.NPCConfigs[0]
	if npc.Name != "Heinrich" {
		t.Errorf("NPC Name = %q, want %q", npc.Name, "Heinrich")
	}
	if npc.Engine != "cascaded" {
		t.Errorf("NPC Engine = %q, want %q", npc.Engine, "cascaded")
	}
	if npc.VoiceID != "voice-abc123" {
		t.Errorf("NPC VoiceID = %q, want %q", npc.VoiceID, "voice-abc123")
	}
	if len(npc.KnowledgeScope) != 3 {
		t.Errorf("KnowledgeScope len = %d, want 3", len(npc.KnowledgeScope))
	}
	if npc.BudgetTier != "standard" {
		t.Errorf("BudgetTier = %q, want %q", npc.BudgetTier, "standard")
	}
	if npc.AddressOnly != true {
		t.Error("AddressOnly should be true")
	}
}

// TestIntegration_GRPCProtobufStateConversion verifies the protobuf
// SessionState enum round-trips correctly through gRPC for all valid states.
func TestIntegration_GRPCProtobufStateConversion(t *testing.T) {
	t.Parallel()

	callback := &stubGatewayCallback{}
	gwClient, cleanup := startGatewayServer(t, callback)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	states := []gateway.SessionState{
		gateway.SessionPending,
		gateway.SessionActive,
		gateway.SessionEnded,
	}

	for _, state := range states {
		sid := fmt.Sprintf("state-test-%s", state)
		err := gwClient.ReportState(ctx, sid, state, "")
		if err != nil {
			t.Fatalf("ReportState(%v): %v", state, err)
		}
	}

	callback.mu.Lock()
	defer callback.mu.Unlock()

	if len(callback.states) != 3 {
		t.Fatalf("expected 3 state reports, got %d", len(callback.states))
	}

	for i, want := range states {
		got := callback.states[i]
		if got.State != want {
			t.Errorf("state[%d] = %v, want %v", i, got.State, want)
		}
	}
}

// Ensure stubWorkerHandler implements WorkerHandler at compile time.
var _ grpctransport.WorkerHandler = (*stubWorkerHandler)(nil)

// Ensure we can check that the generated gRPC interface is present.
var _ pb.SessionWorkerServiceServer = (*grpctransport.WorkerServer)(nil)
