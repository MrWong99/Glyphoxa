package spend

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Monthly plan-allowance gate (ADR-0054 seam (b), ADR-0055): compares a
// tenant's month-to-date estimated usage against the ACTIVE plan's
// included_usage_usd. The Usage Ledger itself stays attribution-only ("never a
// gate", ADR-0054) — this is the separate spend-meter-style mechanism that
// READS it. The decision points live in session.Manager.Start (refuse a start
// on an exhausted allowance; tighten the session's hard cap to the remainder),
// exactly the ADR-0046 cap mechanics. Off-session spend (Recap /
// Highlight-enrich) is not yet tenant-attributed, so the gate has ADR-0054's
// documented undercount — accepted, not fixed here.

// AllowanceReader is the two storage reads behind the gate. *storage.Store
// satisfies it.
type AllowanceReader interface {
	// TenantIncludedUsageUSD is the active plan's monthly allowance, joined
	// live from the plan row; nil = no allowance configured (no gate).
	TenantIncludedUsageUSD(ctx context.Context, tenantID uuid.UUID) (*float64, error)
	// TenantMonthUsageUSD sums the ledger's estimated USD over [from, to).
	TenantMonthUsageUSD(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (float64, error)
}

// AllowanceState is a Start-time snapshot of a tenant's monthly allowance.
// Snapshot semantics mirror the ADR-0046 caps: computed once per session
// start; the running session's own spend is bounded by the meter, not by
// re-reads.
type AllowanceState struct {
	// IncludedUSD is the plan's monthly allowance; nil = no gate applies.
	IncludedUSD *float64
	// MonthUSD is the flushed ledger's month-to-date estimate at Start. The
	// running session is never in the ledger (it flushes at loop exit), which
	// is exactly right: MTD-at-start + the session meter covers the month.
	MonthUSD float64
}

// RemainingUSD is the allowance still spendable this month: nil when no
// allowance is configured, floored at zero otherwise.
func (s AllowanceState) RemainingUSD() *float64 {
	if s.IncludedUSD == nil {
		return nil
	}
	r := max(*s.IncludedUSD-s.MonthUSD, 0)
	return &r
}

// Exhausted reports a configured allowance fully spent.
func (s AllowanceState) Exhausted() bool {
	return s.IncludedUSD != nil && s.MonthUSD >= *s.IncludedUSD
}

// PlanAllowance reads a tenant's [AllowanceState] over the current UTC
// calendar month — [first-of-month, first-of-next-month), the BillingReport
// window convention. Ledger days are UTC (internal/billing).
type PlanAllowance struct {
	Reader AllowanceReader
	// Now is the clock; nil = time.Now. Injected for tests.
	Now func() time.Time
}

// AllowanceState loads the plan allowance and the month-to-date ledger sum.
// A tenant with no allowance skips the ledger read entirely — the common
// (BYOK / self-host) case costs one query.
func (p PlanAllowance) AllowanceState(ctx context.Context, tenantID uuid.UUID) (AllowanceState, error) {
	included, err := p.Reader.TenantIncludedUsageUSD(ctx, tenantID)
	if err != nil {
		return AllowanceState{}, fmt.Errorf("spend: plan allowance for tenant %s: %w", tenantID, err)
	}
	if included == nil {
		return AllowanceState{}, nil
	}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	t := now().UTC()
	from := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	mtd, err := p.Reader.TenantMonthUsageUSD(ctx, tenantID, from, to)
	if err != nil {
		return AllowanceState{}, fmt.Errorf("spend: month-to-date usage for tenant %s: %w", tenantID, err)
	}
	return AllowanceState{IncludedUSD: included, MonthUSD: mtd}, nil
}
