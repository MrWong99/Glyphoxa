package auth

import (
	"context"
	"crypto/subtle"
	"errors"
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
	adm      Admission // the admission policy (ADR-0041 allowlist / ADR-0055 open)
	ttl      time.Duration
	redirect string // where the callback sends the browser after login
	now      func() time.Time
	log      *slog.Logger
}

// notAuthorizedRedirect is where the callback sends a Discord User it refuses —
// off the allowlist in allowlist mode (ADR-0041), or suspended in open mode
// (ADR-0055): the login screen with a non-leaky not_authorized signal.
// Bad-state/missing-code still fail with http.Error.
const notAuthorizedRedirect = "/login?error=not_authorized"

// aupRequiredRedirect is where an open-mode OAuth start (or an open-mode signup
// reaching the callback) without the AUP acknowledgment is bounced (#518): back
// to the login screen, which renders the acknowledgment checkbox and a hint.
// Unreachable through the SPA (the OAuth anchor only renders once the box is
// ticked) — this is the server-side backstop for a hand-typed
// /auth/discord/login or a hand-crafted OAuth round trip.
const aupRequiredRedirect = "/login?error=aup_required"

// aupStateSuffix marks the state COOKIE of an OAuth round trip whose start
// carried the AUP acknowledgment (#518). It rides the cookie, not the state
// param sent to Discord, so the callback reads it from the value this server
// set. newToken is Raw-URL base64, so "." can never appear in a nonce and the
// suffix is unambiguous.
const aupStateSuffix = ".aup"

// onboardingRedirect is where a FRESH signup lands after the callback: the
// name-your-Tenant onboarding step (ADR-0055). The path is a Go↔SPA contract —
// web/src/app/router.tsx mounts the matching route.
const onboardingRedirect = "/onboarding/create-tenant"

// NewOAuth builds the OAuth handlers. appRedirect is the post-login destination
// (the SPA root, "/"). adm is the admission policy: in allowlist mode a Discord
// User whose snowflake is absent from the allowlist is denied a session at the
// callback (ADR-0041); in open mode such a User is admitted via the create-only
// signup transaction instead (ADR-0055). Allowlisted Users keep the
// claim-or-create login path in BOTH modes.
func NewOAuth(store OAuthStore, discord DiscordOAuth, appRedirect string, adm Admission, log *slog.Logger) *OAuth {
	if appRedirect == "" {
		appRedirect = "/"
	}
	if log == nil {
		log = slog.Default()
	}
	return &OAuth{
		store:    store,
		discord:  discord,
		adm:      adm,
		ttl:      defaultSessionTTL,
		redirect: appRedirect,
		now:      time.Now,
		log:      log,
	}
}

