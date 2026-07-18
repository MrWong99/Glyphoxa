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

// signupParams returns a valid open-mode signup input for the given snowflake,
// bound to the billingSpecs BYOK tier (the ADR-0055 default-plan shape).
func signupParams(discordID, token string) storage.SignupParams {
	return storage.SignupParams{
		User: storage.UpsertUserParams{
			DiscordUserID: discordID, Name: "Rin Okabe", Avatar: "https://cdn/a.png",
		},
		TenantName: "Rin's Table",
		PlanSlug:   "byok-free",
		Session: storage.NewSession{
			Token:     token,
			ExpiresAt: time.Now().Add(time.Hour),
			IP:        "203.0.113.7",
			UA:        "test-agent",
		},
	}
}

func TestAdmissionPostureRecordAndGet(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()

	if _, err := st.GetAdmissionPosture(ctx); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetAdmissionPosture on fresh DB = %v, want ErrNotFound", err)
	}

	if err := st.RecordAdmissionPosture(ctx, "open"); err != nil {
		t.Fatalf("RecordAdmissionPosture(open): %v", err)
	}
	mode, err := st.GetAdmissionPosture(ctx)
	if err != nil || mode != "open" {
		t.Fatalf("GetAdmissionPosture = %q, %v, want open", mode, err)
	}

	// Recording again upserts the singleton row rather than inserting a second.
	if err := st.RecordAdmissionPosture(ctx, "allowlist"); err != nil {
		t.Fatalf("RecordAdmissionPosture(allowlist): %v", err)
	}
	mode, err = st.GetAdmissionPosture(ctx)
	if err != nil || mode != "allowlist" {
		t.Fatalf("GetAdmissionPosture after flip = %q, %v, want allowlist", mode, err)
	}
}

// TestAuthenticateSessionRefusesSuspended pins the ADR-0055 per-request
// re-check: suspension takes effect on the very next request, with no session
// deletion, and unsuspending restores access with the same token.
func TestAuthenticateSessionRefusesSuspended(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()

	u, err := st.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: "555001", Name: "Mo"})
	if err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	_, err = st.CreateSession(ctx, storage.NewSession{
		UserID: u.ID, Token: "tok-suspend", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := st.AuthenticateSession(ctx, "tok-suspend"); err != nil {
		t.Fatalf("AuthenticateSession before suspension: %v", err)
	}

	if err := st.SetUserSuspended(ctx, "555001", true); err != nil {
		t.Fatalf("SetUserSuspended(true): %v", err)
	}
	if _, err := st.AuthenticateSession(ctx, "tok-suspend"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("AuthenticateSession while suspended = %v, want ErrNotFound", err)
	}

	if err := st.SetUserSuspended(ctx, "555001", false); err != nil {
		t.Fatalf("SetUserSuspended(false): %v", err)
	}
	if _, err := st.AuthenticateSession(ctx, "tok-suspend"); err != nil {
		t.Fatalf("AuthenticateSession after unsuspend: %v", err)
	}
}

func TestSetUserSuspendedUnknownUser(t *testing.T) {
	st := migrated(t)
	if err := st.SetUserSuspended(context.Background(), "999999", true); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("SetUserSuspended(unknown) = %v, want ErrNotFound", err)
	}
}

// TestProvisionSignupFoundsTenantOnce pins the ADR-0055 open-mode provisioning
// contract: the first signup founds a fresh Tenant bound to the user, binds the
// default Plan, and mints a working session — and a returning signup reuses the
// bound Tenant instead of founding another.
func TestProvisionSignupFoundsTenantOnce(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()
	if _, err := st.SyncPlans(ctx, billingSpecs(), false); err != nil {
		t.Fatalf("SyncPlans: %v", err)
	}

	res, err := st.ProvisionSignup(ctx, signupParams("777001", "tok-first"))
	if err != nil {
		t.Fatalf("ProvisionSignup: %v", err)
	}
	if !res.Created {
		t.Fatalf("first signup Created = false, want true")
	}
	if res.Tenant.Name != "Rin's Table" {
		t.Fatalf("tenant name = %q, want Rin's Table", res.Tenant.Name)
	}
	sub, err := st.ActiveSubscription(ctx, res.Tenant.ID)
	if err != nil || sub.PlanSlug != "byok-free" {
		t.Fatalf("ActiveSubscription = %+v, %v, want byok-free", sub, err)
	}
	if got, err := st.AuthenticateSession(ctx, "tok-first"); err != nil || got.ID != res.User.ID {
		t.Fatalf("AuthenticateSession(minted) = %+v, %v, want signup user", got, err)
	}
	if tid, err := st.TenantForUser(ctx, res.User.ID); err != nil || tid != res.Tenant.ID {
		t.Fatalf("TenantForUser = %s, %v, want bound tenant %s", tid, err, res.Tenant.ID)
	}

	// A returning login provisions nothing new: same tenant, no second
	// subscription bind, a fresh session.
	again, err := st.ProvisionSignup(ctx, signupParams("777001", "tok-second"))
	if err != nil {
		t.Fatalf("ProvisionSignup returning: %v", err)
	}
	if again.Created {
		t.Fatalf("returning signup Created = true, want false")
	}
	if again.Tenant.ID != res.Tenant.ID {
		t.Fatalf("returning tenant = %s, want %s", again.Tenant.ID, res.Tenant.ID)
	}
	sub2, err := st.ActiveSubscription(ctx, res.Tenant.ID)
	if err != nil || sub2.ID != sub.ID {
		t.Fatalf("returning login rebound the plan: %+v vs %+v (%v)", sub2, sub, err)
	}
	if _, err := st.AuthenticateSession(ctx, "tok-second"); err != nil {
		t.Fatalf("AuthenticateSession(returning) : %v", err)
	}
}

