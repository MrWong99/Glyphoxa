// Package billingtest holds the fakes tests use to stand in for the
// off-session metering seam's dependencies ([billing.OffSession]): a Usage
// Ledger that records what was written, and the three plan postures a call
// site has to behave correctly under. It exists so every call site's tests
// assert the SAME two things the same way — usage reached the ledger, and an
// exhausted allowance refused before the provider — instead of each package
// growing its own slightly different fake.
package billingtest

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/billing"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Usage is a recording [billing.UsageWriter]: the stand-in for usage_ledger.
// The zero value is ready; it is safe for concurrent use.
type Usage struct {
	mu   sync.Mutex
	rows []storage.UsageRow
}

// AddUsage records the rows (implements [billing.UsageWriter]).
func (u *Usage) AddUsage(_ context.Context, rows []storage.UsageRow) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.rows = append(u.rows, rows...)
	return nil
}

// Rows returns a copy of everything written so far.
func (u *Usage) Rows() []storage.UsageRow {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]storage.UsageRow(nil), u.rows...)
}

// allowance is a fixed [billing.AllowanceChecker].
type allowance struct{ state spend.AllowanceState }

func (a allowance) AllowanceState(context.Context, uuid.UUID) (spend.AllowanceState, error) {
	return a.state, nil
}

// Ungated is a seam with no allowance gate (the `allowlist` Admission Mode
// posture) over a ledger nobody reads — for tests that are not about metering.
func Ungated() *billing.OffSession {
	return billing.NewOffSession(&Usage{}, nil, nil)
}

// Recording is an ungated seam writing to usage.
func Recording(usage *Usage) *billing.OffSession {
	return billing.NewOffSession(usage, nil, nil)
}

// Exhausted is the seam a platform-key Tenant in `open` Admission Mode meets
// once its plan's monthly allowance is spent: every call is refused with
// [billing.ErrAllowanceExhausted].
func Exhausted(usage *Usage) *billing.OffSession {
	included := 5.0
	return billing.NewOffSession(usage, allowance{spend.AllowanceState{IncludedUSD: &included, MonthUSD: 5}}, nil)
}

// BYOK is the seam a BYOK-plan Tenant meets in `open` Admission Mode: the
// gate IS wired, but the plan carries no included allowance (it pays its own
// provider), so nothing is refused however much it has used.
func BYOK(usage *Usage) *billing.OffSession {
	return billing.NewOffSession(usage, allowance{spend.AllowanceState{MonthUSD: 1000}}, nil)
}

// OneRow asserts the ledger holds exactly ONE row, that it belongs to tenant
// and sits under (component, provider, model), and that it carries a priced
// estimate. It fails on an empty model for the llm component: (provider, "")
// misses the price map and bills at the conservative fallback.
func OneRow(t testing.TB, usage *Usage, tenant uuid.UUID, component storage.Component, provider, model string) storage.UsageRow {
	t.Helper()
	rows := usage.Rows()
	if len(rows) != 1 {
		t.Fatalf("usage ledger rows = %d (%+v), want exactly 1", len(rows), rows)
	}
	r := rows[0]
	if r.TenantID != tenant {
		t.Errorf("ledger row tenant = %s, want %s", r.TenantID, tenant)
	}
	if r.Component != component || r.Provider != provider || r.Model != model {
		t.Errorf("ledger row keyed (%s, %s, %q), want (%s, %s, %q)", r.Component, r.Provider, r.Model, component, provider, model)
	}
	if component == storage.ComponentLLM && r.Model == "" {
		t.Error("ledger row has an empty model: it would be priced at the conservative fallback")
	}
	if r.EstimatedUSD <= 0 {
		t.Errorf("ledger row estimate = %v, want a priced amount", r.EstimatedUSD)
	}
	return r
}
