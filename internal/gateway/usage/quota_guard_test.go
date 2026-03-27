package usage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/internal/gateway"
	"github.com/MrWong99/glyphoxa/internal/gateway/sessionorch"
)

func TestQuotaGuard_AllowsWithinQuota(t *testing.T) {
	t.Parallel()

	orch := sessionorch.NewMemoryOrchestrator()
	usageStore := NewMemoryStore()
	ctx := context.Background()

	// Record 10 hours of usage, quota is 40.
	_ = usageStore.RecordUsage(ctx, "acme", Record{
		Period:       CurrentPeriod(),
		SessionHours: 10,
	})

	guard := NewQuotaGuard(orch, usageStore, func(_ context.Context, _ string) (float64, error) {
		return 40, nil
	})

	id, err := guard.ValidateAndCreate(ctx, sessionorch.SessionRequest{
		TenantID:    "acme",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierShared,
	})
	if err != nil {
		t.Fatalf("ValidateAndCreate: %v", err)
	}
	if id == "" {
		t.Error("expected non-empty session ID")
	}
}

func TestQuotaGuard_BlocksExceededQuota(t *testing.T) {
	t.Parallel()

	orch := sessionorch.NewMemoryOrchestrator()
	usageStore := NewMemoryStore()
	ctx := context.Background()

	// Record 40 hours (at limit), quota is 40.
	_ = usageStore.RecordUsage(ctx, "acme", Record{
		Period:       CurrentPeriod(),
		SessionHours: 40,
	})

	guard := NewQuotaGuard(orch, usageStore, func(_ context.Context, _ string) (float64, error) {
		return 40, nil
	})

	_, err := guard.ValidateAndCreate(ctx, sessionorch.SessionRequest{
		TenantID:    "acme",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierShared,
	})
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Errorf("ValidateAndCreate = %v, want ErrQuotaExceeded", err)
	}
}

func TestQuotaGuard_UnlimitedQuota(t *testing.T) {
	t.Parallel()

	orch := sessionorch.NewMemoryOrchestrator()
	usageStore := NewMemoryStore()
	ctx := context.Background()

	// Huge usage, but unlimited quota (0).
	_ = usageStore.RecordUsage(ctx, "acme", Record{
		Period:       CurrentPeriod(),
		SessionHours: 9999,
	})

	guard := NewQuotaGuard(orch, usageStore, func(_ context.Context, _ string) (float64, error) {
		return 0, nil // unlimited
	})

	_, err := guard.ValidateAndCreate(ctx, sessionorch.SessionRequest{
		TenantID:    "acme",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierDedicated,
	})
	if err != nil {
		t.Fatalf("ValidateAndCreate: %v", err)
	}
}

