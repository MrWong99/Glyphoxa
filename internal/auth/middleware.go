package auth

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
)

// RequireSession is the net/http guard for the plain (non-Connect) read
// endpoints — the SSE transcript relay and its snapshot (ADR-0014 Hop-B) — and
// the plain import POST (#291). Those handlers live OUTSIDE the Connect
// interceptor chain ([NewAuthInterceptor]), so they would be unauthenticated
// without this. It mirrors the interceptor's gate: the glyphoxa_session cookie
// must resolve to an operator via [Authenticator], else the request is rejected
// 401. On success it injects the resolved operator into the request context
// ([CurrentUser]) — a pure addition mirroring the interceptor's WithUser, so a
// downstream handler (ServeImport) resolves the tenant off the session; the relay
// handlers that ignore it are unaffected.
//
// EventSource and same-origin fetch send the cookie automatically, so no custom
// header is needed; GET reads need no CSRF check (ADR-0016 exempts NO_SIDE_EFFECTS
// reads). A state-changing POST additionally wraps in [RequireCSRF].
func RequireSession(a Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := cookieValue(r.Header, SessionCookieName)
		if token == "" {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		u, err := a.AuthenticateSession(r.Context(), token)
		if err != nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
	})
}

// RequireTenant is the net/http mirror of [NewTenantInterceptor] for the plain
// (non-Connect) byte endpoints whose handlers require a tenant — the Session
// Highlight clip (#308) and image (#311) mounts. It MUST be composed INSIDE
// [RequireSession] (session first, then tenant): it reads the operator that guard
// injected ([CurrentUser]) and resolves the bound tenant SERVER-SIDE via
// [TenantResolver] (ADR-0039 thin pass-through) — never from a client header —
// then injects it ([WithTenant]) so the handler's [TenantID] read hits.
//
// This closes #408: those mounts previously wrapped only RequireSession (user,
// no tenant), so the handlers' TenantID lookup always missed and every clip/image
// request 401'd in production, while the Connect RPCs (which carry the tenant
// interceptor) returned 200 with the same cookie.
//
// Failure posture differs from the Connect interceptor deliberately. The
// interceptor proceeds tenantless and lets each handler fail on its own terms
// (some Connect procedures are tenant-agnostic). These byte handlers ALWAYS need
// a tenant, so a missing operator or an unresolved tenant rejects 401 here —
// fail-fast with the same code the handler would return anyway. The other plain
// mounts that do NOT read TenantID — the SSE relay + snapshot (relay.ServeEvents/
// ServeSnapshot) and the bundle export/import (bundle.ServeExport/ServeImport,
// which resolves the tenant off the session itself) — do NOT compose this wrapper
// and stay unchanged.
func RequireTenant(tr TenantResolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := CurrentUser(r.Context())
		if !ok {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		tenantID, err := tr.TenantForUser(r.Context(), u.ID)
		if err != nil {
			// Mirror NewTenantInterceptor's log (interceptors.go) so a 401 here is
			// never a blind wall — the exact debugging pain that made #408 need a live
			// repro. The byte handlers require a tenant, so we reject rather than proceed.
			slog.Default().Warn("require tenant: no tenant for operator", "user_id", u.ID, "err", err)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithTenant(r.Context(), tenantID)))
	})
}

// RequireCSRF is the plain-HTTP double-submit CSRF guard (ADR-0016): the mirror
// of [NewCSRFInterceptor] for a state-changing net/http POST that bypasses the
// Connect chain (the import upload). The glyphoxa_csrf cookie must
// constant-time-match the X-CSRF-Token header, else 403. RequireSession alone is
// insufficient — a same-origin cookie rides along on a cross-site POST, so the
// header the SPA sets from the script-readable cookie is what proves intent.
// Compose it INSIDE RequireSession (auth first, then anti-forgery).
func RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie := cookieValue(r.Header, CSRFCookieName)
		header := r.Header.Get("X-CSRF-Token")
		if cookie == "" || header == "" ||
			subtle.ConstantTimeCompare([]byte(cookie), []byte(header)) != 1 {
			http.Error(w, "CSRF token mismatch", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