// TestProvisionSignupNeverClaimsUnboundTenant pins the create-only rule: an
// existing unbound Tenant (the operator's seed) is NOT claimable by a signup —
// ADR-0041's no-TOFU rejection carried into ADR-0055.
func TestProvisionSignupNeverClaimsUnboundTenant(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()
	if _, err := st.SyncPlans(ctx, billingSpecs(), false); err != nil {
		t.Fatalf("SyncPlans: %v", err)
	}
	seedID, err := st.CreateTenant(ctx, "Seeded")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	res, err := st.ProvisionSignup(ctx, signupParams("777002", "tok-seed"))
	if err != nil {
		t.Fatalf("ProvisionSignup: %v", err)
	}
	if res.Tenant.ID == seedID {
		t.Fatalf("signup claimed the seeded unbound tenant — create-only violated")
	}
	// The seed stays unbound: ResolveOperatorTenant for a real operator can
	// still claim it later.
	if tid, err := st.TenantForUser(ctx, res.User.ID); err != nil || tid == seedID {
		t.Fatalf("TenantForUser = %s, %v; must be the fresh tenant", tid, err)
	}
}

// TestProvisionSignupAtomicOnBadPlan pins all-or-nothing provisioning
// (ADR-0055): a failing default-plan bind leaves no user, tenant, or session
// behind — no ownerless half-provisioned Tenants on retry.
func TestProvisionSignupAtomicOnBadPlan(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()
	// No plans synced: the bind must fail with ErrNotFound.
	p := signupParams("777003", "tok-atomic")
	if _, err := st.ProvisionSignup(ctx, p); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("ProvisionSignup with unknown slug = %v, want ErrNotFound", err)
	}
	if _, err := st.GetUserByDiscordID(ctx, "777003"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("user row survived the rolled-back signup: %v", err)
	}
	if _, err := st.AuthenticateSession(ctx, "tok-atomic"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("session survived the rolled-back signup: %v", err)
	}
	if _, err := st.FindTenantByName(ctx, p.TenantName); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("tenant row survived the rolled-back signup: %v", err)
	}
}

// TestProvisionSignupRefusesSuspendedUser: a suspended user completing OAuth
// again must not mint a session that the per-request re-check would refuse
// anyway — the callback needs a crisp signal to bounce them at the door.
func TestProvisionSignupRefusesSuspendedUser(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()
	if _, err := st.SyncPlans(ctx, billingSpecs(), false); err != nil {
		t.Fatalf("SyncPlans: %v", err)
	}
	if _, err := st.ProvisionSignup(ctx, signupParams("777004", "tok-a")); err != nil {
		t.Fatalf("first signup: %v", err)
	}
	if err := st.SetUserSuspended(ctx, "777004", true); err != nil {
		t.Fatalf("SetUserSuspended: %v", err)
	}
	if _, err := st.ProvisionSignup(ctx, signupParams("777004", "tok-b")); !errors.Is(err, storage.ErrUserSuspended) {
		t.Fatalf("ProvisionSignup(suspended) = %v, want ErrUserSuspended", err)
	}
	if _, err := st.AuthenticateSession(ctx, "tok-b"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("suspended signup minted a session anyway")
	}
}