func TestQuotaGuard_DelegatesOtherMethods(t *testing.T) {
	t.Parallel()

	orch := sessionorch.NewMemoryOrchestrator()
	usageStore := NewMemoryStore()
	ctx := context.Background()

	guard := NewQuotaGuard(orch, usageStore, func(_ context.Context, _ string) (float64, error) {
		return 0, nil
	})

	// Create a session through the guard.
	id, err := guard.ValidateAndCreate(ctx, sessionorch.SessionRequest{
		TenantID:    "acme",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierShared,
	})
	if err != nil {
		t.Fatalf("ValidateAndCreate: %v", err)
	}

	// Verify GetSession works through delegation.
	s, err := guard.GetSession(ctx, id)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if s.TenantID != "acme" {
		t.Errorf("TenantID = %q, want %q", s.TenantID, "acme")
	}

	// Verify ActiveSessions works.
	sessions, err := guard.ActiveSessions(ctx, "acme")
	if err != nil {
		t.Fatalf("ActiveSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("ActiveSessions count = %d, want 1", len(sessions))
	}
}

func TestQuotaGuard_Transition(t *testing.T) {
	t.Parallel()

	orch := sessionorch.NewMemoryOrchestrator()
	usageStore := NewMemoryStore()
	ctx := context.Background()

	guard := NewQuotaGuard(orch, usageStore, func(_ context.Context, _ string) (float64, error) {
		return 0, nil
	})

	id, err := guard.ValidateAndCreate(ctx, sessionorch.SessionRequest{
		TenantID:    "acme",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierShared,
	})
	if err != nil {
		t.Fatalf("ValidateAndCreate: %v", err)
	}

	// Transition to active via the guard.
	if err := guard.Transition(ctx, id, gateway.SessionActive, ""); err != nil {
		t.Fatalf("Transition: %v", err)
	}

	// Verify state changed through inner orchestrator.
	s, err := orch.GetSession(ctx, id)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if s.State != gateway.SessionActive {
		t.Errorf("State = %v, want SessionActive", s.State)
	}
}

func TestQuotaGuard_RecordHeartbeat(t *testing.T) {
	t.Parallel()

	orch := sessionorch.NewMemoryOrchestrator()
	usageStore := NewMemoryStore()
	ctx := context.Background()

	guard := NewQuotaGuard(orch, usageStore, func(_ context.Context, _ string) (float64, error) {
		return 0, nil
	})

	id, err := guard.ValidateAndCreate(ctx, sessionorch.SessionRequest{
		TenantID:    "acme",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierShared,
	})
	if err != nil {
		t.Fatalf("ValidateAndCreate: %v", err)
	}

	if err := guard.RecordHeartbeat(ctx, id); err != nil {
		t.Fatalf("RecordHeartbeat: %v", err)
	}

	// Verify heartbeat was recorded through inner orchestrator.
	s, err := orch.GetSession(ctx, id)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if s.LastHeartbeat == nil {
		t.Error("LastHeartbeat is nil, want non-nil after RecordHeartbeat")
	}
}

func TestQuotaGuard_CleanupZombies(t *testing.T) {
	t.Parallel()

	orch := sessionorch.NewMemoryOrchestrator()
	usageStore := NewMemoryStore()
	ctx := context.Background()

	guard := NewQuotaGuard(orch, usageStore, func(_ context.Context, _ string) (float64, error) {
		return 0, nil
	})

	id, err := guard.ValidateAndCreate(ctx, sessionorch.SessionRequest{
		TenantID:    "acme",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierShared,
	})
	if err != nil {
		t.Fatalf("ValidateAndCreate: %v", err)
	}

	// Transition to active and record a heartbeat.
	if err := guard.Transition(ctx, id, gateway.SessionActive, ""); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if err := guard.RecordHeartbeat(ctx, id); err != nil {
		t.Fatalf("RecordHeartbeat: %v", err)
	}

	// Cleanup with very short timeout — heartbeat just happened, so no zombies.
	cleaned, err := guard.CleanupZombies(ctx, 1*time.Hour)
	if err != nil {
		t.Fatalf("CleanupZombies: %v", err)
	}
	if len(cleaned) != 0 {
		t.Errorf("CleanupZombies = %d, want 0 (heartbeat is recent)", len(cleaned))
	}
}

func TestQuotaGuard_CleanupStalePending(t *testing.T) {
	t.Parallel()

	orch := sessionorch.NewMemoryOrchestrator()
	usageStore := NewMemoryStore()
	ctx := context.Background()

	guard := NewQuotaGuard(orch, usageStore, func(_ context.Context, _ string) (float64, error) {
		return 0, nil
	})

	_, err := guard.ValidateAndCreate(ctx, sessionorch.SessionRequest{
		TenantID:    "acme",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierShared,
	})
	if err != nil {
		t.Fatalf("ValidateAndCreate: %v", err)
	}

	// Session was just created, so a 1h maxAge should not clean it up.
	cleaned, err := guard.CleanupStalePending(ctx, 1*time.Hour)
	if err != nil {
		t.Fatalf("CleanupStalePending: %v", err)
	}
	if len(cleaned) != 0 {
		t.Errorf("CleanupStalePending = %d, want 0 (session is recent)", len(cleaned))
	}
}

func TestQuotaGuard_LookupError(t *testing.T) {
	t.Parallel()

	orch := sessionorch.NewMemoryOrchestrator()
	usageStore := NewMemoryStore()
	ctx := context.Background()

	guard := NewQuotaGuard(orch, usageStore, func(_ context.Context, _ string) (float64, error) {
		return 0, errors.New("tenant not found")
	})

	_, err := guard.ValidateAndCreate(ctx, sessionorch.SessionRequest{
		TenantID:    "bad",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierShared,
	})
	if err == nil {
		t.Fatal("expected error from lookup failure")
	}
}

func TestQuotaGuard_AllNonEndedSessions(t *testing.T) {
	t.Parallel()

	orch := sessionorch.NewMemoryOrchestrator()
	usageStore := NewMemoryStore()
	ctx := context.Background()

	guard := NewQuotaGuard(orch, usageStore, func(_ context.Context, _ string) (float64, error) {
		return 0, nil
	})

	// Create two sessions for different tenants/campaigns.
	_, err := guard.ValidateAndCreate(ctx, sessionorch.SessionRequest{
		TenantID:    "acme",
		CampaignID:  "c1",
		GuildID:     "g1",
		LicenseTier: config.TierDedicated,
	})
	if err != nil {
		t.Fatalf("ValidateAndCreate #1: %v", err)
	}

	_, err = guard.ValidateAndCreate(ctx, sessionorch.SessionRequest{
		TenantID:    "other",
		CampaignID:  "c2",
		GuildID:     "g2",
		LicenseTier: config.TierShared,
	})
	if err != nil {
		t.Fatalf("ValidateAndCreate #2: %v", err)
	}

	sessions, err := guard.AllNonEndedSessions(ctx)
	if err != nil {
		t.Fatalf("AllNonEndedSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("AllNonEndedSessions count = %d, want 2", len(sessions))
	}
}
