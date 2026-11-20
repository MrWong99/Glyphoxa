package auth

import (
	"context"
	"errors"
	"net/http"
	"time"

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
// tenant-agnostic, so an operator with no bound tenant proceeds tenantless
// (logged) and each handler fails on its own terms — unlike the byte mounts
// (mounts.go), which declare [TenantRequired]. A tenant LOOKUP failure is
// CodeUnavailable on both (#661).
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
	switch d {
	case DenyCSRF:
		return connect.CodePermissionDenied
	case DenyUnavailable:
		return connect.CodeUnavailable
	}
	return connect.CodeUnauthenticated
}

// denialError builds the connect error for a policy [Denial].
func denialError(d Denial) *connect.Error {
	return connect.NewError(connectCode(d), errors.New(d.Message()))
}

// The three single-check interceptors below expose ONE policy check each —
// the Connect mirrors of middleware.go's RequireSession / RequireCSRF /
// RequireTenant. Production handlers never compose them by hand: [Policy.Stack]
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
// and injected ([TenantID]); an unauthenticated context or an operator with no
// bound tenant (logged) proceeds without one, and a lookup failure is
// CodeUnavailable.
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
// management handler. Build it with [Policy.Stack] and apply it with
// [Stack.HandlerOptions]. Every service reuses the same Stack so the gate is
// identical across services.
type Stack struct {
	policy       *Policy
	interceptors []connect.Interceptor
}

// Stack assembles the Connect interceptor stack over this policy — the same
// policy instance the plain-mount table ([MustGuardMounts]) enforces, so the
// two transports cannot gate differently.
func (p *Policy) Stack(public ...string) *Stack {
	return &Stack{policy: p, interceptors: []connect.Interceptor{NewPolicyInterceptor(p, public...)}}
}

// MaxRequestBytes bounds one Connect request message on every service the
// stack guards by default. connect-go reads — and gzip-inflates — the whole
// request body BEFORE the policy interceptor runs, so this cap is what an
// UNAUTHENTICATED caller can make the web tier buffer per request: without it
// that is unbounded, and at the old one-size-fits-all 48 MiB it was still a
// cheap memory-exhaustion lever on every service. No request message outside
// CampaignService carries bytes, and 1 MiB is far above the largest text
// message any of them accepts. Over the cap, connect answers
// CodeResourceExhausted without buffering the body.
const MaxRequestBytes = 1 << 20

// MaxUploadRequestBytes is the read cap of the ONE service that takes image
// uploads over Connect (CampaignService: CreateMap / ReplaceMapImage /
// SetNodePortrait). The largest legitimate message is an image at the blob cap
// of 32 MiB (ADR-0048), which the SPA's JSON transport base64-inflates by 4/3
// to ~43 MiB; 48 MiB leaves headroom for the rest of the message. It is only
// reachable through [Stack.UploadHandler], which puts the session check in
// front of it.
const MaxUploadRequestBytes = 48 << 20

// UploadBodyReadTimeout is how long an AUTHENTICATED upload may take to send
// its body — the 48 MiB Connect image upload and the 192 MiB bundle import. It
// is sized so a slow home uplink still finishes (192 MiB in 30 minutes needs
// under 1 Mbit/s). The web listener arms a short default body deadline on every
// request (web.DefaultBodyReadTimeout); this longer one REPLACES it only after
// the session check has passed ([Stack.UploadHandler], a [GuardedMount] with
// BodyReadTimeout set), so a caller without a valid session never gets more
// than the short default to hold a connection open.
const UploadBodyReadTimeout = 30 * time.Minute

// HandlerOptions returns the connect.HandlerOptions that install the stack on a
// generated handler, e.g. authServer.Handler(stack.HandlerOptions()...): the
// interceptor chain plus the [MaxRequestBytes] read cap. Production mounts use
// [Stack.Handler], which adds the hard stop on the raw body; the bare options
// remain for tests that mount a service in isolation.
func (s *Stack) HandlerOptions() []connect.HandlerOption {
	return s.handlerOptions(MaxRequestBytes)
}

// bodySlack is the room left above a message cap for what is body but not
// message: protocol envelopes and the like. A body exceeding cap+bodySlack
// cannot contain an acceptable message.
const bodySlack = 4 << 10

