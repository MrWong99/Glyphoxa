// Package auth is the single-operator authentication tier (ADR-0016 / ADR-0039):
// the net/http Discord OAuth carve-out (ADR-0015), the opaque cookie session it
// issues, and the Connect interceptor stack + context accessors that gate the
// management RPCs. The stacked Configuration (#68) and Campaign (#71) PRs reuse
// this package's exported seam: [NewStack] for the interceptor options and
// [CurrentUser] / [TenantID] to read the resolved principal out of a handler's
// context.
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
