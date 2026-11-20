//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/billing"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
)

// TestOffSessionMeteringAgainstTheRealLedger runs the off-session metering
// seam (#658, ADR-0054 amendment 2026-11-20) against real tables, wired the
// way `open` Admission Mode wires it: the store is both the ledger it writes
// and the allowance it reads. It pins the loop the unit tests can only fake —
// what one call writes is what the next call's gate reads:
//
//   - a platform-plan Tenant's call lands a usage_ledger row under the right
//     (component, provider, model) with a priced estimate;
//   - once the month's ledger reaches the plan's allowance the NEXT call is
//     refused, with nothing written;
//   - a BYOK-plan Tenant is never refused however much it has used, and is
//     ledgered all the same;
//   - one Tenant's spend never counts against another's allowance.
func TestOffSessionMeteringAgainstTheRealLedger(t *testing.T) {
	st, pool := migratedWithPool(t)
	ctx := context.Background()
	if _, err := st.SyncPlans(ctx, billingSpecs(), false); err != nil {
		t.Fatalf("SyncPlans: %v", err)
	}
	platform, err := st.CreateTenant(ctx, "Trial")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := st.SetTenantPlan(ctx, platform, "all-inclusive"); err != nil { // allowance: $15
		t.Fatalf("SetTenantPlan platform: %v", err)
	}
	byok, err := st.CreateTenant(ctx, "Own keys")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := st.SetTenantPlan(ctx, byok, "byok-free"); err != nil {
		t.Fatalf("SetTenantPlan byok: %v", err)
	}

	seam := billing.NewOffSession(st, spend.PlanAllowance{Reader: st}, nil)

	// One image-sized call for the platform Tenant: allowed, and ledgered.
	const model = "gemini-2.5-flash-image"
	call, err := seam.Begin(ctx, platform, nil)
	if err != nil {
		t.Fatalf("Begin under a fresh allowance: %v", err)
	}
	call.Recorder().LLMTokens(observe.ProviderGemini, model, 120, 1290)
	if err := call.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var (
		rows     int
		in, out  int64
		estimate float64
	)
	if err := pool.QueryRow(ctx,
		`SELECT count(*), COALESCE(sum(llm_input_tokens),0), COALESCE(sum(llm_output_tokens),0), COALESCE(sum(estimated_usd),0)
		   FROM usage_ledger
		  WHERE tenant_id = $1 AND component = 'llm' AND provider = 'gemini' AND model = $2`,
		platform, model).Scan(&rows, &in, &out, &estimate); err != nil {
		t.Fatalf("read usage_ledger: %v", err)
	}
	if rows != 1 || in != 120 || out != 1290 {
		t.Fatalf("usage_ledger: %d rows, %d/%d tokens; want 1 row with 120/1290", rows, in, out)
	}
	if estimate <= 0 || estimate != call.EstimatedUSD() {
		t.Errorf("ledger estimate = %v, call estimate = %v; want the same priced amount", estimate, call.EstimatedUSD())
	}

	// Spend the rest of the allowance. Sized from the price map's own estimate
	// so the test does not hard-code a rate.
	perMTok := spend.EstimateLLMUSD(observe.ProviderGemini, model, 0, 1_000_000)
	if perMTok <= 0 {
		t.Fatalf("no price for %s", model)
	}
	call, err = seam.Begin(ctx, platform, nil)
	if err != nil {
		t.Fatalf("Begin with allowance left: %v", err)
	}
	call.Recorder().LLMTokens(observe.ProviderGemini, model, 0, int(15/perMTok*1_000_000)+1)
	if err := call.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// The gate now reads what the calls above wrote.
	if _, err := seam.Begin(ctx, platform, nil); !errors.Is(err, billing.ErrAllowanceExhausted) {
		t.Fatalf("Begin after the allowance is spent = %v, want ErrAllowanceExhausted", err)
	}
	if err := seam.Check(ctx, platform); !errors.Is(err, billing.ErrAllowanceExhausted) {
		t.Fatalf("Check after the allowance is spent = %v, want ErrAllowanceExhausted", err)
	}

	// The BYOK Tenant is untouched by the other Tenant's spend, is never
	// refused for its own, and is ledgered.
	for range 2 {
		call, err = seam.Begin(ctx, byok, nil)
		if err != nil {
			t.Fatalf("BYOK tenant refused: %v", err)
		}
		call.Recorder().LLMTokens(observe.ProviderGemini, model, 0, 100_000_000) // far past any allowance
		if err := call.Close(ctx); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*), COALESCE(sum(llm_output_tokens),0) FROM usage_ledger WHERE tenant_id = $1`, byok).Scan(&rows, &out); err != nil {
		t.Fatalf("read usage_ledger: %v", err)
	}
	if rows != 1 || out != 200_000_000 {
		t.Errorf("BYOK ledger: %d rows, %d output tokens; want the two calls accumulated into one daily bucket of 200000000", rows, out)
	}
}
