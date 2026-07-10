package auth

import (
	"crypto/subtle"
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
