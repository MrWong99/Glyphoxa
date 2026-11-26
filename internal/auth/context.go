// Package auth is the single-operator authentication tier (ADR-0016 / ADR-0039):
// the net/http Discord OAuth carve-out (ADR-0015), the opaque cookie session it
// issues, and ONE transport-agnostic policy (#446, policy.go) that gates both
// transports — the Connect interceptor stack ([Policy.Stack]) over the management
// RPCs and the guarded plain-mount table ([MustGuardMounts]) over the byte
// endpoints. [CurrentUser] / [TenantID] read the resolved principal out of a
// handler's context.
package auth

import (
	"context"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// ctxKey is the private context key type for the resolved principal, so no other
// package can collide with or overwrite these context values.
type ctxKey int

const (
	userKey ctxKey = iota
	tenantKey
	guestKey
)

// WithUser returns a copy of ctx carrying the authenticated operator. The auth
// interceptor calls it after validating the session cookie; tests use it to
// inject a principal without standing up the interceptor.
func WithUser(ctx context.Context, u storage.User) context.Context {
	return context.WithValue(ctx, userKey, u)
}

// CurrentUser returns the authenticated operator carried in ctx, and false when
// the request is unauthenticated. RPC handlers (e.g. AuthService.GetCurrentUser,
// and #68/#71's mutations) use it to read the caller.
func CurrentUser(ctx context.Context) (storage.User, bool) {
	u, ok := ctx.Value(userKey).(storage.User)
	return u, ok
}

// WithTenant returns a copy of ctx carrying the resolved tenant id. The tenant
// interceptor calls it; the single-operator pass-through resolves the operator's
// bound tenant (ADR-0039).
func WithTenant(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, tenantKey, id)
}

// TenantID returns the resolved tenant id carried in ctx, and false when none
// was resolved (an unauthenticated request, or an operator with no bound tenant).
func TenantID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(tenantKey).(uuid.UUID)
	return id, ok
}

// GuestScope is what a request acting as a Campaign Guest is confined to
// (ADR-0065): ONE Campaign of the host Tenant, and the Guest Rights its GM
// granted. While it is present, [TenantID] is the HOST Tenant — so every
// handler a guest can reach must scope to CampaignID, never to the Tenant.
type GuestScope struct {
	CampaignID uuid.UUID
	Rights     RightSet
}

// WithGuest returns a copy of ctx marked as acting as a Campaign Guest. The
// policy calls it after re-authorizing the guest row for this request; tests
// use it to inject a guest principal without standing up the interceptor.
func WithGuest(ctx context.Context, g GuestScope) context.Context {
	return context.WithValue(ctx, guestKey, g)
}

// Guest returns the request's guest scope, and false for a Tenant's own
// operator — the ordinary case, for which nothing about a handler changes.
func Guest(ctx context.Context) (GuestScope, bool) {
	g, ok := ctx.Value(guestKey).(GuestScope)
	return g, ok
}

// GuestLacks reports whether the request is a Campaign Guest WITHOUT the
// right — the test a guest-reachable handler makes before returning something
// only that right covers. False for a non-guest: a GM lacks nothing.
func GuestLacks(ctx context.Context, r GuestRight) bool {
	g, ok := Guest(ctx)
	return ok && !g.Rights.Has(r)
}
