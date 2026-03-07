package usage

import (
	"context"
	"errors"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/config"
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
