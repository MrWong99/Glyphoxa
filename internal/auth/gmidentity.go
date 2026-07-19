package auth

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// gmSnapshotTTL bounds how stale the tenant-binding snapshot may get before a
// non-blocking IsGM kicks a background reload. Bindings change only on a web
// login's tenant claim (ResolveOperatorTenant), so a minute of staleness is
// invisible in practice while keeping the hot voice/interaction paths free of
// per-call DB reads.
const gmSnapshotTTL = time.Minute

// gmRefreshTimeout bounds one background snapshot reload so a stalled DB cannot
// leak goroutines (mirrors speaker.Resolver's fill bound).
const gmRefreshTimeout = 5 * time.Second

// TenantOperatorLister lists each Tenant paired with its operator's Discord
// snowflake (tenant.operator_user_id → users.discord_user_id) — the per-Tenant
// GM-identity source (#490). *storage.Store satisfies it via
// ListTenantOperatorBindings.
type TenantOperatorLister interface {
	ListTenantOperatorBindings(ctx context.Context) ([]storage.TenantOperatorBinding, error)
}

// GMIdentity resolves GM identity for the GM-identity consumers (the Butler
// voice-address gate, the speaker resolver's transcript GM labels, and the
// presence gate's GM-only slash commands) from the Tenant binding instead of
// env-allowlist membership alone (ADR-0055, amending ADR-0050's GM-identity
// clause). It serves TWO shapes off one per-Tenant snapshot:
//
//   - IsGMInTenant(tenantID, id) — the PER-TENANT verdict (#490): id is GM when it
//     is bound as THIS Tenant's operator, OR on the GLYPHOXA_OPERATOR_IDS
//     allowlist (a DEPLOYMENT-WIDE override — the interim platform-admin identity,
//     GM in every Tenant). This closes ADR-0055's deployment-scope caveat: a
//     Tenant A operator is no longer GM in Tenant B. It is the authorization
//     verdict the presence Gate and the per-session Butler gate consume.
//   - IsGM(id) — the DEPLOYMENT-WIDE union (label-only): id is GM in SOME Tenant,
//     OR allowlisted. Kept ONLY for the transcript-label path (speaker.Resolver,
//     which is campaign-keyed and has no Tenant handy) and the standalone voice
//     node's Butler gate (single-operator, ADR-0039). Never use it to AUTHORIZE a
//     cross-tenant action — that is IsGMInTenant's job.
//
// The union is the transitional semantics the self-signup design note requires —
// it keeps GM working for an operator who never completed a web login
// (operator_user_id is NULL, only the allowlist knows them) and for a multi-entry
// allowlist's second account. Member-Role-based GM replaces this when
// tenant_members lands.
//
// A bound operator REMOVED from the allowlist stays GM (in its own Tenant): the
// binding is Tenant ownership and nothing unbinds it (ADR-0055 — GM identity now
// derives from ownership, not list membership). Allowlist removal still revokes
// what it always governed — admission at the OAuth callback and live web sessions
// at the boot sweep. Full GM revocation of an ex-owner is a Tenant reassignment.
//
// Neither verdict blocks or does I/O: they serve the cached binding snapshot and,
// when it has expired, kick ONE deduplicated background reload — the same
// never-block posture as speaker.Resolver, because they run inside the voice
// address-detection path and the interaction dispatch.
type GMIdentity struct {
	bindings TenantOperatorLister
	allow    OperatorAllowlist
	log      *slog.Logger
	now      func() time.Time

	mu       sync.Mutex
	bound    map[string]map[uuid.UUID]struct{} // discordUserID -> set of Tenants it operates
	expires  time.Time                         // snapshot expiry; zero = never loaded
	inflight bool                              // a background reload is running (dedup)
	wg       sync.WaitGroup                    // tracks in-flight reloads (test synchronization)
}

// NewGMIdentity builds the checker over the binding lister and the parsed env
// allowlist (the transitional fallback). log nil defaults to a discarding
// logger. Call [GMIdentity.Refresh] once at boot to load the initial snapshot;
// until it succeeds, IsGM serves the allowlist alone.
func NewGMIdentity(bindings TenantOperatorLister, allow OperatorAllowlist, log *slog.Logger) *GMIdentity {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &GMIdentity{
		bindings: bindings,
		allow:    allow,
		log:      log,
		now:      time.Now,
		bound:    map[string]map[uuid.UUID]struct{}{},
	}
}

