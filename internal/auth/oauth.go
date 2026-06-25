package auth

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// OAuthStore is the persistence the OAuth callback needs: refresh the operator
// record, bind the single tenant, and create the session row. *storage.Store
// satisfies it.
type OAuthStore interface {
	UpsertUser(ctx context.Context, p storage.UpsertUserParams) (storage.User, error)
	ResolveOperatorTenant(ctx context.Context, userID uuid.UUID) (storage.Tenant, error)
	CreateSession(ctx context.Context, n storage.NewSession) (storage.Session, error)
}

// defaultSessionTTL is how long a freshly issued session is valid.
const defaultSessionTTL = 30 * 24 * time.Hour

// stateCookieTTL bounds the OAuth round trip; the state nonce only has to
// survive the redirect to Discord and back.
const stateCookieTTL = 10 * time.Minute

// OAuth serves the Discord OAuth carve-out (ADR-0015 — HTML redirects, NOT
// Connect): GET /auth/discord/login starts the flow, GET /auth/discord/callback
// finishes it by issuing the session + CSRF cookies (ADR-0016).
type OAuth struct {
	store    OAuthStore
	discord  DiscordOAuth
	ttl      time.Duration
	redirect string // where the callback sends the browser after login
	now      func() time.Time
	log      *slog.Logger
}

// NewOAuth builds the OAuth handlers. appRedirect is the post-login destination
// (the SPA root, "/").
func NewOAuth(store OAuthStore, discord DiscordOAuth, appRedirect string, log *slog.Logger) *OAuth {
	if appRedirect == "" {
		appRedirect = "/"
	}
	if log == nil {
		log = slog.Default()
	}
	return &OAuth{
		store:    store,
		discord:  discord,
		ttl:      defaultSessionTTL,
		redirect: appRedirect,
		now:      time.Now,
		log:      log,
	}
}

// Login starts the OAuth flow: mint an anti-forgery state nonce, stash it in a
// short-lived cookie, and 302 to Discord's authorize URL.
func (o *OAuth) Login(w http.ResponseWriter, r *http.Request) {
	state, err := newToken()
	if err != nil {
		o.log.Error("oauth login: mint state", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	secure := requestSecure(r)
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    state,
		Path:     "/",
		Expires:  o.now().Add(stateCookieTTL),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, o.discord.AuthCodeURL(state), http.StatusFound)
}

// Callback finishes the OAuth flow: verify the state nonce, exchange the code
// for the Discord identity, upsert the operator, bind the tenant, create the
// session, set the session + CSRF cookies, clear the state cookie, and 302 to
// the SPA. The Discord exchange is behind the DiscordOAuth interface so CI uses
// a fake — no live Discord call.
func (o *OAuth) Callback(w http.ResponseWriter, r *http.Request) {
	state := r.FormValue("state")
	stateCookie, err := r.Cookie(stateCookieName)
	if err != nil || state == "" ||
		subtle.ConstantTimeCompare([]byte(stateCookie.Value), []byte(state)) != 1 {
		http.Error(w, "invalid OAuth state", http.StatusBadRequest)
		return
	}
	code := r.FormValue("code")
	if code == "" {
		http.Error(w, "missing OAuth code", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	du, err := o.discord.Exchange(ctx, code)
	if err != nil {
		// The raw error can carry endpoint/status detail; log it, return generic.
		o.log.Error("oauth callback: discord exchange", "err", err)
		http.Error(w, "discord sign-in failed", http.StatusBadGateway)
		return
	}

	user, err := o.store.UpsertUser(ctx, storage.UpsertUserParams{
		DiscordUserID: du.ID,
		Name:          du.DisplayName(),
		Avatar:        du.AvatarURL,
	})
	if err != nil {
		o.log.Error("oauth callback: upsert user", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := o.store.ResolveOperatorTenant(ctx, user.ID); err != nil {
		o.log.Error("oauth callback: bind tenant", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	sessionToken, err := newToken()
	if err != nil {
		o.log.Error("oauth callback: mint session token", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	csrfToken, err := newToken()
	if err != nil {
		o.log.Error("oauth callback: mint csrf token", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	expires := o.now().Add(o.ttl)
	if _, err := o.store.CreateSession(ctx, storage.NewSession{
		UserID:    user.ID,
		Token:     sessionToken,
		ExpiresAt: expires,
		IP:        clientIP(r),
		UA:        r.UserAgent(),
	}); err != nil {
		o.log.Error("oauth callback: create session", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	secure := requestSecure(r)
	http.SetCookie(w, sessionCookie(sessionToken, secure, expires))
	http.SetCookie(w, csrfCookie(csrfToken, secure, expires))
	http.SetCookie(w, clearCookie(stateCookieName, true, secure))
	http.Redirect(w, r, o.redirect, http.StatusFound)
}

// clientIP extracts the best-effort client IP for the session audit row,
// preferring the proxy's X-Forwarded-For first hop.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		first, _, _ := strings.Cut(xff, ",")
		return strings.TrimSpace(first)
	}
	return r.RemoteAddr
}
