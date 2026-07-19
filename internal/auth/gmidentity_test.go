package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// defaultTenant is the Tenant every `ids`-shorthand binding is attributed to, so
// the deployment-wide IsGM tests keep their terse string form while the snapshot is
// per-Tenant underneath.
var defaultTenant = uuid.MustParse("11111111-1111-1111-1111-111111111111")

// fakeBindings is a scripted TenantOperatorBindingLister: it returns the configured
// bindings (or err), counts calls, and can block each list on a gate so the
// async-refresh dedup is observable. The `ids` shorthand attributes each snowflake
// to defaultTenant; `bindings` sets explicit per-Tenant rows.
type fakeBindings struct {
	mu       sync.Mutex
	ids      []string
	bindings []storage.TenantOperatorBinding
	err      error
	gate     chan struct{} // when non-nil, each List blocks on it before returning
	calls    int
}

func (f *fakeBindings) ListTenantOperatorBindings(context.Context) ([]storage.TenantOperatorBinding, error) {
	if f.gate != nil {
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := append([]storage.TenantOperatorBinding(nil), f.bindings...)
	for _, id := range f.ids {
		out = append(out, storage.TenantOperatorBinding{TenantID: defaultTenant, DiscordUserID: id})
	}
	return out, nil
}

func (f *fakeBindings) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeBindings) set(ids []string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids, f.bindings, f.err = ids, nil, err
}

const (
	boundGM    = "444444444444444444" // tenant-bound operator, NOT allowlisted
	allowedGM  = "555555555555555555" // allowlisted, NOT tenant-bound
	strangerGM = "666666666666666666" // neither
)

// TestGMIdentityUnion: GM identity is the transitional union (ADR-0055): a
// tenant-bound operator snowflake OR an env-allowlisted one is GM; anyone else
// (including the empty unattributed speaker) is not. The boundGM case pins the
// decided revocation semantics: a bound operator NOT on the allowlist (never
// listed, or since removed) IS GM — the binding is Tenant ownership and only a
// Tenant reassignment revokes it; the allowlist governs admission and web
// sessions, not GM identity (see the GMIdentity doc).
func TestGMIdentityUnion(t *testing.T) {
	bindings := &fakeBindings{ids: []string{boundGM}}
	g := NewGMIdentity(bindings, ParseOperatorAllowlist(allowedGM), nil)
	if err := g.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if !g.IsGM(boundGM) {
		t.Error("tenant-bound operator must be GM even when absent from the allowlist")
	}
	if !g.IsGM(allowedGM) {
		t.Error("allowlisted snowflake must stay GM (transitional fallback)")
	}
	if g.IsGM(strangerGM) {
		t.Error("unbound, non-allowlisted snowflake must not be GM")
	}
	if g.IsGM("") {
		t.Error("the empty (unattributed) speaker must never be GM")
	}
}

// TestIsGMInTenant: GM standing is per-Tenant (#490, ADR-0055 deployment-scope
// caveat closed). A tenant-bound operator is GM in its OWN Tenant only — a Tenant A
// operator invoking in Tenant B is NOT GM there (the cross-tenant escalation the
// regression guards). The env allowlist stays a DEPLOYMENT-WIDE override: an
// allowlisted snowflake is GM in EVERY Tenant (interim platform-admin identity).
func TestIsGMInTenant(t *testing.T) {
	tenantA := uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000000")
	tenantB := uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000000")

	bindings := &fakeBindings{bindings: []storage.TenantOperatorBinding{
		{TenantID: tenantA, DiscordUserID: boundGM},
	}}
	g := NewGMIdentity(bindings, ParseOperatorAllowlist(allowedGM), nil)
	if err := g.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// Bound operator: GM in its own Tenant, NOT in another.
	if !g.IsGMInTenant(tenantA, boundGM) {
		t.Error("tenant-A operator must be GM in tenant A")
	}
	if g.IsGMInTenant(tenantB, boundGM) {
		t.Error("tenant-A operator must NOT be GM in tenant B (cross-tenant escalation)")
	}

	// Allowlisted snowflake: deployment-wide override — GM in every Tenant.
	if !g.IsGMInTenant(tenantA, allowedGM) || !g.IsGMInTenant(tenantB, allowedGM) {
		t.Error("allowlisted snowflake must be GM in every tenant (deployment-wide override)")
	}

	// Stranger and the empty speaker: never GM.
	if g.IsGMInTenant(tenantA, strangerGM) {
		t.Error("unbound, non-allowlisted snowflake must not be GM in any tenant")
	}
	if g.IsGMInTenant(tenantA, "") {
		t.Error("the empty (unattributed) speaker must never be GM")
	}
}

