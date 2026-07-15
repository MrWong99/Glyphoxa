package auth

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// This file is the single transport-agnostic auth policy (issue #446): session
// authentication, the double-submit CSRF check (ADR-0016), tenant resolution
// (ADR-0039), and the denial posture for each — decided ONCE, here. The Connect
// interceptor (interceptors.go) and the plain net/http guards (middleware.go,
// mounts.go) are thin adapters that map transport specifics (headers, connect
// codes vs HTTP statuses) onto [Policy.Evaluate]. Before this seam the two
// transports hand-synced copies of the same gate, and they drifted: #408
// shipped because the clip/image mounts composed only the session check (no
// tenant), so those endpoints 401'd in production while the Connect RPCs
// passed on the same cookie.

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

// Policy owns the auth decisions both transports enforce. Construct with
// [NewPolicy]; evaluate with [Policy.Evaluate].
type Policy struct {
	authn   Authenticator
	tenants TenantResolver
}

// NewPolicy builds the shared policy. tr may be nil only when every evaluation
// uses [TenantNone] (a nil resolver fails tenant resolution closed).
func NewPolicy(a Authenticator, tr TenantResolver) *Policy {
	return &Policy{authn: a, tenants: tr}
}

// Input is the transport-free request evidence the policy inspects: the two
// cookie values and the CSRF echo header. Adapters build it with
// [InputFromHeader]; tests may construct it directly.
type Input struct {
	SessionToken string // glyphoxa_session cookie value, "" when absent
	CSRFCookie   string // glyphoxa_csrf cookie value, "" when absent
	CSRFHeader   string // X-CSRF-Token header value, "" when absent
}

// InputFromHeader extracts the policy [Input] from a header set. It works for
// both a net/http request's Header and a Connect request's Header() (which
// carries the inbound Cookie header), so cookie extraction cannot drift
// between the transports either.
func InputFromHeader(h http.Header) Input {
	return Input{
		SessionToken: cookieValue(h, SessionCookieName),
		CSRFCookie:   cookieValue(h, CSRFCookieName),
		CSRFHeader:   h.Get("X-CSRF-Token"),
	}
}

// TenantMode declares what a mount/procedure needs from tenant resolution.
// The zero value is deliberately NOT a valid mode: a [GuardedMount] row that
// forgets to declare one fails [MustGuardMounts] at startup instead of
// silently composing the subset that caused #408.
type TenantMode int

const (
	tenantUnspecified TenantMode = iota // zero value — rejected loudly, fails closed
	// TenantNone skips tenant resolution entirely. The import POST uses it:
	// its handler resolves the tenant off the session itself (#291).
	TenantNone
	// TenantOptional resolves the operator's tenant when authenticated and
	// proceeds tenantless on failure (logged) — the Connect posture, where
	// some procedures are tenant-agnostic and each handler fails on its own
	// terms.
	TenantOptional
	// TenantRequired rejects as unauthenticated when the tenant cannot be
	// resolved — the byte-mount posture (clip/image/SSE/snapshot/export):
	// those handlers ALWAYS need a tenant, so this fails fast with the same
	// code the handler would return anyway.
	TenantRequired
)

// Check declares which checks one mount or procedure requires. The Connect
// adapter derives CSRF from the procedure's IdempotencyLevel; the plain-HTTP
// guard derives it from the request method — both are transport spellings of
// the one rule "state-changing requires the double-submit pair".
type Check struct {
	// Public marks a procedure reachable without a valid session so it can
	// self-handle the missing principal (AuthService.GetCurrentUser, the
	// SPA's 401 → /login probe). A valid session still injects the operator.
	Public bool
	// CSRF requires the double-submit pair (ADR-0016).
	CSRF bool
	// Tenant is the tenant-resolution posture. Must be set explicitly.
	Tenant TenantMode
}

// Denial classifies why the policy rejected a request. Adapters map it onto
// their transport: connect codes (interceptors.go) or HTTP statuses
// (middleware.go WriteDenial).
type Denial int

const (
	// DenyNone means the request passed every required check.
	DenyNone Denial = iota
	// DenyUnauthenticated is the missing/invalid-session (or unresolvable
	// required tenant) rejection: CodeUnauthenticated / 401.
	DenyUnauthenticated
	// DenyCSRF is the double-submit failure: CodePermissionDenied / 403.
	DenyCSRF
)

// Message is the user-facing rejection text, identical on both transports.
func (d Denial) Message() string {
	switch d {
	case DenyCSRF:
		return "csrf check failed, retry"
	default:
		return "please sign in"
	}
}

// Verdict is the policy outcome for one request: either a [Denial], or the
// resolved principal/tenant to inject into the request context via
// [Verdict.Context].
type Verdict struct {
	Deny      Denial
	User      storage.User
	HasUser   bool
	Tenant    uuid.UUID
	HasTenant bool
}

