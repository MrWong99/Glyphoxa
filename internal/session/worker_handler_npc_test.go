package session

import (
	"context"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/agent"
	agentmock "github.com/MrWong99/glyphoxa/internal/agent/mock"
	"github.com/MrWong99/glyphoxa/internal/agent/orchestrator"
	"github.com/MrWong99/glyphoxa/internal/gateway"
)

// newTestHandlerWithNPCs creates a WorkerHandler with a session containing mock NPCs.
func newTestHandlerWithNPCs(t *testing.T, sessionID string, agents ...agent.NPCAgent) *WorkerHandler {
	t.Helper()

	orch := orchestrator.New(agents)
	rt := NewRuntime(RuntimeConfig{
		SessionID:    sessionID,
		Agents:       agents,
		Orchestrator: orch,
	})

	handler := NewWorkerHandler(
		func(_ context.Context, req gateway.StartSessionRequest) (*Runtime, error) {
			return rt, nil
		},
		nil,
	)

	if err := handler.StartSession(context.Background(), gateway.StartSessionRequest{SessionID: sessionID}); err != nil {
		t.Fatalf("start session: %v", err)
	}
	return handler
}

func TestWorkerHandler_ListNPCs(t *testing.T) {
	t.Parallel()

	npc1 := &agentmock.NPCAgent{IDResult: "npc-1", NameResult: "Greymantle"}
	npc2 := &agentmock.NPCAgent{IDResult: "npc-2", NameResult: "Thorn"}
	handler := newTestHandlerWithNPCs(t, "s1", npc1, npc2)
	defer handler.StopAll(context.Background())

	npcs, err := handler.ListNPCs(context.Background(), "s1")
	if err != nil {
		t.Fatalf("ListNPCs: %v", err)
	}
	if len(npcs) != 2 {
		t.Fatalf("got %d npcs, want 2", len(npcs))
	}

	names := make(map[string]bool)
	for _, n := range npcs {
		names[n.Name] = true
		if n.Muted {
			t.Errorf("NPC %q should not be muted initially", n.Name)
		}
	}
	if !names["Greymantle"] || !names["Thorn"] {
		t.Errorf("expected Greymantle and Thorn, got %v", names)
	}
}

func TestWorkerHandler_ListNPCs_NotFound(t *testing.T) {
	t.Parallel()

	handler := NewWorkerHandler(
		func(_ context.Context, req gateway.StartSessionRequest) (*Runtime, error) {
			return NewRuntime(RuntimeConfig{SessionID: req.SessionID}), nil
		},
		nil,
	)

	_, err := handler.ListNPCs(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown session")
	}
}

func TestWorkerHandler_MuteUnmuteNPC(t *testing.T) {
	t.Parallel()

	npc := &agentmock.NPCAgent{IDResult: "npc-1", NameResult: "Greymantle"}
	handler := newTestHandlerWithNPCs(t, "s1", npc)
	defer handler.StopAll(context.Background())

	ctx := context.Background()

	// Mute.
	if err := handler.MuteNPC(ctx, "s1", "Greymantle"); err != nil {
		t.Fatalf("MuteNPC: %v", err)
	}

	npcs, _ := handler.ListNPCs(ctx, "s1")
	if len(npcs) != 1 || !npcs[0].Muted {
		t.Error("expected NPC to be muted after MuteNPC")
	}

	// Unmute.
	if err := handler.UnmuteNPC(ctx, "s1", "Greymantle"); err != nil {
		t.Fatalf("UnmuteNPC: %v", err)
	}

	npcs, _ = handler.ListNPCs(ctx, "s1")
	if len(npcs) != 1 || npcs[0].Muted {
		t.Error("expected NPC to be unmuted after UnmuteNPC")
	}
}

func TestWorkerHandler_MuteNPC_NotFound(t *testing.T) {
	t.Parallel()

	npc := &agentmock.NPCAgent{IDResult: "npc-1", NameResult: "Greymantle"}
	handler := newTestHandlerWithNPCs(t, "s1", npc)
	defer handler.StopAll(context.Background())

	if err := handler.MuteNPC(context.Background(), "s1", "Nonexistent"); err == nil {
		t.Fatal("expected error for unknown NPC name")
	}
}

