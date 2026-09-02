package auth

import (
	"context"
	"errors"
	"net/http"

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
//
// It is a full [connect.Interceptor], not a UnaryInterceptorFunc: a
// UnaryInterceptorFunc's WrapStreamingHandler is a PASS-THROUGH, so mounting a
// server-streaming procedure (the #592 chat exchange is the first) on a
// unary-only stack would serve it with NO session, CSRF, or tenant check. The
// streaming wrap runs the identical policy evaluation on the stream's request
// headers; a Connect stream open is an HTTP POST, so the CSRF double-submit
// check applies to it exactly as to a unary mutation.
func NewPolicyInterceptor(p *Policy, public ...string) connect.Interceptor {
	publicSet := make(map[string]struct{}, len(public))
	for _, proc := range public {
		publicSet[proc] = struct{}{}
	}
	return policyInterceptor{p: p, public: publicSet}
}

// policyInterceptor is [NewPolicyInterceptor]'s value: one shared [Policy]
// evaluation mapped onto both Connect call shapes.
type policyInterceptor struct {
	p      *Policy
	public map[string]struct{}
}

// evaluate runs the shared policy for one procedure against its request
// headers, returning the verdict the two wraps enforce identically.
func (i policyInterceptor) evaluate(ctx context.Context, procedure string, header http.Header, idem connect.IdempotencyLevel) Verdict {
	_, isPublic := i.public[procedure]
	return i.p.Evaluate(ctx, InputFromHeader(header), Check{
		Public: isPublic,
		CSRF:   idem != connect.IdempotencyNoSideEffects,
		Tenant: TenantOptional,
	})
}

// WrapUnary implements [connect.Interceptor] for unary calls — the behavior
// every management RPC has always had.
func (i policyInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		v := i.evaluate(ctx, req.Spec().Procedure, req.Header(), req.Spec().IdempotencyLevel)
		if v.Deny != DenyNone {
			return nil, denialError(v.Deny)
		}
		return next(v.Context(ctx), req)
	}
}

// WrapStreamingClient implements [connect.Interceptor]. The policy gates the
// SERVER side; this process's outbound Connect clients (none today) pass
// through untouched.
func (i policyInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

// WrapStreamingHandler implements [connect.Interceptor]: the same policy
// evaluation as WrapUnary, on the stream's request headers, before the handler
// sees the stream.
func (i policyInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		v := i.evaluate(ctx, conn.Spec().Procedure, conn.RequestHeader(), conn.Spec().IdempotencyLevel)
		if v.Deny != DenyNone {
			return denialError(v.Deny)
		}
		return next(v.Context(ctx), conn)
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

// MaxRequestBytes bounds one Connect request message on every service the stack
// guards. Without it connect-go reads — and gzip-inflates — an unbounded body
// BEFORE the policy interceptor runs, so an unauthenticated client could pin
// the web tier's memory with a multi-gigabyte POST. The largest legitimate
// message is a map image upload (CreateMap / ReplaceMapImage) at the blob cap of
// 32 MiB (ADR-0048), which the SPA's JSON transport base64-inflates by 4/3 to
// ~43 MiB; 48 MiB leaves headroom for the rest of the message. Over the cap,
// connect answers CodeResourceExhausted without buffering the body.
const MaxRequestBytes = 48 << 20

// HandlerOptions returns the connect.HandlerOptions that install the stack on a
// generated handler, e.g. authServer.Handler(stack.HandlerOptions()...): the
// interceptor chain plus the [MaxRequestBytes] read cap.
func (s *Stack) HandlerOptions() []connect.HandlerOption {
	return []connect.HandlerOption{
		connect.WithReadMaxBytes(MaxRequestBytes),
		connect.WithInterceptors(s.interceptors...),
	}
}
