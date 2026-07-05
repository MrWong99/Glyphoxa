package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// webEnvVars are the environment variables a web/all-Mode Web Instance must have
// to boot with a usable login (ADR-0041): the three Discord OAuth credentials AND
// a non-empty operator allowlist. The allowlist is mandatory — a login that
// authenticates but authorizes nobody is not a login.
var webEnvVars = []string{
	"DISCORD_OAUTH_CLIENT_ID",
	"DISCORD_OAUTH_CLIENT_SECRET",
	"DISCORD_OAUTH_REDIRECT_URL",
	"GLYPHOXA_OPERATOR_IDS",
}

// requireWebEnv is the boot preflight for web/all Mode (ADR-0041, issue #112):
// unless GLYPHOXA_DEV_MODE is set (checked by the caller), every var in
// [webEnvVars] must be present and non-blank, else the Web Instance refuses to
// boot. The returned error NAMES every missing variable so a mis-configured
// deploy is fixable in one pass instead of failing one var at a time. This is an
// operability gate: without OAuth nobody can obtain a session, so a login-less
// Web Instance is a deploy that looks healthy but cannot be logged into — it must
// fail loud. getenv is injected so the helper is table-testable.
func requireWebEnv(getenv func(string) string) error {
	var missing []string
	for _, k := range webEnvVars {
		if strings.TrimSpace(getenv(k)) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("web/all mode refuses to boot without a usable login (ADR-0041): "+
			"missing or empty %s — set them, or set GLYPHOXA_DEV_MODE=1 for an insecure "+
			"loopback-only dev instance", strings.Join(missing, ", "))
	}

	// Present is not enough for the allowlist: parse it exactly like the runtime
	// gate (#103) does, so a separators-only value or a pasted username fails
	// HERE instead of booting the deploy nobody can log into that this preflight
	// exists to prevent.
	allow := auth.ParseOperatorAllowlist(getenv("GLYPHOXA_OPERATOR_IDS"))
	if allow.Len() == 0 {
		return fmt.Errorf("web/all mode refuses to boot without a usable login (ADR-0041): " +
			"GLYPHOXA_OPERATOR_IDS contains no operator IDs (separators only) — set at " +
			"least one Discord User snowflake, or set GLYPHOXA_DEV_MODE=1 for an " +
			"insecure loopback-only dev instance")
	}
	if bad := allow.Malformed(); len(bad) > 0 {
		return fmt.Errorf("web/all mode refuses to boot without a usable login (ADR-0041): "+
			"GLYPHOXA_OPERATOR_IDS entries are not Discord User snowflakes (digits only): "+
			"%s — such an entry can never match a login, which would silently lock the "+
			"operator out", strings.Join(bad, ", "))
	}
	return nil
}

// devMode reports whether the GLYPHOXA_DEV_MODE opt-out is enabled: a non-blank
// value that is not an explicit falsy spelling. "0", "false", "no" and "off"
// (any case) count as OFF — an operator writing GLYPHOXA_DEV_MODE=false to
// disable the auth bypass must get it disabled, not enabled; ADR-0041 intends an
// explicit dev opt-IN. When on, the Web Instance boots without OAuth,
// auto-authenticates every request as the dev operator, and binds to loopback
// only (see [enableDevMode]).
func devMode(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("GLYPHOXA_DEV_MODE"))) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

// sttStreaming reports whether the GLYPHOXA_STT_STREAMING opt-in enables the
// streaming-STT transport (ADR-0042, issue #180). Same truthy parse as [devMode]:
// blank or an explicit falsy spelling ("0"/"false"/"no"/"off", any case) is OFF;
// anything else is ON. Default OFF keeps the batch STT path byte-for-byte, so the
// streaming path ships dark until an operator opts in.
func sttStreaming(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("GLYPHOXA_STT_STREAMING"))) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

