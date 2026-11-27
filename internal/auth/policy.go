package auth

import (
	"context"
	"crypto/subtle"
	"errors"
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

// WorkspaceResolver decides what a session acts as (ADR-0065): the user's own
// Tenant, or — as a Campaign Guest — one Campaign of a host Tenant. It is
// asked on EVERY request with the session token the request presented, so a
// guest who was removed, or whose rights changed, is re-authorized on their
// very next call. storage.ErrNotFound means the user has no workspace at all.
// [Workspaces] is the production implementation.
type WorkspaceResolver interface {
	ResolveWorkspace(ctx context.Context, u storage.User, sessionToken string) (storage.Workspace, error)
}

// Policy owns the auth decisions both transports enforce. Construct with
// [NewPolicy]; evaluate with [Policy.Evaluate].
type Policy struct {
	authn   Authenticator
	tenants TenantResolver
	// workspaces, when wired ([Policy.WithWorkspaces]), replaces tenants: the
	// policy then resolves a WORKSPACE and runs the guest gate.
	workspaces WorkspaceResolver
}

// NewPolicy builds the shared policy. tr may be nil only when every evaluation
// uses [TenantNone] (a nil resolver fails tenant resolution closed).
func NewPolicy(a Authenticator, tr TenantResolver) *Policy {
	return &Policy{authn: a, tenants: tr}
}

// WithWorkspaces makes the policy resolve a workspace instead of just a Tenant
// (ADR-0065) and returns it for chaining. It is an explicit opt-in — not a
// type assertion on the [TenantResolver] — so a policy can only ever produce a
// guest principal when its composition root said so, and with it the guest
// gate: a request that resolves to a Campaign Guest is refused unless its
// [Check] declares a [GuestPosture] the guest's rights satisfy. Without it no
// request is ever a guest. A nil resolver leaves the policy as it was.
func (p *Policy) WithWorkspaces(wr WorkspaceResolver) *Policy {
	p.workspaces = wr
	return p
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
	// proceeds tenantless when the operator has none bound (logged) — the
	// Connect posture, where some procedures are tenant-agnostic and each
	// handler fails on its own terms. A resolver FAILURE (anything but
	// storage.ErrNotFound) still denies as unavailable (#661).
	TenantOptional
	// TenantRequired rejects as unauthenticated when the operator has no
	// bound tenant — the byte-mount posture (clip/image/SSE/snapshot/export):
	// those handlers ALWAYS need a tenant, so this fails fast with the same
	// code the handler would return anyway. A resolver FAILURE is unavailable
	// (503), not unauthenticated (#661).
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
	// Guest is what the mount/procedure requires of a Campaign Guest
	// (ADR-0065). The zero value is GM-only: a guest is denied.
	Guest GuestPosture
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
	// DenyUnavailable means the session — or the operator's tenant (#661) —
	// could not be checked at all: the authenticator or tenant resolver failed
	// for a reason other than "no such row" (a DB outage, a pool timeout):
	// CodeUnavailable / 503. It is NOT an
	// unauthenticated answer: a presented token that could not be verified must
	// not read as "please sign in" (the SPA logs the operator out on that).
	DenyUnavailable
	// DenyGuest is the guest gate's refusal (ADR-0065): the session acts as a
	// Campaign Guest and this mount/procedure is GM-only, or needs a Guest
	// Right the guest does not hold: CodePermissionDenied / 403. One denial
	// for both, so the answer does not reveal which rights exist.
	DenyGuest
)

// Message is the user-facing rejection text, identical on both transports.
func (d Denial) Message() string {
	switch d {
	case DenyCSRF:
		return "csrf check failed, retry"
	case DenyUnavailable:
		return "sign-in check temporarily unavailable, retry"
	case DenyGuest:
		return "your access to this campaign does not include this"
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
	// Guest is set (IsGuest) when the session acts as a Campaign Guest: Tenant
	// is then the HOST Tenant.
	Guest   GuestScope
	IsGuest bool
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
	if v.IsGuest {
		ctx = WithGuest(ctx, v.Guest)
	}
	return ctx
}

// Evaluate runs the full gate in the canonical order — session (who) → CSRF
// (anti-forgery) → workspace (which Tenant, and as whom) → guest gate — and
// returns the verdict. It is the ONLY decision path: the Connect interceptor
// and the plain-mount guard both call it per request, so a check cannot exist
// on one transport and not the other (the #408 drift class).
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
	if p.workspaces != nil && hasUser {
		return p.evaluateWorkspace(ctx, u, in.SessionToken, c)
	}
	tenant, hasTenant, deny := p.evaluateTenant(ctx, u, hasUser, c.Tenant)
	if deny != DenyNone {
		return Verdict{Deny: deny}
	}
	return Verdict{User: u, HasUser: hasUser, Tenant: tenant, HasTenant: hasTenant}
}

// evaluateWorkspace is the tenant step of a policy with a [WorkspaceResolver]
// (ADR-0065), plus the guest gate. The workspace is resolved for EVERY
// authenticated request — including a [TenantNone] one, which injects no
// Tenant but must still learn whether the caller is a guest, or a guest could
// reach a mount merely because that mount resolves its Tenant itself.
//
// The denial postures mirror [Policy.evaluateTenant]: a resolver FAILURE is
// [DenyUnavailable] (#661), no workspace at all is "no tenant" (denied only by
// [TenantRequired]), and an undeclared tenant mode fails closed. A guest
// workspace then has to pass c.Guest, whose zero value admits nobody.
func (p *Policy) evaluateWorkspace(ctx context.Context, u storage.User, token string, c Check) Verdict {
	switch c.Tenant {
	case TenantNone, TenantOptional, TenantRequired:
	default:
		return Verdict{Deny: DenyUnauthenticated}
	}
	v := Verdict{User: u, HasUser: true}
	w, err := p.workspaces.ResolveWorkspace(ctx, u, token)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		slog.Default().Warn("auth policy: no workspace for user", "user_id", u.ID)
		if c.Tenant == TenantRequired {
			return Verdict{Deny: DenyUnauthenticated}
		}
		return v
	case err != nil:
		slog.Default().Warn("auth policy: resolve workspace", "user_id", u.ID, "err", err)
		return Verdict{Deny: DenyUnavailable}
	}
	if w.Guest {
		scope := GuestScope{CampaignID: w.CampaignID, Rights: RightSetFromStrings(w.Rights)}
		if !c.Guest.Admits(scope.Rights) {
			return Verdict{Deny: DenyGuest}
		}
		v.Guest, v.IsGuest = scope, true
	}
	if c.Tenant != TenantNone {
		v.Tenant, v.HasTenant = w.TenantID, true
	}
	return v
}