// TestGMIdentityRefreshErrorKeepsAllowlist: a failed boot Refresh surfaces the
// error but the checker still serves the allowlist fallback — GM identity
// degrades to today's env-only behavior, never to nobody.
func TestGMIdentityRefreshErrorKeepsAllowlist(t *testing.T) {
	bindings := &fakeBindings{err: errors.New("db down")}
	g := NewGMIdentity(bindings, ParseOperatorAllowlist(allowedGM), nil)
	if err := g.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh must surface the lister error")
	}

	if !g.IsGM(allowedGM) {
		t.Error("allowlist fallback must survive a failed refresh")
	}
	if g.IsGM(boundGM) {
		t.Error("no binding snapshot loaded, so a bound-only id is not yet GM")
	}
}

// TestGMIdentityStaleServesSnapshotAndRefreshesOnce: an expired snapshot is
// still served (IsGM never blocks) while ONE deduplicated background refresh
// runs; once it lands, the new binding is visible.
func TestGMIdentityStaleServesSnapshotAndRefreshesOnce(t *testing.T) {
	bindings := &fakeBindings{ids: []string{boundGM}}
	g := NewGMIdentity(bindings, ParseOperatorAllowlist(""), nil)

	base := time.Now()
	now := base
	var nowMu sync.Mutex
	g.now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}

	if err := g.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := bindings.callCount(); got != 1 {
		t.Fatalf("boot refresh calls = %d, want 1", got)
	}

	// A new operator binds; the snapshot has not expired yet, so IsGM serves the
	// old set with no new list call.
	bindings.set([]string{boundGM, strangerGM}, nil)
	if g.IsGM(strangerGM) {
		t.Error("fresh snapshot must be served as-is (no re-list before expiry)")
	}
	if got := bindings.callCount(); got != 1 {
		t.Fatalf("pre-expiry IsGM re-listed: calls = %d, want 1", got)
	}

	// Expire the snapshot behind a gate: several IsGM calls must all serve the
	// stale set immediately and spawn exactly ONE background refresh.
	bindings.gate = make(chan struct{})
	nowMu.Lock()
	now = base.Add(gmSnapshotTTL + time.Second)
	nowMu.Unlock()
	for range 3 {
		if !g.IsGM(boundGM) {
			t.Error("stale snapshot must still be served while the refresh runs")
		}
	}
	close(bindings.gate)
	g.wg.Wait()
	if got := bindings.callCount(); got != 2 {
		t.Errorf("stale IsGM burst refreshed %d times, want exactly 1 more (2 total)", got)
	}
	if !g.IsGM(strangerGM) {
		t.Error("the refreshed snapshot must carry the newly-bound operator")
	}
}

// TestGMIdentityFailedBackgroundRefreshKeepsSnapshot: a background refresh that
// errors keeps serving the last good snapshot (plus allowlist) rather than
// dropping to nobody, and a later successful refresh recovers.
func TestGMIdentityFailedBackgroundRefreshKeepsSnapshot(t *testing.T) {
	bindings := &fakeBindings{ids: []string{boundGM}}
	g := NewGMIdentity(bindings, ParseOperatorAllowlist(""), nil)

	base := time.Now()
	now := base
	var nowMu sync.Mutex
	g.now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}

	if err := g.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	bindings.set(nil, errors.New("db down"))
	nowMu.Lock()
	now = base.Add(gmSnapshotTTL + time.Second)
	nowMu.Unlock()
	_ = g.IsGM(boundGM) // triggers the failing background refresh
	g.wg.Wait()
	if !g.IsGM(boundGM) {
		t.Error("failed background refresh must keep the last good snapshot")
	}

	// Recovery: the lister heals, the next stale read refreshes to the new set.
	bindings.set([]string{strangerGM}, nil)
	nowMu.Lock()
	now = base.Add(2 * (gmSnapshotTTL + time.Second))
	nowMu.Unlock()
	_ = g.IsGM(boundGM)
	g.wg.Wait()
	if g.IsGM(boundGM) {
		t.Error("recovered refresh must serve the NEW binding set (old operator unbound)")
	}
	if !g.IsGM(strangerGM) {
		t.Error("recovered refresh must serve the newly-bound operator")
	}
}

// TestGMIdentityEmpty: Empty reports whether the union has no identity source at
// all — the "Butler unaddressable by voice" warning condition.
func TestGMIdentityEmpty(t *testing.T) {
	g := NewGMIdentity(&fakeBindings{}, ParseOperatorAllowlist(""), nil)
	_ = g.Refresh(context.Background())
	if !g.Empty() {
		t.Error("no bindings + empty allowlist must report Empty")
	}

	g = NewGMIdentity(&fakeBindings{ids: []string{boundGM}}, ParseOperatorAllowlist(""), nil)
	_ = g.Refresh(context.Background())
	if g.Empty() {
		t.Error("a tenant-bound operator must clear Empty")
	}

	g = NewGMIdentity(&fakeBindings{}, ParseOperatorAllowlist(allowedGM), nil)
	_ = g.Refresh(context.Background())
	if g.Empty() {
		t.Error("an allowlisted snowflake must clear Empty")
	}
}