// forceLoopback rewrites a listen address to bind 127.0.0.1, preserving the port
// (":8080" → "127.0.0.1:8080", "0.0.0.0:9000" → "127.0.0.1:9000"). GLYPHOXA_DEV_MODE
// pins the host to loopback so a mis-set flag in production is blunted: a
// container port-mapping cannot reach a loopback bind (ADR-0041). Same-host
// processes still can — which is why [devAuthMiddleware] additionally refuses
// requests carrying proxy evidence. An address with no parseable host:port falls
// back to a bare loopback bind.
func forceLoopback(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "127.0.0.1"
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// devSessionTTL bounds the auto-authenticated dev session; [devSessions] re-mints
// an expired (or logged-out) one on the next request, so the TTL is a freshness
// bound, not a lifetime limit for the dev instance.
const devSessionTTL = 24 * time.Hour

// proxyHeaders is the request-header evidence that a reverse proxy forwarded the
// request — the same headers the auth tier itself reads to detect a proxy
// (X-Forwarded-Proto for cookie security, X-Forwarded-For for session audit).
// Dev mode refuses requests carrying any of them: the loopback bind stops
// container port-mappings, but a same-host reverse proxy (or a port-forward)
// still dials 127.0.0.1, and auto-authenticating traffic that provably crossed
// a proxy would hand every proxied visitor the operator console.
var proxyHeaders = []string{"X-Forwarded-For", "X-Forwarded-Proto", "Forwarded"}

// seedDevSession synthesizes the dev operator and issues a real session for it
// (ADR-0041 GLYPHOXA_DEV_MODE). It upserts the fixed synthetic operator
// ([storage.DevOperatorDiscordID]), binds/creates its tenant, and mints a
// session + CSRF token — the same row shape the OAuth callback produces — so the
// existing interceptor stack + RequireSession + CSRF gate accept the injected
// cookies unchanged (see [devAuthMiddleware]). The store is the same
// auth.OAuthStore the OAuth callback uses; now is injected for tests.
func seedDevSession(ctx context.Context, store auth.OAuthStore, now func() time.Time) (sessionToken, csrfToken string, err error) {
	user, err := store.UpsertUser(ctx, storage.UpsertUserParams{
		DiscordUserID: storage.DevOperatorDiscordID,
		Name:          "Dev Operator",
	})
	if err != nil {
		return "", "", fmt.Errorf("seed dev operator: %w", err)
	}
	if _, err := store.ResolveOperatorTenant(ctx, user.ID); err != nil {
		return "", "", fmt.Errorf("bind dev operator tenant: %w", err)
	}
	sessionToken, err = auth.NewToken()
	if err != nil {
		return "", "", fmt.Errorf("mint dev session token: %w", err)
	}
	csrfToken, err = auth.NewToken()
	if err != nil {
		return "", "", fmt.Errorf("mint dev csrf token: %w", err)
	}
	if _, err := store.CreateSession(ctx, storage.NewSession{
		UserID:    user.ID,
		Token:     sessionToken,
		ExpiresAt: now().Add(devSessionTTL),
		IP:        "127.0.0.1",
		UA:        "glyphoxa-dev-mode",
	}); err != nil {
		return "", "", fmt.Errorf("create dev session: %w", err)
	}
	return sessionToken, csrfToken, nil
}

// devSessions holds the auto-auth dev session and re-mints it when it dies
// (ADR-0041 GLYPHOXA_DEV_MODE). The session is a real DB row, so the SPA's
// Logout button deletes it and the TTL expires it — without re-seeding, either
// would 401 every subsequent request until a process restart. tokens revalidates
// the cached pair per request (one indexed read) and seeds a fresh session when
// it is gone.
type devSessions struct {
	store auth.OAuthStore
	authn auth.Authenticator
	now   func() time.Time

	mu      sync.Mutex
	session string
	csrf    string
}

// tokens returns a currently-valid session/CSRF pair, minting one if the cached
// pair is absent, expired, or logged out.
func (d *devSessions) tokens(ctx context.Context) (session, csrf string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.session != "" {
		if _, err := d.authn.AuthenticateSession(ctx, d.session); err == nil {
			return d.session, d.csrf, nil
		}
	}
	session, csrf, err = seedDevSession(ctx, d.store, d.now)
	if err != nil {
		return "", "", err
	}
	d.session, d.csrf = session, csrf
	return session, csrf, nil
}

// devAuthMiddleware makes every request arrive already authenticated as the
// dev operator (ADR-0041 GLYPHOXA_DEV_MODE). It stamps the glyphoxa_session
// cookie (satisfying both the Connect auth interceptor and the plain-read
// RequireSession guard) and BOTH the glyphoxa_csrf cookie AND a matching
// X-CSRF-Token header (satisfying the double-submit CSRF interceptor) onto every
// inbound request, replacing any cookies the client sent. This reuses the whole
// existing gate unchanged — nothing is special-cased downstream. Requests
// carrying proxy evidence ([proxyHeaders]) are refused with 403: they crossed a
// reverse proxy, which the loopback bind alone cannot rule out on the same host.
// INSECURE for anything but local dev; it is only ever wired behind the loopback
// bind [forceLoopback] forces.
func devAuthMiddleware(d *devSessions, log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, h := range proxyHeaders {
			if r.Header.Get(h) != "" {
				log.Error("GLYPHOXA_DEV_MODE refused a proxied request — dev mode must never sit behind a reverse proxy (ADR-0041)",
					"header", h, "remote", r.RemoteAddr)
				http.Error(w, "GLYPHOXA_DEV_MODE refuses proxied requests (ADR-0041): "+
					"dev mode auto-authenticates every caller and must never be exposed "+
					"through a reverse proxy or port-forward", http.StatusForbidden)
				return
			}
		}
		session, csrf, err := d.tokens(r.Context())
		if err != nil {
			log.Error("GLYPHOXA_DEV_MODE could not (re-)seed the dev session", "error", err)
			http.Error(w, "dev session unavailable", http.StatusInternalServerError)
			return
		}
		r.Header.Del("Cookie")
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: session})
		r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrf})
		r.Header.Set("X-CSRF-Token", csrf)
		next.ServeHTTP(w, r)
	})
}