// Context injects the verdict's resolved operator ([WithUser]) and tenant
// ([WithTenant]) into ctx. Both adapters call it, so context injection cannot
// drift between the transports.
func (v Verdict) Context(ctx context.Context) context.Context {
	if v.HasUser {
		ctx = WithUser(ctx, v.User)
	}
	if v.HasTenant {
		ctx = WithTenant(ctx, v.Tenant)
	}
	return ctx
}

// Evaluate runs the full gate in the canonical order — session (who) → CSRF
// (anti-forgery) → tenant (which tenant) — and returns the verdict. It is the
// ONLY decision path: the Connect interceptor and the plain-mount guard both
// call it per request, so a check cannot exist on one transport and not the
// other (the #408 drift class).
func (p *Policy) Evaluate(ctx context.Context, in Input, c Check) Verdict {
	u, hasUser, deny := p.evaluateSession(ctx, in.SessionToken, c.Public)
	if deny != DenyNone {
		return Verdict{Deny: deny}
	}
	// CSRF applies regardless of authentication state: a public state-changing
	// procedure still needs the double-submit pair.
	if deny := evaluateCSRF(in.CSRFCookie, in.CSRFHeader, c.CSRF); deny != DenyNone {
		return Verdict{Deny: deny}
	}
	tenant, hasTenant, deny := p.evaluateTenant(ctx, u, hasUser, c.Tenant)
	if deny != DenyNone {
		return Verdict{Deny: deny}
	}
	return Verdict{User: u, HasUser: hasUser, Tenant: tenant, HasTenant: hasTenant}
}

// evaluateSession validates the session token and resolves the operator. A
// missing/invalid token on a non-public check denies; on a public check the
// request proceeds without a principal (the procedure self-handles it).
func (p *Policy) evaluateSession(ctx context.Context, token string, public bool) (storage.User, bool, Denial) {
	if token != "" && p.authn != nil {
		if u, err := p.authn.AuthenticateSession(ctx, token); err == nil {
			return u, true, DenyNone
		}
	}
	if public {
		return storage.User{}, false, DenyNone
	}
	return storage.User{}, false, DenyUnauthenticated
}

// evaluateCSRF is the double-submit check (ADR-0016): the glyphoxa_csrf cookie
// must constant-time-match the X-CSRF-Token header. The cookie is not
// guaranteed present before the first login, which is why reads are exempt
// (required=false) rather than treating an absent pair as a match.
func evaluateCSRF(cookie, header string, required bool) Denial {
	if !required {
		return DenyNone
	}
	if cookie == "" || header == "" ||
		subtle.ConstantTimeCompare([]byte(cookie), []byte(header)) != 1 {
		return DenyCSRF
	}
	return DenyNone
}

// evaluateTenant applies the declared [TenantMode]. An unspecified (zero)
// mode fails CLOSED — [MustGuardMounts] rejects it at startup, but if one
// slips through a hand-built [Check] it must deny everything, never silently
// skip the tenant gate.
func (p *Policy) evaluateTenant(ctx context.Context, u storage.User, hasUser bool, mode TenantMode) (uuid.UUID, bool, Denial) {
	switch mode {
	case TenantNone:
		return uuid.Nil, false, DenyNone
	case TenantOptional:
		if !hasUser {
			return uuid.Nil, false, DenyNone
		}
		if id, ok := p.resolveTenant(ctx, u); ok {
			return id, true, DenyNone
		}
		return uuid.Nil, false, DenyNone
	case TenantRequired:
		if !hasUser {
			return uuid.Nil, false, DenyUnauthenticated
		}
		if id, ok := p.resolveTenant(ctx, u); ok {
			return id, true, DenyNone
		}
		return uuid.Nil, false, DenyUnauthenticated
	default:
		return uuid.Nil, false, DenyUnauthenticated
	}
}

// resolveTenant resolves the operator's bound tenant SERVER-SIDE (ADR-0039
// thin pass-through) — never from a client header — so the multi-tenant
// membership check fills in here later without a rewrite. A failure is warned
// so a downstream 401 is never a blind wall — the exact debugging pain that
// made #408 need a live repro.
func (p *Policy) resolveTenant(ctx context.Context, u storage.User) (uuid.UUID, bool) {
	if p.tenants == nil {
		slog.Default().Warn("auth policy: tenant check declared but no TenantResolver wired", "user_id", u.ID)
		return uuid.Nil, false
	}
	id, err := p.tenants.TenantForUser(ctx, u.ID)
	if err != nil {
		slog.Default().Warn("auth policy: no tenant for operator", "user_id", u.ID, "err", err)
		return uuid.Nil, false
	}
	return id, true
}
