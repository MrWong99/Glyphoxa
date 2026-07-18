package spend

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeAllowanceReader scripts the two storage reads and records the month
// window it was asked for.
type fakeAllowanceReader struct {
	included    *float64
	includedErr error
	mtd         float64
	mtdErr      error
	gotFrom     time.Time
	gotTo       time.Time
	mtdCalls    int
}

func (f *fakeAllowanceReader) TenantIncludedUsageUSD(context.Context, uuid.UUID) (*float64, error) {
	return f.included, f.includedErr
}

func (f *fakeAllowanceReader) TenantMonthUsageUSD(_ context.Context, _ uuid.UUID, from, to time.Time) (float64, error) {
	f.mtdCalls++
	f.gotFrom, f.gotTo = from, to
	return f.mtd, f.mtdErr
}

func fptr(v float64) *float64 { return &v }

func TestAllowanceStateMath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		state         AllowanceState
		wantRemaining *float64
		wantExhausted bool
	}{
		{"no allowance -> no gate", AllowanceState{MonthUSD: 99}, nil, false},
		{"untouched allowance", AllowanceState{IncludedUSD: fptr(15), MonthUSD: 0}, fptr(15), false},
		{"partially spent", AllowanceState{IncludedUSD: fptr(15), MonthUSD: 14.5}, fptr(0.5), false},
		{"exactly spent", AllowanceState{IncludedUSD: fptr(15), MonthUSD: 15}, fptr(0), true},
		{"overspent floors at zero", AllowanceState{IncludedUSD: fptr(15), MonthUSD: 22}, fptr(0), true},
		{"zero allowance is always exhausted", AllowanceState{IncludedUSD: fptr(0), MonthUSD: 0}, fptr(0), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := c.state.RemainingUSD()
			switch {
			case (got == nil) != (c.wantRemaining == nil):
				t.Errorf("RemainingUSD = %v, want %v", got, c.wantRemaining)
			case got != nil && *got != *c.wantRemaining:
				t.Errorf("RemainingUSD = %v, want %v", *got, *c.wantRemaining)
			}
			if c.state.Exhausted() != c.wantExhausted {
				t.Errorf("Exhausted = %v, want %v", c.state.Exhausted(), c.wantExhausted)
			}
		})
	}
}

// TestPlanAllowance_MonthWindow pins the query window: the current UTC
// calendar month, [first-of-month, first-of-next-month) — the BillingReport
// convention — including a non-UTC clock normalized to UTC.
func TestPlanAllowance_MonthWindow(t *testing.T) {
	t.Parallel()
	reader := &fakeAllowanceReader{included: fptr(15), mtd: 3}
	// July 31st 23:30 at UTC+2 is August 1st 01:30 local — but the ledger's
	// days are UTC, so the window must still be July.
	loc := time.FixedZone("CEST", 2*3600)
	p := PlanAllowance{Reader: reader, Now: func() time.Time {
		return time.Date(2026, 8, 1, 1, 30, 0, 0, loc) // = 2026-07-31 23:30 UTC
	}}

	state, err := p.AllowanceState(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("AllowanceState: %v", err)
	}
	wantFrom := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	wantTo := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if !reader.gotFrom.Equal(wantFrom) || !reader.gotTo.Equal(wantTo) {
		t.Errorf("window = [%v, %v), want [%v, %v)", reader.gotFrom, reader.gotTo, wantFrom, wantTo)
	}
	if state.IncludedUSD == nil || *state.IncludedUSD != 15 || state.MonthUSD != 3 {
		t.Errorf("state = %+v", state)
	}
}

// A tenant with no configured allowance never touches the ledger — the common
// BYOK/self-host case costs one read.
func TestPlanAllowance_NoAllowanceSkipsLedger(t *testing.T) {
	t.Parallel()
	reader := &fakeAllowanceReader{}
	p := PlanAllowance{Reader: reader}
	state, err := p.AllowanceState(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("AllowanceState: %v", err)
	}
	if state.IncludedUSD != nil || state.Exhausted() {
		t.Errorf("state = %+v, want the zero no-gate state", state)
	}
	if reader.mtdCalls != 0 {
		t.Errorf("ledger read %d times for a no-allowance tenant, want 0", reader.mtdCalls)
	}
}

func TestPlanAllowance_ReadErrorsPropagate(t *testing.T) {
	t.Parallel()
	boom := errors.New("db down")
	for name, reader := range map[string]*fakeAllowanceReader{
		"allowance read": {includedErr: boom},
		"ledger read":    {included: fptr(15), mtdErr: boom},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := PlanAllowance{Reader: reader}
			if _, err := p.AllowanceState(context.Background(), uuid.New()); !errors.Is(err, boom) {
				t.Fatalf("err = %v, want wrapping the read error", err)
			}
		})
	}
}
