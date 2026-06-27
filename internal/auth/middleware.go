package auth

import "net/http"

// RequireSession is the net/http guard for the plain (non-Connect) read
// endpoints — the SSE transcript relay and its snapshot (ADR-0014 Hop-B). Those
// handlers live OUTSIDE the Connect interceptor chain ([NewAuthInterceptor]), so
// they would be unauthenticated without this. It mirrors the interceptor's gate:
// the glyphoxa_session cookie must resolve to an operator via [Authenticator],
// else the request is rejected 401.
//
// EventSource and same-origin fetch send the cookie automatically, so no custom
// header is needed; both are GET reads, so there is no CSRF check (ADR-0016
// exempts NO_SIDE_EFFECTS reads).
func RequireSession(a Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := cookieValue(r.Header, SessionCookieName)
		if token == "" {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if _, err := a.AuthenticateSession(r.Context(), token); err != nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