// Handler builds a guarded service's handler with the default
// [MaxRequestBytes] cap, e.g.
//
//	path, handler := stack.Handler(authServer.Handler)
//
// On top of [Stack.HandlerOptions] it stops READING an over-cap body.
// connect-go's own cap bounds what it BUFFERS, but having hit it connect
// drains the rest of the body to io.Discard to report its size — so an
// unauthenticated caller could still make the server pull an arbitrarily long
// stream through. http.MaxBytesHandler ends the read at the cap (and closes the
// connection); connect still answers CodeResourceExhausted.
func (s *Stack) Handler(build func(...connect.HandlerOption) (string, http.Handler)) (string, http.Handler) {
	path, h := build(s.handlerOptions(MaxRequestBytes)...)
	return path, http.MaxBytesHandler(h, MaxRequestBytes+bodySlack)
}

func (s *Stack) handlerOptions(readMax int) []connect.HandlerOption {
	return []connect.HandlerOption{
		connect.WithReadMaxBytes(readMax),
		connect.WithInterceptors(s.interceptors...),
	}
}

// UploadHandler builds the handler of the service that accepts image uploads
// and returns it ALREADY wrapped in the pre-body session check. build is the
// generated service's Handler method, e.g.
//
//	path, handler := stack.UploadHandler(campaignSrv.Handler)
//
// It is the only way to obtain the [MaxUploadRequestBytes] read cap: the
// large-cap options are not exported on their own, so the cap cannot be
// mounted without the check that makes it safe.
//
// connect-go buffers the request before any interceptor runs, so the policy
// interceptor cannot stop an unauthenticated caller from making the server
// read 48 MiB. The wrapper therefore authenticates on the HEADERS alone, one
// layer out, and rejects a missing or invalid session before a single body
// byte is read. It is the session half of the policy only — the interceptor
// still runs the full gate (CSRF, tenant) on the decoded call — and the
// principal it resolves rides the request context so the interceptor does not
// authenticate the same token a second time. Only once the session has passed
// is the body deadline extended to [UploadBodyReadTimeout].
//
// It denies EVERY sessionless request, so build must be a service with no
// public procedures (CampaignService has none; the two public probes live on
// AuthService). The denial is written in the caller's RPC protocol, with the
// same code and message the interceptor would have answered.
func (s *Stack) UploadHandler(build func(...connect.HandlerOption) (string, http.Handler)) (string, http.Handler) {
	path, next := build(s.handlerOptions(MaxUploadRequestBytes)...)
	next = http.MaxBytesHandler(next, MaxUploadRequestBytes+bodySlack)
	errs := connect.NewErrorWriter()
	policy := s.policy
	if policy == nil {
		// A hand-built zero Stack has no authenticator: fail closed.
		policy = NewPolicy(nil, nil)
	}
	return path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := cookieValue(r.Header, SessionCookieName)
		u, _, deny := policy.evaluateSession(r.Context(), token, false)
		if deny != DenyNone {
			abandonBody(w, r)
			_ = errs.Write(w, r, denialError(deny))
			return
		}
		extendBodyDeadline(w, UploadBodyReadTimeout)
		next.ServeHTTP(w, r.WithContext(withVerifiedSession(r.Context(), token, u)))
	})
}

// abandonBody tells net/http not to wait for the rest of a request body the
// handler is refusing unread. Without it an HTTP/1.1 server tries to drain the
// unsent body after the handler returns so the connection can be reused — and
// a client that declared a large body and never sends it holds the connection
// until the read deadline. `Connection: close` makes the server answer and
// hang up. It is HTTP/1.1 only: on HTTP/2 a refused stream is simply reset,
// and closing the connection would kill its other streams.
func abandonBody(w http.ResponseWriter, r *http.Request) {
	if r.ProtoMajor == 1 && r.ContentLength != 0 {
		w.Header().Set("Connection", "close")
	}
}

// extendBodyDeadline replaces the listener's default body read deadline with a
// longer one, for a request that has passed authentication. Best effort: a
// ResponseWriter that cannot set deadlines (a test recorder) is left alone.
func extendBodyDeadline(w http.ResponseWriter, d time.Duration) {
	if d > 0 {
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(d))
	}
}
