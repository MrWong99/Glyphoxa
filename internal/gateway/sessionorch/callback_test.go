package sessionorch

import (
	"context"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/internal/gateway"
)

func TestCallbackBridge_ReportState(t *testing.T) {
	t.Parallel()

	orch := NewMemoryOrchestrator()
	cb := NewCallbackBridge(orch)
	ctx := context.Background()

	id, err := orch.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    "t1",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierShared,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := cb.ReportState(ctx, id, gateway.SessionActive, ""); err != nil {
		t.Fatalf("report active: %v", err)
	}

	s, _ := orch.GetSession(ctx, id)
	if s.State != gateway.SessionActive {
		t.Errorf("got state %v, want %v", s.State, gateway.SessionActive)
	}

	if err := cb.ReportState(ctx, id, gateway.SessionEnded, "something broke"); err != nil {
		t.Fatalf("report ended: %v", err)
	}

	s, _ = orch.GetSession(ctx, id)
	if s.State != gateway.SessionEnded {
		t.Errorf("got state %v, want %v", s.State, gateway.SessionEnded)
	}
	if s.Error != "something broke" {
		t.Errorf("got error %q, want %q", s.Error, "something broke")
	}
}

func TestCallbackBridge_ReportState_NotFound(t *testing.T) {
	t.Parallel()

	orch := NewMemoryOrchestrator()
	cb := NewCallbackBridge(orch)
	ctx := context.Background()

	err := cb.ReportState(ctx, "nonexistent-id", gateway.SessionActive, "")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestCallbackBridge_Heartbeat_NotFound(t *testing.T) {
	t.Parallel()

	orch := NewMemoryOrchestrator()
	cb := NewCallbackBridge(orch)
	ctx := context.Background()

	err := cb.Heartbeat(ctx, "nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestCallbackBridge_ReportState_EndedSetsError(t *testing.T) {
	t.Parallel()

	orch := NewMemoryOrchestrator()
	cb := NewCallbackBridge(orch)
	ctx := context.Background()

	id, _ := orch.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    "t1",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierShared,
	})

	if err := cb.ReportState(ctx, id, gateway.SessionEnded, "worker crashed"); err != nil {
		t.Fatalf("report ended: %v", err)
	}

	s, _ := orch.GetSession(ctx, id)
	if s.Error != "worker crashed" {
		t.Errorf("Error = %q, want %q", s.Error, "worker crashed")
	}
	if s.EndedAt == nil {
		t.Error("expected EndedAt to be set")
	}
}

func TestOrchestratorAdapter_ValidateAndCreate(t *testing.T) {
	t.Parallel()

	orch := NewMemoryOrchestrator()
	adapter := NewOrchestratorAdapter(orch)
	ctx := context.Background()

	id, err := adapter.ValidateAndCreate(ctx, "tenant-1", "campaign-1", "guild-1", "channel-1", config.TierDedicated)
	if err != nil {
		t.Fatalf("ValidateAndCreate: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty session ID")
	}
}

func TestOrchestratorAdapter_Transition(t *testing.T) {
	t.Parallel()

	orch := NewMemoryOrchestrator()
	adapter := NewOrchestratorAdapter(orch)
	ctx := context.Background()

	id, _ := adapter.ValidateAndCreate(ctx, "t1", "c1", "g1", "ch1", config.TierShared)

	if err := adapter.Transition(ctx, id, gateway.SessionActive, ""); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	s, _ := orch.GetSession(ctx, id)
	if s.State != gateway.SessionActive {
		t.Errorf("state = %v, want %v", s.State, gateway.SessionActive)
	}
}