// Refresh synchronously reloads the binding snapshot — the boot-time load. A
// failure keeps the previous snapshot (initially empty, so IsGM degrades to
// the allowlist fallback) and is returned for the caller to log; it never
// needs to fail the boot.
func (g *GMIdentity) Refresh(ctx context.Context) error {
	bindings, err := g.bindings.ListTenantOperatorBindings(ctx)
	if err != nil {
		return fmt.Errorf("auth: list tenant-operator bindings: %w", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.install(bindings)
	return nil
}

// install replaces the snapshot with the per-Tenant bindings and restamps the
// expiry. Callers hold g.mu. An empty snowflake is skipped defensively — it must
// never make the empty (unattributed) speaker a GM.
func (g *GMIdentity) install(bindings []storage.TenantOperatorBinding) {
	next := make(map[string]map[uuid.UUID]struct{}, len(bindings))
	for _, b := range bindings {
		if b.DiscordUserID == "" {
			continue
		}
		set := next[b.DiscordUserID]
		if set == nil {
			set = map[uuid.UUID]struct{}{}
			next[b.DiscordUserID] = set
		}
		set[b.TenantID] = struct{}{}
	}
	g.bound = next
	g.expires = g.now().Add(gmSnapshotTTL)
}

// IsGM reports the DEPLOYMENT-WIDE verdict: the snowflake is bound as SOME
// Tenant's operator, or allowlisted (the transitional union, ADR-0055). It is
// LABEL-ONLY (#490): use it for the transcript GM label (speaker.Resolver, which
// is campaign-keyed) and the standalone voice node's single-operator Butler gate.
// To AUTHORIZE a cross-tenant action use IsGMInTenant. It never blocks: an expired
// snapshot is still served while one background reload runs. The empty
// (unattributed) speaker is never a GM.
func (g *GMIdentity) IsGM(discordUserID string) bool {
	if discordUserID == "" {
		return false
	}

	g.mu.Lock()
	set, bound := g.bound[discordUserID]
	boundAnywhere := bound && len(set) > 0
	g.kickRefreshLocked()
	g.mu.Unlock()

	return boundAnywhere || g.allow.Contains(discordUserID)
}

// IsGMInTenant is the PER-TENANT authorization verdict (#490, ADR-0055): the
// snowflake is GM in tenantID when it is bound as THIS Tenant's operator, OR on the
// GLYPHOXA_OPERATOR_IDS allowlist (a DEPLOYMENT-WIDE override — the interim
// platform-admin identity). A Tenant A operator is therefore NOT GM in Tenant B,
// closing the cross-tenant escalation ADR-0055 flagged. Same never-block posture as
// IsGM; the empty (unattributed) user is never GM.
func (g *GMIdentity) IsGMInTenant(tenantID uuid.UUID, discordUserID string) bool {
	if discordUserID == "" {
		return false
	}

	g.mu.Lock()
	_, boundHere := g.bound[discordUserID][tenantID]
	g.kickRefreshLocked()
	g.mu.Unlock()

	return boundHere || g.allow.Contains(discordUserID)
}

// kickRefreshLocked spawns ONE deduplicated background reload when the snapshot has
// expired. Callers hold g.mu.
func (g *GMIdentity) kickRefreshLocked() {
	if !g.now().Before(g.expires) && !g.inflight {
		g.inflight = true
		g.wg.Add(1)
		go g.refreshAsync()
	}
}

// refreshAsync runs one background snapshot reload. A failure keeps the last
// good snapshot (warned, so a silent GM freeze is diagnosable) and re-stamps
// the expiry either way — a broken DB retries once per TTL, not per call.
func (g *GMIdentity) refreshAsync() {
	defer g.wg.Done()

	ctx, cancel := context.WithTimeout(context.Background(), gmRefreshTimeout)
	defer cancel()
	bindings, err := g.bindings.ListTenantOperatorBindings(ctx)

	g.mu.Lock()
	defer g.mu.Unlock()
	g.inflight = false
	if err != nil {
		g.log.Warn("auth: refreshing tenant-operator GM bindings failed; serving the last snapshot", "err", err)
		g.expires = g.now().Add(gmSnapshotTTL)
		return
	}
	g.install(bindings)
}

// Empty reports whether the union has no identity source at all — no bound
// tenant operator in the snapshot and an empty allowlist. The composition
// roots use it for the "Butler unaddressable by voice" boot warning.
func (g *GMIdentity) Empty() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.bound) == 0 && g.allow.Len() == 0
}
