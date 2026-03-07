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

	if err := cb.ReportState(ctx, id, gateway.SessionActive); err != nil {
		t.Fatalf("report active: %v", err)
	}

	s, _ := orch.GetSession(ctx, id)
	if s.State != gateway.SessionActive {
		t.Errorf("got state %v, want %v", s.State, gateway.SessionActive)
	}

	if err := cb.ReportState(ctx, id, gateway.SessionEnded); err != nil {
		t.Fatalf("report ended: %v", err)
	}

	s, _ = orch.GetSession(ctx, id)
	if s.State != gateway.SessionEnded {
		t.Errorf("got state %v, want %v", s.State, gateway.SessionEnded)
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