func TestOrchestratorAdapter_GetSessionInfo(t *testing.T) {
	t.Parallel()

	orch := NewMemoryOrchestrator()
	adapter := NewOrchestratorAdapter(orch)
	ctx := context.Background()

	id, _ := adapter.ValidateAndCreate(ctx, "t1", "camp-1", "guild-1", "chan-1", config.TierDedicated)

	info, err := adapter.GetSessionInfo(ctx, id)
	if err != nil {
		t.Fatalf("GetSessionInfo: %v", err)
	}
	if info.SessionID != id {
		t.Errorf("SessionID = %q, want %q", info.SessionID, id)
	}
	if info.GuildID != "guild-1" {
		t.Errorf("GuildID = %q, want %q", info.GuildID, "guild-1")
	}
	if info.ChannelID != "chan-1" {
		t.Errorf("ChannelID = %q, want %q", info.ChannelID, "chan-1")
	}
	if info.CampaignName != "camp-1" {
		t.Errorf("CampaignName = %q, want %q", info.CampaignName, "camp-1")
	}
	if info.State != gateway.SessionPending {
		t.Errorf("State = %v, want %v", info.State, gateway.SessionPending)
	}
}

func TestOrchestratorAdapter_GetSessionInfo_NotFound(t *testing.T) {
	t.Parallel()

	orch := NewMemoryOrchestrator()
	adapter := NewOrchestratorAdapter(orch)
	ctx := context.Background()

	_, err := adapter.GetSessionInfo(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestOrchestratorAdapter_ListActiveSessionIDs(t *testing.T) {
	t.Parallel()

	orch := NewMemoryOrchestrator()
	adapter := NewOrchestratorAdapter(orch)
	ctx := context.Background()

	// Create two sessions for the same tenant.
	id1, _ := adapter.ValidateAndCreate(ctx, "t1", "c1", "g1", "ch1", config.TierDedicated)
	id2, _ := adapter.ValidateAndCreate(ctx, "t1", "c2", "g2", "ch2", config.TierDedicated)

	ids, err := adapter.ListActiveSessionIDs(ctx, "t1")
	if err != nil {
		t.Fatalf("ListActiveSessionIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 active session IDs, got %d", len(ids))
	}

	// Check that both IDs are present.
	found := map[string]bool{}
	for _, id := range ids {
		found[id] = true
	}
	if !found[id1] || !found[id2] {
		t.Errorf("expected IDs %q and %q, got %v", id1, id2, ids)
	}
}

func TestOrchestratorAdapter_ListActiveSessionIDs_Empty(t *testing.T) {
	t.Parallel()

	orch := NewMemoryOrchestrator()
	adapter := NewOrchestratorAdapter(orch)
	ctx := context.Background()

	ids, err := adapter.ListActiveSessionIDs(ctx, "no-such-tenant")
	if err != nil {
		t.Fatalf("ListActiveSessionIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected 0 IDs, got %d", len(ids))
	}
}

func TestOrchestratorAdapter_ListActiveSessionIDs_ExcludesEnded(t *testing.T) {
	t.Parallel()

	orch := NewMemoryOrchestrator()
	adapter := NewOrchestratorAdapter(orch)
	ctx := context.Background()

	id1, _ := adapter.ValidateAndCreate(ctx, "t1", "c1", "g1", "ch1", config.TierDedicated)
	_, _ = adapter.ValidateAndCreate(ctx, "t1", "c2", "g2", "ch2", config.TierDedicated)

	// End the first session.
	_ = adapter.Transition(ctx, id1, gateway.SessionEnded, "done")

	ids, err := adapter.ListActiveSessionIDs(ctx, "t1")
	if err != nil {
		t.Fatalf("ListActiveSessionIDs: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("expected 1 active session ID, got %d", len(ids))
	}
}

func TestCallbackBridge_Heartbeat(t *testing.T) {
	t.Parallel()

	orch := NewMemoryOrchestrator()
	cb := NewCallbackBridge(orch)
	ctx := context.Background()

	id, _ := orch.ValidateAndCreate(ctx, SessionRequest{
		TenantID:    "t1",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierShared,
	})

	if err := cb.Heartbeat(ctx, id); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	s, _ := orch.GetSession(ctx, id)
	if s.LastHeartbeat == nil {
		t.Fatal("expected LastHeartbeat to be set")
	}
}
