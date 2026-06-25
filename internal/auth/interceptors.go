package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Authenticator validates a session token and resolves the owning operator.
// *storage.Store satisfies it via AuthenticateSession.
type Authenticator interface {
	AuthenticateSession(ctx context.Context, token string) (storage.User, error)
}

// TenantResolver resolves the tenant bound to an operator (ADR-0039 thin
// pass-through). *storage.Store satisfies it via TenantForUser.
type TenantResolver interface {
	TenantForUser(ctx context.Context, userID uuid.UUID) (uuid.UUID, error)
}

// NewAuthInterceptor validates the glyphoxa_session cookie on every Connect call
// and puts the resolved operator into the request context ([CurrentUser]).
// Unauthenticated requests are rejected with CodeUnauthenticated — INCLUDING
// reads, so the whole API is gated — EXCEPT procedures named in public, which
// are allowed through unauthenticated so they can self-handle the missing
// session (AuthService.GetCurrentUser returns CodeUnauthenticated itself, the
// SPA's 401 → /login probe).
func NewAuthInterceptor(a Authenticator, public ...string) connect.UnaryInterceptorFunc {
	publicSet := make(map[string]struct{}, len(public))
	for _, p := range public {
		publicSet[p] = struct{}{}
	}
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if token := cookieValue(req.Header(), SessionCookieName); token != "" {
				if u, err := a.AuthenticateSession(ctx, token); err == nil {
					return next(WithUser(ctx, u), req)
				}
			}
			if _, ok := publicSet[req.Spec().Procedure]; ok {
				return next(ctx, req)
			}
			return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
		}
	}
}

// NewCSRFInterceptor enforces the double-submit CSRF check (ADR-0016) on
// state-changing calls: the X-CSRF-Token header must match the glyphoxa_csrf
// cookie, else CodePermissionDenied. Reads (NO_SIDE_EFFECTS, e.g.
// GetActiveCampaign / GetCurrentUser) are exempt — they mutate nothing, and the
// CSRF cookie is not guaranteed present before the first login.
func NewCSRFInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if req.Spec().IdempotencyLevel == connect.IdempotencyNoSideEffects {
				return next(ctx, req)
			}
			cookie := cookieValue(req.Header(), CSRFCookieName)
			header := req.Header().Get("X-CSRF-Token")
			if cookie == "" || header == "" ||
				subtle.ConstantTimeCompare([]byte(cookie), []byte(header)) != 1 {
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New("CSRF token mismatch"))
			}
			return next(ctx, req)
		}
	}
}

// NewTenantInterceptor resolves the authenticated operator's bound tenant and
// puts it in the context ([TenantID]). For the single operator this is the thin
// X-Tenant-Id pass-through (ADR-0039): the tenant is resolved server-side from
// the operator, not trusted from a client header, so the multi-tenant
// membership check fills in here later without a rewrite. Unauthenticated reads
// (no operator in ctx) pass through untouched; a resolve failure is logged and
// the request proceeds without a tenant (handlers that need one fail on their
// own terms).
func NewTenantInterceptor(tr TenantResolver) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			u, ok := CurrentUser(ctx)
			if !ok {
				return next(ctx, req)
			}
			tenantID, err := tr.TenantForUser(ctx, u.ID)
			if err != nil {
				slog.Default().Warn("tenant interceptor: no tenant for operator",
					"user_id", u.ID, "err", err)
				return next(ctx, req)
			}
			return next(WithTenant(ctx, tenantID), req)
		}
	}
}

// Stack is the ordered Connect interceptor stack the web tier mounts on every
// management handler: auth (who) → CSRF (anti-forgery) → tenant (which tenant).
// Build it with [NewStack] and apply it with [Stack.HandlerOptions]. The stacked
// #68/#71 PRs reuse the same Stack so the gate is identical across services.
type Stack struct {
	interceptors []connect.Interceptor
}

// NewStack assembles the interceptor stack. public lists the fully-qualified
// procedures reachable without a valid session (the SPA's
// AuthService.GetCurrentUser boot probe).
func NewStack(a Authenticator, tr TenantResolver, public ...string) *Stack {
	return &Stack{interceptors: []connect.Interceptor{
		NewAuthInterceptor(a, public...),
		NewCSRFInterceptor(),
		NewTenantInterceptor(tr),
	}}
}

// HandlerOptions returns the connect.HandlerOption that installs the stack on a
// generated handler, e.g. authServer.Handler(stack.HandlerOptions()...).
func (s *Stack) HandlerOptions() []connect.HandlerOption {
	return []connect.HandlerOption{connect.WithInterceptors(s.interceptors...)}
}
