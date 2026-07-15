package auth

import (
	"context"
	"errors"

	"connectrpc.com/connect"
)

// This file is the Connect ADAPTER over the shared auth policy (policy.go,
// issue #446). It contains no auth decisions of its own: it maps the Connect
// transport — request headers, the procedure's public/read markers, connect
// error codes — onto [Policy.Evaluate], whose verdict it enforces. The plain
// net/http mounts get the same policy through middleware.go / mounts.go.

// NewPolicyInterceptor gates every Connect call with the shared [Policy]:
// session (who) → CSRF (anti-forgery, ADR-0016) → tenant (which tenant,
// ADR-0039), injecting the resolved operator ([CurrentUser]) and tenant
// ([TenantID]) into the request context.
//
// Unauthenticated requests are rejected with CodeUnauthenticated — INCLUDING
// reads, so the whole API is gated — EXCEPT procedures named in public, which
// are allowed through unauthenticated so they can self-handle the missing
// session (AuthService.GetCurrentUser returns CodeUnauthenticated itself, the
// SPA's 401 → /login probe). CSRF applies to state-changing calls only:
// NO_SIDE_EFFECTS reads (e.g. GetActiveCampaign / GetCurrentUser) mutate
// nothing, and the CSRF cookie is not guaranteed present before the first
// login. The tenant is [TenantOptional]: some Connect procedures are
// tenant-agnostic, so a resolve failure proceeds tenantless (logged) and each
// handler fails on its own terms — unlike the byte mounts (mounts.go), which
// declare [TenantRequired].
func NewPolicyInterceptor(p *Policy, public ...string) connect.UnaryInterceptorFunc {
	publicSet := make(map[string]struct{}, len(public))
	for _, proc := range public {
		publicSet[proc] = struct{}{}
	}
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			_, isPublic := publicSet[req.Spec().Procedure]
			v := p.Evaluate(ctx, InputFromHeader(req.Header()), Check{
				Public: isPublic,
				CSRF:   req.Spec().IdempotencyLevel != connect.IdempotencyNoSideEffects,
				Tenant: TenantOptional,
			})
			if v.Deny != DenyNone {
				return nil, denialError(v.Deny)
			}
			return next(v.Context(ctx), req)
		}
	}
}

// connectCode maps a policy [Denial] onto the Connect transport — the
// interceptor-side mirror of middleware.go's [WriteDenial].
func connectCode(d Denial) connect.Code {
	if d == DenyCSRF {
		return connect.CodePermissionDenied
	}
	return connect.CodeUnauthenticated
}

// denialError builds the connect error for a policy [Denial].
func denialError(d Denial) *connect.Error {
	return connect.NewError(connectCode(d), errors.New(d.Message()))
}

// The three single-check interceptors below expose ONE policy check each —
// the Connect mirrors of middleware.go's RequireSession / RequireCSRF /
// RequireTenant. Production handlers never compose them by hand: [NewStack]
// runs the full policy in one interceptor. They remain the exported seam for
// mounting an isolated check around a service in tests (e.g. proving a
// mutation is CSRF-gated without standing up the whole stack).

// NewAuthInterceptor enforces only the session check: the glyphoxa_session
// cookie must resolve to an operator, else CodeUnauthenticated — except for
// procedures named in public, which pass through unauthenticated so they can
// self-handle the missing session. A resolved operator rides the context
// ([CurrentUser]).
func NewAuthInterceptor(a Authenticator, public ...string) connect.UnaryInterceptorFunc {
	publicSet := make(map[string]struct{}, len(public))
	for _, proc := range public {
		publicSet[proc] = struct{}{}
	}
	p := NewPolicy(a, nil)
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			_, isPublic := publicSet[req.Spec().Procedure]
			u, hasUser, deny := p.evaluateSession(ctx, cookieValue(req.Header(), SessionCookieName), isPublic)
			if deny != DenyNone {
				return nil, denialError(deny)
			}
			if hasUser {
				ctx = WithUser(ctx, u)
			}
			return next(ctx, req)
		}
	}
}

// NewCSRFInterceptor enforces only the double-submit CSRF check (ADR-0016) on
// state-changing calls; NO_SIDE_EFFECTS reads are exempt.
func NewCSRFInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			deny := evaluateCSRF(
				cookieValue(req.Header(), CSRFCookieName),
				req.Header().Get("X-CSRF-Token"),
				req.Spec().IdempotencyLevel != connect.IdempotencyNoSideEffects,
			)
			if deny != DenyNone {
				return nil, denialError(deny)
			}
			return next(ctx, req)
		}
	}
}

// NewTenantInterceptor enforces only the [TenantOptional] tenant resolution:
// an authenticated operator's bound tenant is resolved server-side (ADR-0039)
// and injected ([TenantID]); an unauthenticated context or a resolve failure
// (logged) proceeds without one.
func NewTenantInterceptor(tr TenantResolver) connect.UnaryInterceptorFunc {
	p := NewPolicy(nil, tr)
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			u, hasUser := CurrentUser(ctx)
			id, hasTenant, deny := p.evaluateTenant(ctx, u, hasUser, TenantOptional)
			if deny != DenyNone {
				return nil, denialError(deny)
			}
			if hasTenant {
				ctx = WithTenant(ctx, id)
			}
			return next(ctx, req)
		}
	}
}

// Stack is the Connect interceptor stack the web tier mounts on every
// management handler. Build it with [NewStack] and apply it with
// [Stack.HandlerOptions]. Every service reuses the same Stack so the gate is
// identical across services.
type Stack struct {
	interceptors []connect.Interceptor
}

// NewStack assembles the interceptor stack over a fresh [Policy]. public
// lists the fully-qualified procedures reachable without a valid session (the
// SPA's AuthService.GetCurrentUser boot probe). The composition root, which
// also feeds the plain-mount table, builds the policy once and uses
// [Policy.Stack] instead.
func NewStack(a Authenticator, tr TenantResolver, public ...string) *Stack {
	return NewPolicy(a, tr).Stack(public...)
}

// Stack assembles the Connect interceptor stack over this policy — the same
// policy instance the plain-mount table ([MustGuardMounts]) enforces, so the
// two transports cannot gate differently.
func (p *Policy) Stack(public ...string) *Stack {
	return &Stack{interceptors: []connect.Interceptor{NewPolicyInterceptor(p, public...)}}
}

// HandlerOptions returns the connect.HandlerOption that installs the stack on a
// generated handler, e.g. authServer.Handler(stack.HandlerOptions()...).
func (s *Stack) HandlerOptions() []connect.HandlerOption {
	return []connect.HandlerOption{connect.WithInterceptors(s.interceptors...)}
}