// enableDevMode applies the GLYPHOXA_DEV_MODE opt-out end to end (ADR-0041): it
// forces the listen address to loopback, seeds an auto-auth session for the
// synthetic operator (failing the boot, not the first request, on a broken DB),
// logs a loud insecure-mode warning, and returns the forced address plus a
// wrapper that injects a valid session on every request — re-minting it after a
// logout or TTL expiry. The caller wraps its mounts + SPA root with wrap and
// listens on loopbackAddr. This REPLACES the manual DB-session-insert dev flow.
// INSECURE — never enable in production.
func enableDevMode(ctx context.Context, store auth.OAuthStore, authn auth.Authenticator, addr string, log *slog.Logger, now func() time.Time) (loopbackAddr string, wrap func(http.Handler) http.Handler, err error) {
	loopbackAddr = forceLoopback(addr)
	d := &devSessions{store: store, authn: authn, now: now}
	if _, _, err := d.tokens(ctx); err != nil {
		return "", nil, err
	}
	log.Warn("GLYPHOXA_DEV_MODE ENABLED — INSECURE: every request is auto-authenticated "+
		"as the dev operator and the web API is bound to loopback only; this bypasses "+
		"Discord OAuth and the operator allowlist and MUST NOT be used in production. "+
		"The dev operator claims the seeded Tenant — point dev mode at a throwaway "+
		"database (a later real login takes the Tenant over)",
		"addr", loopbackAddr)
	wrap = func(h http.Handler) http.Handler {
		return devAuthMiddleware(d, log, h)
	}
	return loopbackAddr, wrap, nil
}