func TestGetPlanBySlug(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()
	if _, err := st.SyncPlans(ctx, billingSpecs(), false); err != nil {
		t.Fatalf("SyncPlans: %v", err)
	}

	p, err := st.GetPlanBySlug(ctx, "all-inclusive")
	if err != nil {
		t.Fatalf("GetPlanBySlug: %v", err)
	}
	if p.Slug != "all-inclusive" || p.KeySource != "platform" || p.Archived {
		t.Fatalf("plan = %+v, want live all-inclusive platform plan", p)
	}
	if p.IncludedUsageUSD == nil || *p.IncludedUsageUSD != 15.0 {
		t.Fatalf("IncludedUsageUSD = %v, want 15", p.IncludedUsageUSD)
	}

	if _, err := st.GetPlanBySlug(ctx, "nope"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetPlanBySlug(unknown) = %v, want ErrNotFound", err)
	}

	// Archived plans are returned with the flag set — the caller (the boot
	// preflight) decides archived is a refusal; the read stays faithful.
	if _, err := st.SyncPlans(ctx, billingSpecs()[1:], true); err != nil {
		t.Fatalf("archive sync: %v", err)
	}
	p, err = st.GetPlanBySlug(ctx, "byok-free")
	if err != nil || !p.Archived {
		t.Fatalf("archived plan = %+v, %v, want Archived=true", p, err)
	}
}

func TestRenameTenant(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()
	id, err := st.CreateTenant(ctx, "Before")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	ten, err := st.RenameTenant(ctx, id, "After")
	if err != nil || ten.Name != "After" || ten.ID != id {
		t.Fatalf("RenameTenant = %+v, %v, want renamed tenant", ten, err)
	}
	got, err := st.GetTenant(ctx, id)
	if err != nil || got.Name != "After" {
		t.Fatalf("GetTenant after rename = %+v, %v", got, err)
	}

	if _, err := st.RenameTenant(ctx, uuid.New(), "X"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("RenameTenant(unknown) = %v, want ErrNotFound", err)
	}
}

// TestTenantAllowanceReads pins the two reads behind the ADR-0055 monthly
// allowance gate (b): the live plan-joined allowance and the month-window
// ledger sum. The gate itself lives outside storage — the Usage Ledger never
// gates (ADR-0054).
func TestTenantAllowanceReads(t *testing.T) {
	st := migrated(t)
	ctx := context.Background()
	if _, err := st.SyncPlans(ctx, billingSpecs(), false); err != nil {
		t.Fatalf("SyncPlans: %v", err)
	}
	tenantID, err := st.CreateTenant(ctx, "Metered")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	// No subscription: no allowance.
	inc, err := st.TenantIncludedUsageUSD(ctx, tenantID)
	if err != nil || inc != nil {
		t.Fatalf("no-sub allowance = %v, %v, want nil", inc, err)
	}
	// BYOK plan: NULL allowance.
	if _, err := st.SetTenantPlan(ctx, tenantID, "byok-free"); err != nil {
		t.Fatalf("SetTenantPlan byok: %v", err)
	}
	inc, err = st.TenantIncludedUsageUSD(ctx, tenantID)
	if err != nil || inc != nil {
		t.Fatalf("byok allowance = %v, %v, want nil", inc, err)
	}
	// Platform plan: the live plan row's allowance.
	if _, err := st.SetTenantPlan(ctx, tenantID, "all-inclusive"); err != nil {
		t.Fatalf("SetTenantPlan platform: %v", err)
	}
	inc, err = st.TenantIncludedUsageUSD(ctx, tenantID)
	if err != nil || inc == nil || *inc != 15.0 {
		t.Fatalf("platform allowance = %v, %v, want 15", inc, err)
	}

	// Month-window sum: rows inside [from, to) count, rows outside don't.
	juneDay := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	julyDay := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
	rows := []storage.UsageRow{
		{TenantID: tenantID, Day: juneDay, Component: storage.ComponentLLM,
			Provider: "groq", Model: "m", EstimatedUSD: 4.5},
		{TenantID: tenantID, Day: julyDay, Component: storage.ComponentLLM,
			Provider: "groq", Model: "m", EstimatedUSD: 1.25},
		{TenantID: tenantID, Day: julyDay, Component: storage.ComponentTTS,
			Provider: "elevenlabs", EstimatedUSD: 0.75},
	}
	if err := st.AddUsage(ctx, rows); err != nil {
		t.Fatalf("AddUsage: %v", err)
	}
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	sum, err := st.TenantMonthUsageUSD(ctx, tenantID, from, to)
	if err != nil || sum != 2.0 {
		t.Fatalf("July sum = %v, %v, want 2.0", sum, err)
	}
	// A tenant with no ledger rows sums to zero, not an error.
	otherID, err := st.CreateTenant(ctx, "Quiet")
	if err != nil {
		t.Fatalf("CreateTenant quiet: %v", err)
	}
	sum, err = st.TenantMonthUsageUSD(ctx, otherID, from, to)
	if err != nil || sum != 0 {
		t.Fatalf("quiet sum = %v, %v, want 0", sum, err)
	}
}