// evaluateSession validates the session token and resolves the operator. A
// missing/invalid token on a non-public check denies; on a public check the
// request proceeds without a principal (the procedure self-handles it). An
// authenticator FAILURE (anything but storage.ErrNotFound) is [DenyUnavailable]
// on both kinds of check: the token was presented and could not be verified,
// which is neither "signed in" nor "not signed in".
func (p *Policy) evaluateSession(ctx context.Context, token string, public bool) (storage.User, bool, Denial) {
	// A pre-body check ([Stack.UploadHandler]) already verified this
	// exact token for this request: reuse its principal instead of a second
	// authenticator round trip.
	if u, ok := verifiedSession(ctx, token); ok {
		return u, true, DenyNone
	}
	if token != "" && p.authn != nil {
		u, err := p.authn.AuthenticateSession(ctx, token)
		switch {
		case err == nil:
			return u, true, DenyNone
		case !errors.Is(err, storage.ErrNotFound):
			slog.Default().Warn("auth policy: authenticate session", "err", err)
			return storage.User{}, false, DenyUnavailable
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
		id, ok, err := p.resolveTenant(ctx, u)
		if err != nil {
			return uuid.Nil, false, DenyUnavailable
		}
		return id, ok, DenyNone
	case TenantRequired:
		if !hasUser {
			return uuid.Nil, false, DenyUnauthenticated
		}
		id, ok, err := p.resolveTenant(ctx, u)
		switch {
		case err != nil:
			return uuid.Nil, false, DenyUnavailable
		case !ok:
			return uuid.Nil, false, DenyUnauthenticated
		}
		return id, true, DenyNone
	default:
		return uuid.Nil, false, DenyUnauthenticated
	}
}

// resolveTenant resolves the operator's bound tenant SERVER-SIDE (ADR-0039
// thin pass-through) — never from a client header — so the multi-tenant
// membership check fills in here later without a rewrite. It distinguishes the
// two ways a tenant can be missing (#661), exactly as [Policy.evaluateSession]
// does for the session: ok=false with a nil error means the operator has NO
// bound tenant (storage.ErrNotFound, or no resolver wired — both answer "no
// tenant"); a non-nil error means the lookup itself FAILED (a pool timeout, a
// dropped connection), which callers must answer as [DenyUnavailable] — a
// transient database error must never read as "please sign in" (the SPA signs
// the operator out on that) nor let a Connect call proceed tenantless into a
// misleading handler error. Both outcomes are warned so a downstream denial is
// never a blind wall — the exact debugging pain that made #408 need a live
// repro.
func (p *Policy) resolveTenant(ctx context.Context, u storage.User) (uuid.UUID, bool, error) {
	if p.tenants == nil {
		slog.Default().Warn("auth policy: tenant check declared but no TenantResolver wired", "user_id", u.ID)
		return uuid.Nil, false, nil
	}
	id, err := p.tenants.TenantForUser(ctx, u.ID)
	switch {
	case err == nil:
		return id, true, nil
	case errors.Is(err, storage.ErrNotFound):
		slog.Default().Warn("auth policy: no tenant for operator", "user_id", u.ID)
		return uuid.Nil, false, nil
	}
	slog.Default().Warn("auth policy: resolve tenant", "user_id", u.ID, "err", err)
	return uuid.Nil, false, err
}

// verifiedSessionKey carries a session the pre-body check has already verified
// for this request ([Stack.UploadHandler]). It is unexported and
// bound to the exact token it was verified for, so nothing outside this
// package can mint one and a request that somehow presents a different token
// downstream is authenticated from scratch.
type verifiedSessionKey struct{}

type verifiedSessionValue struct {
	token string
	user  storage.User
}

func withVerifiedSession(ctx context.Context, token string, u storage.User) context.Context {
	return context.WithValue(ctx, verifiedSessionKey{}, verifiedSessionValue{token: token, user: u})
}

func verifiedSession(ctx context.Context, token string) (storage.User, bool) {
	v, ok := ctx.Value(verifiedSessionKey{}).(verifiedSessionValue)
	if !ok || token == "" || subtle.ConstantTimeCompare([]byte(v.token), []byte(token)) != 1 {
		return storage.User{}, false
	}
	return v.user, true
}
