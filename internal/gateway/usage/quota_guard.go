package usage

import (
	"context"
	"fmt"
	"time"

	"github.com/MrWong99/glyphoxa/internal/gateway"
	"github.com/MrWong99/glyphoxa/internal/gateway/sessionorch"
)

// TenantQuotaLookup resolves the monthly session hour quota for a tenant.
// Returns 0 for unlimited.
type TenantQuotaLookup func(ctx context.Context, tenantID string) (monthlyHours float64, err error)

// QuotaGuard wraps a session [sessionorch.Orchestrator] and enforces usage
// quotas before allowing session creation. All other methods are delegated
// directly to the inner orchestrator.
//
// Quota is checked at session start only — sessions that start within quota
// are allowed to complete even if they exceed the limit during the session.
type QuotaGuard struct {
	inner  sessionorch.Orchestrator
	usage  Store
	lookup TenantQuotaLookup
}

// Compile-time interface assertion.
var _ sessionorch.Orchestrator = (*QuotaGuard)(nil)

// NewQuotaGuard creates a QuotaGuard that checks the usage store before
// delegating to the inner orchestrator.
func NewQuotaGuard(inner sessionorch.Orchestrator, usage Store, lookup TenantQuotaLookup) *QuotaGuard {
	return &QuotaGuard{inner: inner, usage: usage, lookup: lookup}
}

// ValidateAndCreate checks the tenant's quota before creating the session.
// Returns [ErrQuotaExceeded] if the tenant's monthly session hours are at
// or above the configured limit.
func (g *QuotaGuard) ValidateAndCreate(ctx context.Context, req sessionorch.SessionRequest) (string, error) {
	monthlyHours, err := g.lookup(ctx, req.TenantID)
	if err != nil {
		return "", fmt.Errorf("usage: resolve quota for %q: %w", req.TenantID, err)
	}

	if err := g.usage.CheckQuota(ctx, req.TenantID, QuotaConfig{MonthlySessionHours: monthlyHours}); err != nil {
		return "", err
	}

	return g.inner.ValidateAndCreate(ctx, req)
}

// Transition delegates to the inner orchestrator.
func (g *QuotaGuard) Transition(ctx context.Context, sessionID string, state gateway.SessionState, errMsg string) error {
	return g.inner.Transition(ctx, sessionID, state, errMsg)
}

// RecordHeartbeat delegates to the inner orchestrator.
func (g *QuotaGuard) RecordHeartbeat(ctx context.Context, sessionID string) error {
	return g.inner.RecordHeartbeat(ctx, sessionID)
}

// ActiveSessions delegates to the inner orchestrator.
func (g *QuotaGuard) ActiveSessions(ctx context.Context, tenantID string) ([]sessionorch.Session, error) {
	return g.inner.ActiveSessions(ctx, tenantID)
}

// GetSession delegates to the inner orchestrator.
func (g *QuotaGuard) GetSession(ctx context.Context, sessionID string) (sessionorch.Session, error) {
	return g.inner.GetSession(ctx, sessionID)
}

// CleanupZombies delegates to the inner orchestrator.
func (g *QuotaGuard) CleanupZombies(ctx context.Context, timeout time.Duration) (int, error) {
	return g.inner.CleanupZombies(ctx, timeout)
}

// CleanupStalePending delegates to the inner orchestrator.
func (g *QuotaGuard) CleanupStalePending(ctx context.Context, maxAge time.Duration) (int, error) {
	return g.inner.CleanupStalePending(ctx, maxAge)
}
