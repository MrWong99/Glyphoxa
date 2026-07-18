package auth

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
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

// TenantOperatorLister lists the Discord snowflakes bound as tenant operators
// (tenant.operator_user_id → users.discord_user_id). *storage.Store satisfies
// it via ListTenantOperatorDiscordIDs.
type TenantOperatorLister interface {
	ListTenantOperatorDiscordIDs(ctx context.Context) ([]string, error)
}

// GMIdentity resolves GM identity for the three GM-identity consumers (the
// Butler voice-address gate, the speaker resolver's transcript GM labels, and
// the presence gate's GM-only slash commands) from the Tenant binding instead
// of env-allowlist membership alone (ADR-0055, amending ADR-0050's GM-identity
// clause): a snowflake is GM when it is bound as a tenant operator OR on the
// GLYPHOXA_OPERATOR_IDS allowlist. The union is the transitional semantics the
// self-signup design note requires — it keeps GM working for an operator who
// never completed a web login (operator_user_id is NULL, only the allowlist
// knows them) and for a multi-entry allowlist's second account. Member-Role-
// based GM replaces this when tenant_members lands.
//
// Two deliberate consequences of the union, both to revisit before `open`
// Admission Mode ships (they are fine while every principal is
// allowlist-admitted, phase A of the design note):
//
//   - The snapshot is DEPLOYMENT-scoped, not per-Campaign: any tenant's bound
//     operator is GM everywhere. With one bound operator (today's tier) that
//     is exact; `open` mode must scope GM to the Campaign's own Tenant
//     (design note §1.4) or every signup founder becomes GM in every
//     tenant's transcripts — the cross-tenant class §0a hardening closes.
//   - A bound operator REMOVED from the allowlist stays GM: the binding is
//     Tenant ownership and nothing unbinds it (ADR-0055 — GM identity now
//     derives from ownership, not list membership). Allowlist removal still
//     revokes what it always governed — admission at the OAuth callback and
//     live web sessions at the boot sweep. Full GM revocation of an ex-owner
//     is a Tenant reassignment, i.e. the tenant_members track.
//
// IsGM NEVER blocks or does I/O: it serves the cached binding snapshot and,
// when the snapshot has expired, kicks ONE deduplicated background reload —
// the same never-block posture as speaker.Resolver, because IsGM runs inside
// the voice address-detection path and the interaction dispatch.
type GMIdentity struct {
	bindings TenantOperatorLister
	allow    OperatorAllowlist
	log      *slog.Logger
	now      func() time.Time

	mu       sync.Mutex
	bound    map[string]struct{} // last good binding snapshot
	expires  time.Time           // snapshot expiry; zero = never loaded
	inflight bool                // a background reload is running (dedup)
	wg       sync.WaitGroup      // tracks in-flight reloads (test synchronization)
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
		bound:    map[string]struct{}{},
	}
}

// Refresh synchronously reloads the binding snapshot — the boot-time load. A
// failure keeps the previous snapshot (initially empty, so IsGM degrades to
// the allowlist fallback) and is returned for the caller to log; it never
// needs to fail the boot.
func (g *GMIdentity) Refresh(ctx context.Context) error {
	ids, err := g.bindings.ListTenantOperatorDiscordIDs(ctx)
	if err != nil {
		return fmt.Errorf("auth: list tenant-operator bindings: %w", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.install(ids)
	return nil
}

// install replaces the snapshot with ids and restamps the expiry. Callers hold
// g.mu. Empty ids are skipped defensively — an empty snowflake must never make
// the empty (unattributed) speaker a GM.
func (g *GMIdentity) install(ids []string) {
	next := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id != "" {
			next[id] = struct{}{}
		}
	}
	g.bound = next
	g.expires = g.now().Add(gmSnapshotTTL)
}

// IsGM reports whether the Discord snowflake is a GM: a tenant-bound operator
// or an allowlisted one (the transitional union, ADR-0055). It never blocks:
// an expired snapshot is still served while one background reload runs. The
// empty (unattributed) speaker is never a GM.
func (g *GMIdentity) IsGM(discordUserID string) bool {
	if discordUserID == "" {
		return false
	}

	g.mu.Lock()
	_, bound := g.bound[discordUserID]
	if !g.now().Before(g.expires) && !g.inflight {
		g.inflight = true
		g.wg.Add(1)
		go g.refreshAsync()
	}
	g.mu.Unlock()

	return bound || g.allow.Contains(discordUserID)
}

// refreshAsync runs one background snapshot reload. A failure keeps the last
// good snapshot (warned, so a silent GM freeze is diagnosable) and re-stamps
// the expiry either way — a broken DB retries once per TTL, not per call.
func (g *GMIdentity) refreshAsync() {
	defer g.wg.Done()

	ctx, cancel := context.WithTimeout(context.Background(), gmRefreshTimeout)
	defer cancel()
	ids, err := g.bindings.ListTenantOperatorDiscordIDs(ctx)

	g.mu.Lock()
	defer g.mu.Unlock()
	g.inflight = false
	if err != nil {
		g.log.Warn("auth: refreshing tenant-operator GM bindings failed; serving the last snapshot", "err", err)
		g.expires = g.now().Add(gmSnapshotTTL)
		return
	}
	g.install(ids)
}

// Empty reports whether the union has no identity source at all — no bound
// tenant operator in the snapshot and an empty allowlist. The composition
// roots use it for the "Butler unaddressable by voice" boot warning.
func (g *GMIdentity) Empty() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.bound) == 0 && g.allow.Len() == 0
}
