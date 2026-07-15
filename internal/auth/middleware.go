package auth

import (
	"net/http"
)

// This file is the plain net/http ADAPTER over the shared auth policy
// (policy.go, issue #446). Like interceptors.go it contains no auth decisions:
// each wrapper maps one policy check onto the HTTP transport. Production
// mounts do not compose these by hand anymore — they declare a row in the
// guarded mount table ([MustGuardMounts], mounts.go), which runs the full
// [Policy.Evaluate] per request. The wrappers remain the exported seam for
// composing a single check around a handler in tests and one-off tools.

// WriteDenial maps a policy [Denial] onto the HTTP transport — 401 for a
// missing/invalid session (or an unresolvable required tenant), 403 for a
// CSRF failure — with the same message text the Connect adapter returns.
// Call it only with a real denial, never [DenyNone].
func WriteDenial(w http.ResponseWriter, d Denial) {
	status := http.StatusUnauthorized
	if d == DenyCSRF {
		status = http.StatusForbidden
	}
	http.Error(w, d.Message(), status)
}

// RequireSession is the net/http session guard: the glyphoxa_session cookie
// must resolve to an operator via [Authenticator], else 401. On success it
// injects the resolved operator into the request context ([CurrentUser]) — a
// pure addition, so handlers that ignore it are unaffected. EventSource and
// same-origin fetch send the cookie automatically, so no custom header is
// needed; GET reads need no CSRF check (ADR-0016 exempts reads).
func RequireSession(a Authenticator, next http.Handler) http.Handler {
	p := NewPolicy(a, nil)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _, deny := p.evaluateSession(r.Context(), cookieValue(r.Header, SessionCookieName), false)
		if deny != DenyNone {
			WriteDenial(w, deny)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
	})
}

// RequireTenant applies the [TenantRequired] posture: it reads the operator an
// upstream [RequireSession] injected ([CurrentUser]), resolves the bound
// tenant SERVER-SIDE via [TenantResolver] (ADR-0039 — never from a client
// header), and injects it ([WithTenant]) so the handler's [TenantID] read
// hits. A missing operator or an unresolved tenant rejects 401 — the byte
// handlers ALWAYS need a tenant, so this fails fast with the same code the
// handler would return anyway (the deliberate contrast with the Connect
// stack's [TenantOptional], where tenant-agnostic procedures proceed).
//
// This gate closes #408: the clip/image mounts previously composed only the
// session check (user, no tenant), so the handlers' TenantID lookup always
// missed and every request 401'd in production, while the Connect RPCs
// returned 200 with the same cookie. The guarded mount table (mounts.go) now
// makes that subset impossible to compose silently.
func RequireTenant(tr TenantResolver, next http.Handler) http.Handler {
	p := NewPolicy(nil, tr)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, hasUser := CurrentUser(r.Context())
		id, _, deny := p.evaluateTenant(r.Context(), u, hasUser, TenantRequired)
		if deny != DenyNone {
			WriteDenial(w, deny)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithTenant(r.Context(), id)))
	})
}

// RequireCSRF is the plain-HTTP double-submit guard (ADR-0016): the
// glyphoxa_csrf cookie must constant-time-match the X-CSRF-Token header, else
// 403. A session alone is insufficient for a state-changing POST — a
// same-origin cookie rides along on a cross-site POST, so the header the SPA
// sets from the script-readable cookie is what proves intent. Compose it
// INSIDE RequireSession (auth first, then anti-forgery).
func RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deny := evaluateCSRF(cookieValue(r.Header, CSRFCookieName), r.Header.Get("X-CSRF-Token"), true)
		if deny != DenyNone {
			WriteDenial(w, deny)
			return
		}
		next.ServeHTTP(w, r)
	})
}