// Login starts the OAuth flow: mint an anti-forgery state nonce, stash it in a
// short-lived cookie, and 302 to Discord's authorize URL.
//
// In open Admission Mode the start additionally requires the AUP
// acknowledgment (#518, `aup=1` — the login screen appends it once the visitor
// ticks the box): a hand-typed /auth/discord/login must not slip past the
// gate the SPA renders. The acknowledgment rides the state COOKIE to the
// callback, where the signup path records its time on the user row. Allowlist
// mode is untouched — the #518 decision gates open-mode signup only.
func (o *OAuth) Login(w http.ResponseWriter, r *http.Request) {
	aup := r.URL.Query().Get("aup") == "1"
	if o.adm.Mode == AdmissionOpen && !aup {
		http.Redirect(w, r, aupRequiredRedirect, http.StatusFound)
		return
	}
	state, err := newToken()
	if err != nil {
		o.log.Error("oauth login: mint state", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	cookieValue := state
	if aup {
		cookieValue += aupStateSuffix
	}
	secure := requestSecure(r)
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    cookieValue,
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
	if err != nil || state == "" {
		http.Error(w, "invalid OAuth state", http.StatusBadRequest)
		return
	}
	// The AUP acknowledgment (#518) rides the state cookie as a suffix this
	// server appended at Login; strip it before the nonce comparison.
	nonce, aupAccepted := strings.CutSuffix(stateCookie.Value, aupStateSuffix)
	if subtle.ConstantTimeCompare([]byte(nonce), []byte(state)) != 1 {
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

	// Admission (ADR-0041 / ADR-0055). An allowlisted User proceeds on the
	// claim-or-create login path in BOTH modes. A stranger forks by mode: in
	// open mode they are admitted through the create-only signup transaction;
	// in allowlist mode (or an open mode misconfigured without a provisioner —
	// fail closed) they are rejected BEFORE any UpsertUser /
	// ResolveOperatorTenant / CreateSession — no session, no Tenant write, no
	// auto-created Tenant — and bounced to the login screen with a non-leaky
	// not_authorized signal.
	if !o.adm.Allowlist.Contains(du.ID) {
		if o.adm.open() {
			o.signup(w, r, du, aupAccepted)
			return
		}
		if o.adm.Mode == AdmissionOpen {
			o.log.Error("oauth callback: open admission without a signup provisioner — failing closed to allowlist posture")
		}
		o.log.Warn("oauth callback: rejected non-allowlisted Discord user", "discord_user_id", du.ID)
		http.Redirect(w, r, notAuthorizedRedirect, http.StatusFound)
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
	// A suspended user (ADR-0055) is bounced on the allowlisted path too:
	// AuthenticateSession would refuse every request on the minted session
	// anyway, so minting it would only produce a silent login→401 loop. Same
	// non-leaky signal as an allowlist rejection. Unset for every self-host
	// that never touched `glyphoxa user suspend` — behavior-preserving.
	if user.SuspendedAt != nil {
		o.log.Warn("oauth callback: rejected suspended Discord user", "discord_user_id", du.ID)
		http.Redirect(w, r, notAuthorizedRedirect, http.StatusFound)
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

// signup admits a non-allowlisted Discord User in open mode (ADR-0055) through
// the ONE all-or-nothing provisioning transaction: user upsert → (first visit)
// create-only Tenant founding + default-Plan bind → session mint. A fresh
// founder lands on the name-your-Tenant onboarding step; a returning signup
// goes straight to the app. A suspended user gets the same non-leaky
// not_authorized bounce as an allowlist rejection.
//
// aupAccepted is the #518 gate, enforced HERE — at the account-writing moment —
// not just at Login: a round trip whose state cookie lacks the acknowledgment
// suffix (a hand-crafted authorize URL that skipped our Login handler) writes
// nothing and bounces. The acceptance time is recorded on the user row inside
// the provisioning transaction (§305 BGB inclusion evidence), refreshed on
// every returning open-mode login (the login screen re-asks each time).
func (o *OAuth) signup(w http.ResponseWriter, r *http.Request, du DiscordUser, aupAccepted bool) {
	if !aupAccepted {
		o.log.Warn("oauth signup: refused open-mode signup without AUP acknowledgment", "discord_user_id", du.ID)
		http.Redirect(w, r, aupRequiredRedirect, http.StatusFound)
		return
	}
	sessionToken, err := newToken()
	if err != nil {
		o.log.Error("oauth signup: mint session token", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	csrfToken, err := newToken()
	if err != nil {
		o.log.Error("oauth signup: mint csrf token", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	expires := o.now().Add(o.ttl)
	res, err := o.adm.Signup.ProvisionSignup(r.Context(), storage.SignupParams{
		User: storage.UpsertUserParams{
			DiscordUserID: du.ID,
			Name:          du.DisplayName(),
			Avatar:        du.AvatarURL,
		},
		TenantName: du.DisplayName() + "'s Table",
		PlanSlug:   o.adm.SignupPlanSlug,
		Session: storage.NewSession{
			Token:     sessionToken,
			ExpiresAt: expires,
			IP:        clientIP(r),
			UA:        r.UserAgent(),
		},
		AUPAcceptedAt: o.now(),
	})
	if errors.Is(err, storage.ErrUserSuspended) {
		o.log.Warn("oauth signup: rejected suspended Discord user", "discord_user_id", du.ID)
		http.Redirect(w, r, notAuthorizedRedirect, http.StatusFound)
		return
	}
	if err != nil {
		o.log.Error("oauth signup: provision", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	secure := requestSecure(r)
	http.SetCookie(w, sessionCookie(sessionToken, secure, expires))
	http.SetCookie(w, csrfCookie(csrfToken, secure, expires))
	http.SetCookie(w, clearCookie(stateCookieName, true, secure))
	dest := o.redirect
	if res.Created {
		dest = onboardingRedirect
	}
	http.Redirect(w, r, dest, http.StatusFound)
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
