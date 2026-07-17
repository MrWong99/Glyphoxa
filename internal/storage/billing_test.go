//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// billingSpecs is a small two-tier catalog used across the billing tests.
func billingSpecs() []storage.PlanSpec {
	allowance := 15.0
	return []storage.PlanSpec{
		{Slug: "byok-free", DisplayName: "BYOK Free", KeySource: "byok"},
		{Slug: "all-inclusive", DisplayName: "All Inclusive", Description: "usage included",
			MonthlyPriceUSD: 20, KeySource: "platform", IncludedUsageUSD: &allowance,
			Limits: []byte(`{"max_campaigns":10}`)},
	}
}

// TestSyncPlansUpsertArchiveRevive proves the catalog sync lifecycle (ADR-0054):
// insert, update-by-slug, archive-missing, and revival of an archived slug.
func TestSyncPlansUpsertArchiveRevive(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	res, err := st.SyncPlans(ctx, billingSpecs(), false)
	if err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if res.Upserted != 2 || res.Archived != 0 {
		t.Fatalf("initial sync = %+v, want 2 upserted, 0 archived", res)
	}

	// Re-sync with an edited price and one plan missing + archiveMissing: the
	// edit lands, the missing plan archives, nothing is deleted.
	edited := billingSpecs()[1:]
	edited[0].MonthlyPriceUSD = 25
	res, err = st.SyncPlans(ctx, edited, true)
	if err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	if res.Upserted != 1 || res.Archived != 1 {
		t.Fatalf("re-sync = %+v, want 1 upserted, 1 archived", res)
	}

	plans, err := st.ListPlans(ctx)
	if err != nil {
		t.Fatalf("list plans: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("plans = %d, want 2 (archived kept)", len(plans))
	}
	bykSlug := map[string]storage.Plan{}
	for _, p := range plans {
		bykSlug[p.Slug] = p
	}
	if !bykSlug["byok-free"].Archived {
		t.Errorf("byok-free should be archived after archive-missing sync")
	}
	if got := bykSlug["all-inclusive"]; got.Archived || got.MonthlyPriceUSD != 25 {
		t.Errorf("all-inclusive = archived=%v price=%v, want active price 25", got.Archived, got.MonthlyPriceUSD)
	}
	if got := bykSlug["all-inclusive"]; got.IncludedUsageUSD == nil || *got.IncludedUsageUSD != 15 {
		t.Errorf("all-inclusive allowance = %v, want 15", got.IncludedUsageUSD)
	}

	// Syncing the full catalog again revives the archived slug.
	if _, err := st.SyncPlans(ctx, billingSpecs(), false); err != nil {
		t.Fatalf("revive sync: %v", err)
	}
	plans, _ = st.ListPlans(ctx)
	for _, p := range plans {
		if p.Archived {
			t.Errorf("plan %q still archived after revive sync", p.Slug)
		}
	}
}

// TestSubscriptionLifecycle proves subscribe → snapshot → switch → cancel, the
// one-active-subscription invariant, and the archived-plan refusal.
func TestSubscriptionLifecycle(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	if _, err := st.SyncPlans(ctx, billingSpecs(), false); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Unsubscribed tenant: ErrNotFound.
	if _, err := st.ActiveSubscription(ctx, tenantID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("active subscription before subscribe err = %v, want ErrNotFound", err)
	}

	sub, err := st.SetTenantPlan(ctx, tenantID, "all-inclusive")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if sub.PlanSlug != "all-inclusive" || sub.MonthlyPriceUSD != 20 || sub.EndedAt != nil {
		t.Fatalf("subscription = %+v, want active all-inclusive @ 20", sub)
	}

	// A price edit after subscribing must NOT rewrite the snapshot.
	edited := billingSpecs()
	edited[1].MonthlyPriceUSD = 99
	if _, err := st.SyncPlans(ctx, edited, false); err != nil {
		t.Fatalf("price edit sync: %v", err)
	}
	got, err := st.ActiveSubscription(ctx, tenantID)
	if err != nil {
		t.Fatalf("active subscription: %v", err)
	}
	if got.MonthlyPriceUSD != 20 {
		t.Fatalf("snapshot price after catalog edit = %v, want 20", got.MonthlyPriceUSD)
	}

	// Switching plans ends the old subscription and starts a new one.
	if _, err := st.SetTenantPlan(ctx, tenantID, "byok-free"); err != nil {
		t.Fatalf("switch plan: %v", err)
	}
	got, err = st.ActiveSubscription(ctx, tenantID)
	if err != nil {
		t.Fatalf("active after switch: %v", err)
	}
	if got.PlanSlug != "byok-free" {
		t.Fatalf("active plan after switch = %q, want byok-free", got.PlanSlug)
	}

	// Archived plans accept no new subscriptions.
	if _, err := st.SyncPlans(ctx, billingSpecs()[1:], true); err != nil { // archives byok-free
		t.Fatalf("archive sync: %v", err)
	}
	if _, err := st.SetTenantPlan(ctx, uuid.New(), "byok-free"); !errors.Is(err, storage.ErrPlanArchived) {
		t.Fatalf("subscribe to archived err = %v, want ErrPlanArchived", err)
	}

	// Unknown slug and unknown tenant.
	if _, err := st.SetTenantPlan(ctx, tenantID, "no-such-plan"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("unknown slug err = %v, want ErrNotFound", err)
	}

	// Cancel.
	if err := st.EndTenantPlan(ctx, tenantID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := st.ActiveSubscription(ctx, tenantID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("active after cancel err = %v, want ErrNotFound", err)
	}
	if err := st.EndTenantPlan(ctx, tenantID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("double cancel err = %v, want ErrNotFound", err)
	}
}

// TestAddUsageAccumulatesAndReports proves the upsert-accumulate ledger write
// and the per-tenant billing report window.
func TestAddUsageAccumulatesAndReports(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	if _, err := st.SyncPlans(ctx, billingSpecs(), false); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if _, err := st.SetTenantPlan(ctx, tenantID, "all-inclusive"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	day := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	row := storage.UsageRow{
		TenantID: tenantID, Day: day,
		Component: storage.ComponentLLM, Provider: "groq", Model: "openai/gpt-oss-120b",
		LLMInputTokens: 1000, LLMOutputTokens: 500, EstimatedUSD: 0.10,
	}
	if err := st.AddUsage(ctx, []storage.UsageRow{row}); err != nil {
		t.Fatalf("first AddUsage: %v", err)
	}
	// Same bucket again: must accumulate, not duplicate or overwrite.
	if err := st.AddUsage(ctx, []storage.UsageRow{row}); err != nil {
		t.Fatalf("second AddUsage: %v", err)
	}
	// A different day, outside the report window below.
	outside := row
	outside.Day = day.AddDate(0, 2, 0)
	if err := st.AddUsage(ctx, []storage.UsageRow{outside}); err != nil {
		t.Fatalf("outside AddUsage: %v", err)
	}

	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 1, 0)
	lines, err := st.BillingReport(ctx, from, to)
	if err != nil {
		t.Fatalf("BillingReport: %v", err)
	}
	// Filter to THIS test's tenant: on a shared GLYPHOXA_TEST_DSN database the
	// report legitimately includes other tests' tenants.
	var mine []storage.TenantBillingLine
	for _, line := range lines {
		if line.TenantID == tenantID {
			mine = append(mine, line)
		}
	}
	if len(mine) != 1 {
		t.Fatalf("report lines for tenant = %d, want 1", len(mine))
	}
	l := mine[0]
	if l.PlanSlug != "all-inclusive" || l.MonthlyPriceUSD != 20 {
		t.Fatalf("line = %+v, want all-inclusive @ 20", l)
	}
	if l.LLMInputTokens != 2000 || l.LLMOutputTokens != 1000 {
		t.Fatalf("tokens = %d/%d, want 2000/1000 (accumulated, window-scoped)", l.LLMInputTokens, l.LLMOutputTokens)
	}
	if l.EstimatedUSD < 0.199 || l.EstimatedUSD > 0.201 {
		t.Fatalf("estimated USD = %v, want ~0.20", l.EstimatedUSD)
	}

	// Tenant listing shows the active plan.
	tenants, err := st.ListTenantsWithPlan(ctx)
	if err != nil {
		t.Fatalf("ListTenantsWithPlan: %v", err)
	}
	found := false
	for _, tr := range tenants {
		if tr.ID == tenantID {
			found = true
			if tr.PlanSlug != "all-inclusive" {
				t.Fatalf("tenant plan = %q, want all-inclusive", tr.PlanSlug)
			}
		}
	}
	if !found {
		t.Fatalf("tenant %s missing from ListTenantsWithPlan", tenantID)
	}

	// Empty AddUsage is a no-op, never an error.
	if err := st.AddUsage(ctx, nil); err != nil {
		t.Fatalf("empty AddUsage: %v", err)
	}
}