func TestWorkerHandler_MuteAllUnmuteAll(t *testing.T) {
	t.Parallel()

	npc1 := &agentmock.NPCAgent{IDResult: "npc-1", NameResult: "Greymantle"}
	npc2 := &agentmock.NPCAgent{IDResult: "npc-2", NameResult: "Thorn"}
	handler := newTestHandlerWithNPCs(t, "s1", npc1, npc2)
	defer handler.StopAll(context.Background())

	ctx := context.Background()

	// Mute all.
	count, err := handler.MuteAllNPCs(ctx, "s1")
	if err != nil {
		t.Fatalf("MuteAllNPCs: %v", err)
	}
	if count != 2 {
		t.Errorf("MuteAllNPCs returned %d, want 2", count)
	}

	npcs, _ := handler.ListNPCs(ctx, "s1")
	for _, n := range npcs {
		if !n.Muted {
			t.Errorf("NPC %q should be muted after MuteAll", n.Name)
		}
	}

	// Unmute all.
	count, err = handler.UnmuteAllNPCs(ctx, "s1")
	if err != nil {
		t.Fatalf("UnmuteAllNPCs: %v", err)
	}
	if count != 2 {
		t.Errorf("UnmuteAllNPCs returned %d, want 2", count)
	}

	npcs, _ = handler.ListNPCs(ctx, "s1")
	for _, n := range npcs {
		if n.Muted {
			t.Errorf("NPC %q should be unmuted after UnmuteAll", n.Name)
		}
	}
}

func TestWorkerHandler_SpeakNPC(t *testing.T) {
	t.Parallel()

	npc := &agentmock.NPCAgent{IDResult: "npc-1", NameResult: "Greymantle"}
	handler := newTestHandlerWithNPCs(t, "s1", npc)
	defer handler.StopAll(context.Background())

	ctx := context.Background()

	if err := handler.SpeakNPC(ctx, "s1", "Greymantle", "Hello, traveler!"); err != nil {
		t.Fatalf("SpeakNPC: %v", err)
	}

	// SpeakNPC is synchronous so SpeakTextCalls is safe to read after return.
	if len(npc.SpeakTextCalls) != 1 || npc.SpeakTextCalls[0] != "Hello, traveler!" {
		t.Errorf("expected SpeakText to be called with %q, got %v", "Hello, traveler!", npc.SpeakTextCalls)
	}
}

func TestWorkerHandler_SpeakNPC_NotFound(t *testing.T) {
	t.Parallel()

	npc := &agentmock.NPCAgent{IDResult: "npc-1", NameResult: "Greymantle"}
	handler := newTestHandlerWithNPCs(t, "s1", npc)
	defer handler.StopAll(context.Background())

	if err := handler.SpeakNPC(context.Background(), "s1", "Nonexistent", "test"); err == nil {
		t.Fatal("expected error for unknown NPC name")
	}
}

func TestWorkerHandler_NPCOps_SessionNotFound(t *testing.T) {
	t.Parallel()

	handler := NewWorkerHandler(
		func(_ context.Context, req gateway.StartSessionRequest) (*Runtime, error) {
			return NewRuntime(RuntimeConfig{SessionID: req.SessionID}), nil
		},
		nil,
	)

	ctx := context.Background()

	if err := handler.MuteNPC(ctx, "bad", "test"); err == nil {
		t.Error("MuteNPC: expected error for unknown session")
	}
	if err := handler.UnmuteNPC(ctx, "bad", "test"); err == nil {
		t.Error("UnmuteNPC: expected error for unknown session")
	}
	if _, err := handler.MuteAllNPCs(ctx, "bad"); err == nil {
		t.Error("MuteAllNPCs: expected error for unknown session")
	}
	if _, err := handler.UnmuteAllNPCs(ctx, "bad"); err == nil {
		t.Error("UnmuteAllNPCs: expected error for unknown session")
	}
	if err := handler.SpeakNPC(ctx, "bad", "test", "text"); err == nil {
		t.Error("SpeakNPC: expected error for unknown session")
	}
}
