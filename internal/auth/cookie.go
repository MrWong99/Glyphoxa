package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Cookie names (ADR-0016). The session cookie is HttpOnly so script can't read
// the bearer; the CSRF cookie is deliberately NOT HttpOnly so the SPA can echo
// it back in the X-CSRF-Token header (the double-submit pattern).
const (
	// SessionCookieName carries the opaque session token (HttpOnly).
	SessionCookieName = "glyphoxa_session"
	// CSRFCookieName carries the double-submit CSRF token (readable by script).
	CSRFCookieName = "glyphoxa_csrf"
	// stateCookieName carries the short-lived OAuth state nonce.
	stateCookieName = "glyphoxa_oauth_state"
)

// newToken mints an opaque, high-entropy token from crypto/rand — the session
// and CSRF secrets, and the OAuth state nonce. base64url so it is cookie-safe.
// NOT a JWT: server-side sessions revoke with a row delete (ADR-0016).
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: read random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewToken is the exported seam over [newToken] for callers outside the OAuth
// flow that must mint a session/CSRF secret with the same crypto/rand entropy as
// the login path — the GLYPHOXA_DEV_MODE auto-auth boot (ADR-0041) issues its
// dev session this way.
func NewToken() (string, error) {
	return newToken()
}

// cookieValue reads a single cookie value out of a request/response header set,
// returning "" when absent. It works for both a net/http request and a Connect
// request's Header() (which carries the inbound Cookie header).
func cookieValue(h http.Header, name string) string {
	r := http.Request{Header: h}
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// SecureCookies reports whether the session, CSRF and OAuth-state cookies must
// carry the Secure attribute (ADR-0016), derived ONCE from the deployment's
// public URL — the configured Discord OAuth redirect URL — rather than from
// each request. The Go process sits behind a TLS-terminating reverse proxy
// (ADR-0039) and only ever sees cleartext, so the per-request alternative is
// the X-Forwarded-Proto header, which any client can set or omit and which a
// proxy that does not overwrite it passes straight through (#662): a missing
// header silently downgraded the session cookie to non-Secure. The redirect
// URL is operator configuration and is by construction the scheme browsers
// reach the instance on (Discord redirects them to it). An https URL yields
// Secure cookies; http (local dev, http://localhost) and an unset or
// unparsable value (GLYPHOXA_DEV_MODE, which needs no OAuth) yield non-Secure
// ones the browser will still store over cleartext.
func SecureCookies(oauthRedirectURL string) bool {
	u, err := url.Parse(strings.TrimSpace(oauthRedirectURL))
	return err == nil && strings.EqualFold(u.Scheme, "https")
}

// EdgeConfig is what the auth tier must be TOLD about the reverse proxy in
// front of it (ADR-0039), because the alternative is believing request headers
// a client can forge. It is a required constructor argument of [NewOAuth] and
// [NewAuthServer] — not a setter — so the composition root cannot forget to
// decide it and silently issue a non-Secure session cookie on an https
// deployment.
type EdgeConfig struct {
	// SecureCookies marks the session, CSRF and OAuth-state cookies Secure.
	// Derive it with [SecureCookies] from the configured OAuth redirect URL.
	SecureCookies bool
	// ClientIPHeader names the ONE request header the deployment's proxy sets
	// to the real client address, overwriting anything the client sent (e.g.
	// "CF-Connecting-IP" when the instance is reachable only through
	// Cloudflare). Empty trusts no such header; see [EdgeConfig.clientIP].
	ClientIPHeader string
}

// clientIP extracts the best-effort client IP for the session audit row. The
// value is only ever stored (sessions.ip) — nothing authorizes on it — but it
// should not be something the client simply typed (#662).
//
// TRUST ASSUMPTION: the web tier is reached only through the deployment's
// reverse proxy (ADR-0039), and which header that proxy vouches for is
// OPERATOR CONFIGURATION (GLYPHOXA_TRUSTED_CLIENT_IP_HEADER), never guessed:
//
//   - The configured [EdgeConfig.ClientIPHeader], when set and present. Only
//     name a header your proxy overwrites on every request — behind anything
//     else a client can supply it and choose its own audit address.
//   - Otherwise the LAST X-Forwarded-For hop: proxies APPEND the peer they
//     saw, so the rightmost entry is the one the nearest proxy wrote. The
//     first entry — which this used to return — is whatever the client put
//     in the header.
//   - Otherwise the socket peer.
func (e EdgeConfig) clientIP(r *http.Request) string {
	if e.ClientIPHeader != "" {
		if ip := strings.TrimSpace(r.Header.Get(e.ClientIPHeader)); ip != "" {
			return ip
		}
	}
	if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
		hops := strings.Split(xff[len(xff)-1], ",")
		if last := strings.TrimSpace(hops[len(hops)-1]); last != "" {
			return last
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// sessionCookie builds the HttpOnly session cookie (ADR-0016: HttpOnly, Secure,
// SameSite=Lax). Lax (not Strict) so the top-level redirect back from Discord
// still presents the cookie.
func sessionCookie(token string, secure bool, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// csrfCookie builds the double-submit CSRF cookie. It is NOT HttpOnly — the SPA
// reads it and mirrors it into the X-CSRF-Token header that the CSRF interceptor
// checks against this same cookie.
func csrfCookie(token string, secure bool, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     CSRFCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: false,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// joinCookieName carries an invitation token across the OAuth round trip
// (ADR-0065). It is a cookie of its own — never the OAuth `state`, which is
// sent to Discord and comes back in a URL (ADR-0056's reasoning).
const joinCookieName = "glyphoxa_join"

// joinCookiePath scopes the join cookie to the two OAuth routes: the browser
// sends an invitation token nowhere else on the origin.
const joinCookiePath = "/auth/discord"

// joinCookie stashes an invitation token for the callback.
func joinCookie(token string, secure bool, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name:     joinCookieName,
		Value:    token,
		Path:     joinCookiePath,
		Expires:  expires,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// clearJoinCookie expires the join cookie (it has its own Path, so
// [clearCookie] would not match it).
func clearJoinCookie(secure bool) *http.Cookie {
	c := clearCookie(joinCookieName, true, secure)
	c.Path = joinCookiePath
	return c
}

// validJoinToken reports whether s has the shape of an invitation token
// ([NewToken]: unpadded URL-safe base64). The token is echoed into a redirect
// path and a cookie value, so anything else is refused before it gets there.
func validJoinToken(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// clearCookie builds an immediately-expiring cookie that deletes name on the
// client (logout, and clearing the one-shot OAuth state).
func clearCookie(name string, httpOnly, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: httpOnly,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}
