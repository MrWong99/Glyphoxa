package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
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
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("web/all mode refuses to boot without a usable login (ADR-0041): "+
		"missing or empty %s — set them, or set GLYPHOXA_DEV_MODE=1 for an insecure "+
		"loopback-only dev instance", strings.Join(missing, ", "))
}

// devMode reports whether the GLYPHOXA_DEV_MODE opt-out is enabled — a non-blank
// value. When on, the Web Instance boots without OAuth, auto-authenticates every
// request as the seeded operator, and binds to loopback only (see [enableDevMode]).
func devMode(getenv func(string) string) bool {
	return strings.TrimSpace(getenv("GLYPHOXA_DEV_MODE")) != ""
}

// forceLoopback rewrites a listen address to bind 127.0.0.1, preserving the port
// (":8080" → "127.0.0.1:8080", "0.0.0.0:9000" → "127.0.0.1:9000"). GLYPHOXA_DEV_MODE
// pins the host to loopback so a mis-set flag in production is structurally
// ineffective: a container port-mapping cannot reach a loopback bind (ADR-0041).
// An address with no parseable host:port falls back to a bare loopback bind.
func forceLoopback(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "127.0.0.1"
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// devOperatorDiscordID is the synthetic Discord identity GLYPHOXA_DEV_MODE upserts
// as the seeded operator. It is deliberately NOT a real snowflake so it can never
// collide with a genuine Discord user, and stable so repeat boots reuse the one
// dev operator + its claimed tenant.
const devOperatorDiscordID = "glyphoxa-dev-operator"

// devSessionTTL bounds the auto-authenticated dev session; a day is plenty for a
// dev instance and re-minted on every boot.
const devSessionTTL = 24 * time.Hour

// seedDevSession synthesizes the seeded operator and issues a real session for it
// (ADR-0041 GLYPHOXA_DEV_MODE). It upserts a fixed synthetic operator, binds/
// creates its tenant, and mints a session + CSRF token — the same row shape the
// OAuth callback produces — so the existing interceptor stack + RequireSession +
// CSRF gate accept the injected cookies unchanged (see [devAuthMiddleware]). The
// store is the same auth.OAuthStore the OAuth callback uses; now is injected for
// tests.
func seedDevSession(ctx context.Context, store auth.OAuthStore, now func() time.Time) (sessionToken, csrfToken string, err error) {
	user, err := store.UpsertUser(ctx, storage.UpsertUserParams{
		DiscordUserID: devOperatorDiscordID,
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

// devAuthMiddleware makes every request arrive already authenticated as the
// seeded dev operator (ADR-0041 GLYPHOXA_DEV_MODE). It stamps the glyphoxa_session
// cookie (satisfying both the Connect auth interceptor and the plain-read
// RequireSession guard) and BOTH the glyphoxa_csrf cookie AND a matching
// X-CSRF-Token header (satisfying the double-submit CSRF interceptor) onto every
// inbound request, replacing any cookies the client sent. This reuses the whole
// existing gate unchanged — nothing is special-cased downstream. INSECURE: it is
// only ever wired behind the loopback bind [forceLoopback] forces.
func devAuthMiddleware(sessionToken, csrfToken string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("Cookie")
		r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionToken})
		r.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: csrfToken})
		r.Header.Set("X-CSRF-Token", csrfToken)
		next.ServeHTTP(w, r)
	})
}

// enableDevMode applies the GLYPHOXA_DEV_MODE opt-out end to end (ADR-0041): it
// forces the listen address to loopback, seeds an auto-auth session for the
// synthetic operator, logs a loud insecure-mode warning, and returns the forced
// address plus a wrapper that injects that session on every request. The caller
// wraps its mounts + SPA root with wrap and listens on loopbackAddr. This REPLACES
// the manual DB-session-insert dev flow. INSECURE — never enable in production.
func enableDevMode(ctx context.Context, store auth.OAuthStore, addr string, log *slog.Logger, now func() time.Time) (loopbackAddr string, wrap func(http.Handler) http.Handler, err error) {
	loopbackAddr = forceLoopback(addr)
	sessionToken, csrfToken, err := seedDevSession(ctx, store, now)
	if err != nil {
		return "", nil, err
	}
	log.Warn("GLYPHOXA_DEV_MODE ENABLED — INSECURE: every request is auto-authenticated "+
		"as the seeded operator and the web API is bound to loopback only; this bypasses "+
		"Discord OAuth and the operator allowlist and MUST NOT be used in production",
		"addr", loopbackAddr)
	wrap = func(h http.Handler) http.Handler {
		return devAuthMiddleware(sessionToken, csrfToken, h)
	}
	return loopbackAddr, wrap, nil
}
