//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

func fp(v float64) *float64 { return &v }

// TestTenantSpendCapsRoundTrip proves the caps default to NULL, round-trip both
// set, and can be individually cleared back to NULL (#130, ADR-0046). The
// migration applying is implicit — the columns must exist for the queries to run.
func TestTenantSpendCapsRoundTrip(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// Default: a freshly-seeded tenant has neither cap.
	got, err := st.GetTenantSpendCaps(ctx, tenantID)
	if err != nil {
		t.Fatalf("get default caps: %v", err)
	}
	if got.SoftUSD != nil || got.HardUSD != nil {
		t.Fatalf("default caps = %+v, want both nil", got)
	}

	// Set both.
	if err := st.SetTenantSpendCaps(ctx, tenantID, storage.SpendCaps{SoftUSD: fp(5), HardUSD: fp(10)}); err != nil {
		t.Fatalf("set both caps: %v", err)
	}
	got, err = st.GetTenantSpendCaps(ctx, tenantID)
	if err != nil {
		t.Fatalf("get after set both: %v", err)
	}
	if got.SoftUSD == nil || *got.SoftUSD != 5 || got.HardUSD == nil || *got.HardUSD != 10 {
		t.Fatalf("caps after set both = %+v, want soft=5 hard=10", got)
	}

	// Clear soft, keep hard (a nil pointer stores NULL).
	if err := st.SetTenantSpendCaps(ctx, tenantID, storage.SpendCaps{SoftUSD: nil, HardUSD: fp(10)}); err != nil {
		t.Fatalf("clear soft: %v", err)
	}
	got, err = st.GetTenantSpendCaps(ctx, tenantID)
	if err != nil {
		t.Fatalf("get after clear soft: %v", err)
	}
	if got.SoftUSD != nil {
		t.Fatalf("soft after clear = %v, want nil", *got.SoftUSD)
	}
	if got.HardUSD == nil || *got.HardUSD != 10 {
		t.Fatalf("hard after clear soft = %+v, want 10", got.HardUSD)
	}

	// Clear both back to NULL.
	if err := st.SetTenantSpendCaps(ctx, tenantID, storage.SpendCaps{}); err != nil {
		t.Fatalf("clear both: %v", err)
	}
	got, _ = st.GetTenantSpendCaps(ctx, tenantID)
	if got.SoftUSD != nil || got.HardUSD != nil {
		t.Fatalf("caps after clear both = %+v, want both nil", got)
	}
}

// TestTenantSpendCapsUnknownTenant proves a missing tenant is ErrNotFound on both
// get and set — never a silent success.
func TestTenantSpendCapsUnknownTenant(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	if _, err := st.GetTenantSpendCaps(ctx, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("get unknown tenant err = %v, want ErrNotFound", err)
	}
	if err := st.SetTenantSpendCaps(ctx, uuid.New(), storage.SpendCaps{SoftUSD: fp(1)}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("set unknown tenant err = %v, want ErrNotFound", err)
	}
}
