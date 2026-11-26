package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
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
	redirect string     // where the callback sends the browser after login
	edge     EdgeConfig // proxy facts: cookie Secure, trusted client-IP header
	now      func() time.Time
	log      *slog.Logger
}

// notAuthorizedRedirect is where the callback sends a Discord User it refuses —
// neither a Platform Admin nor admitted from the web in allowlist mode
// (ADR-0041 / ADR-0065), or suspended in any mode (ADR-0055): the login screen
// with a non-leaky not_authorized signal. Bad-state/missing-code still fail
// with http.Error.
const notAuthorizedRedirect = "/login?error=not_authorized"

// Join-page error codes ([joinRedirect]). Like onboardingRedirect below, the
// `/join/<token>` path and these codes are a Go↔SPA contract.
const (
	// joinErrorInvalid: the link did not admit this Discord User. Deliberately
	// one code for unknown, revoked, expired, used up and pinned-to-someone-else
	// (storage.ErrInvitationInvalid's reasoning).
	joinErrorInvalid = "invalid"
	// joinErrorAUPRequired: the join was started or completed without the
	// Nutzungsbedingungen/Datenschutz acknowledgment (#518, extended to every
	// Admission Mode by ADR-0065).
	joinErrorAUPRequired = "aup_required"
)

// joinRedirect sends the browser back to the join page of the invitation it
// came from, with an error code the page renders. token has passed
// [validJoinToken]; it is path-escaped regardless.
func joinRedirect(token, code string) string {
	return "/join/" + url.PathEscape(token) + "?error=" + code
}

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
// (the SPA root, "/"). adm is the admission policy ([Admission.decide]): a
// Platform Admin keeps the claim-or-create login path in BOTH modes (ADR-0041);
// in open mode anyone else is admitted via the create-only signup transaction
// (ADR-0055); in allowlist mode anyone else needs an Access Grant or a live
// access invitation (ADR-0065) and is otherwise denied a session. edge is the deployment's proxy
// configuration ([EdgeConfig]) — required, so cookie security is always an
// explicit decision of the caller.
func NewOAuth(store OAuthStore, discord DiscordOAuth, appRedirect string, adm Admission, edge EdgeConfig, log *slog.Logger) *OAuth {
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
		edge:     edge,
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
// callback, where the signup path records its time on the user row. An
// ordinary allowlist-mode login is untouched.
//
// `join=<token>` starts a JOIN (ADR-0065): the visitor came from an invitation
// link's `/join/<token>` page. The token is stashed in its own short-lived
// HttpOnly cookie — never in the OAuth state, which travels to Discord and
// back in a URL — and a join requires the acknowledgment in EVERY Admission
// Mode: whoever redeems an invitation becomes a new account holder however
// the deployment admits.
func (o *OAuth) Login(w http.ResponseWriter, r *http.Request) {
	aup := r.URL.Query().Get("aup") == "1"
	join := r.URL.Query().Get("join")
	if join != "" {
		if !validJoinToken(join) {
			// Not a token this server could have minted; there is no join page
			// to send it back to.
			http.Redirect(w, r, notAuthorizedRedirect, http.StatusFound)
			return
		}
		if !aup {
			http.Redirect(w, r, joinRedirect(join, joinErrorAUPRequired), http.StatusFound)
			return
		}
	}
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
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    cookieValue,
		Path:     "/",
		Expires:  o.now().Add(stateCookieTTL),
		HttpOnly: true,
		Secure:   o.edge.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
	switch {
	case join != "":
		http.SetCookie(w, joinCookie(join, o.edge.SecureCookies, o.now().Add(stateCookieTTL)))
	case hasCookie(r, joinCookieName):
		// An ordinary login must not redeem the invitation of a join someone
		// abandoned in this browser minutes ago.
		http.SetCookie(w, clearJoinCookie(o.edge.SecureCookies))
	}
	http.Redirect(w, r, o.discord.AuthCodeURL(state), http.StatusFound)
}

// hasCookie reports whether the request carries the named cookie.
func hasCookie(r *http.Request, name string) bool {
	_, err := r.Cookie(name)
	return err == nil
}

// takeJoinToken reads the invitation token a join start stashed and ALWAYS
// clears the cookie: it is one-shot, whatever the callback goes on to decide.
// A value that is not token-shaped (the cookie is client-held) reads as no
// join at all.
func (o *OAuth) takeJoinToken(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie(joinCookieName)
	if err != nil {
		return ""
	}
	http.SetCookie(w, clearJoinCookie(o.edge.SecureCookies))
	if !validJoinToken(c.Value) {
		return ""
	}
	return c.Value
}

// Callback finishes the OAuth flow: verify the state nonce, exchange the code
// for the Discord identity, decide admission ([Admission.decide]), and — for
// whoever is admitted — write the user, bind or found the Tenant, create the
// session, set the session + CSRF cookies, clear the state cookie, and 302 to
// the SPA. The Discord exchange is behind the DiscordOAuth interface so CI uses
// a fake — no live Discord call.
func (o *OAuth) Callback(w http.ResponseWriter, r *http.Request) {
	joinToken := o.takeJoinToken(w, r)

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

	// Admission (ADR-0041 / ADR-0055 / ADR-0065). Whoever is not admitted is
	// rejected BEFORE any UpsertUser / ResolveOperatorTenant / CreateSession —
	// no session, no Tenant write, no auto-created Tenant.
	decision, err := o.adm.decide(ctx, du.ID, joinToken, aupAccepted)
	if err != nil {
		o.log.Error("oauth callback: admission", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	switch decision.route {
	case admitPlatformAdmin:
		o.loginPlatformAdmin(w, r, du)
	case admitOpenSignup:
		o.signup(w, r, du, aupAccepted)
	case admitGranted:
		o.admitGranted(w, r, du, decision.platformKeys, aupAccepted)
	case admitCampaignJoin:
		o.joinCampaign(w, r, du, joinToken, aupAccepted)
	case admitGuest:
		o.loginGuest(w, r, du, decision.guestCampaign)
	default:
		o.reject(w, r, du, joinToken, decision.why)
	}
}

// reject bounces a Discord User the callback did not admit. Someone who came
// from an invitation link goes back to that link's join page, which can say
// "this link does not work" — more useful than the login screen's generic
// refusal, and no more revealing: they already hold the token. Everyone else
// gets the non-leaky not_authorized signal.
func (o *OAuth) reject(w http.ResponseWriter, r *http.Request, du DiscordUser, joinToken string, why rejection) {
	switch {
	case why == rejectSuspended:
		o.log.Warn("oauth callback: rejected suspended Discord user", "discord_user_id", du.ID)
		http.Redirect(w, r, notAuthorizedRedirect, http.StatusFound)
	case why == rejectAUPRequired && joinToken != "":
		o.log.Warn("oauth callback: refused a join without AUP acknowledgment", "discord_user_id", du.ID)
		http.Redirect(w, r, joinRedirect(joinToken, joinErrorAUPRequired), http.StatusFound)
	case joinToken != "":
		o.log.Warn("oauth callback: rejected Discord user whose invitation did not admit them", "discord_user_id", du.ID)
		http.Redirect(w, r, joinRedirect(joinToken, joinErrorInvalid), http.StatusFound)
	default:
		if o.adm.Mode == AdmissionOpen {
			o.log.Error("oauth callback: open admission without a signup provisioner — failing closed to allowlist posture")
		}
		o.log.Warn("oauth callback: rejected Discord user who is neither a Platform Admin nor admitted", "discord_user_id", du.ID)
		http.Redirect(w, r, notAuthorizedRedirect, http.StatusFound)
	}
}

// joinCampaign redeems a CAMPAIGN invitation for the Discord User and signs
// them in as that Campaign's guest (ADR-0065 lane 2). The redemption is ONE
// transaction that writes the user row and the guest row and nothing else:
// this path never founds a Tenant and never runs signup provisioning, in any
// Admission Mode and whoever the redeemer is. Someone who already operates a
// Tenant simply gains the guest row; their session starts in the Campaign they
// were invited to and they switch back from the workspace switcher.
//
// decide has already required the acknowledgment; its time is recorded with
// the redemption. A suspended redeemer is refused inside the transaction,
// before anything is written or a use is counted.
func (o *OAuth) joinCampaign(w http.ResponseWriter, r *http.Request, du DiscordUser, joinToken string, aupAccepted bool) {
	p := storage.CampaignRedemption{User: storage.UpsertUserParams{
		DiscordUserID: du.ID,
		Name:          du.DisplayName(),
		Avatar:        du.AvatarURL,
	}}
	if aupAccepted {
		p.AUPAcceptedAt = o.now()
	}
	red, err := o.adm.Guests.RedeemCampaignInvitation(r.Context(), joinToken, p)
	switch {
	case errors.Is(err, storage.ErrUserSuspended):
		o.reject(w, r, du, joinToken, rejectSuspended)
		return
	case errors.Is(err, storage.ErrInvitationInvalid):
		o.reject(w, r, du, joinToken, rejectNotAuthorized)
		return
	case err != nil:
		o.log.Error("oauth campaign join: redeem", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	o.issueSession(w, r, "oauth campaign join", red.User.ID, &red.CampaignID)
}

// loginGuest signs in a Discord User who already is a Campaign Guest and
// presented nothing else that admits them (ADR-0065 lane 2): refresh the user
// row from Discord — for a guest added by snowflake this is what fills in
// their name — and mint a session acting in guestCampaign. No Tenant is
// founded or claimed. Nothing is recorded about the acknowledgment here: the
// join that made them a guest recorded it, and a guest a GM added by snowflake
// signs in through the ordinary login screen, which on an allowlist
// deployment does not ask (the Access Grant holder's posture). A suspended
// guest is bounced like everyone else.
func (o *OAuth) loginGuest(w http.ResponseWriter, r *http.Request, du DiscordUser, guestCampaign uuid.UUID) {
	ctx := r.Context()
	user, err := o.store.UpsertUser(ctx, storage.UpsertUserParams{
		DiscordUserID: du.ID,
		Name:          du.DisplayName(),
		Avatar:        du.AvatarURL,
	})
	if err != nil {
		o.log.Error("oauth guest login: upsert user", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if user.SuspendedAt != nil {
		o.reject(w, r, du, "", rejectSuspended)
		return
	}
	o.issueSession(w, r, "oauth guest login", user.ID, &guestCampaign)
}

// issueSession mints the session + CSRF pair for an admitted user, stores the
// session — acting as a guest of guestCampaign when set — sets the cookies and
// redirects into the app. what prefixes the log lines.
func (o *OAuth) issueSession(w http.ResponseWriter, r *http.Request, what string, userID uuid.UUID, guestCampaign *uuid.UUID) {
	sessionToken, err := newToken()
	if err != nil {
		o.log.Error(what+": mint session token", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	csrfToken, err := newToken()
	if err != nil {
		o.log.Error(what+": mint csrf token", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	expires := o.now().Add(o.ttl)
	if _, err := o.store.CreateSession(r.Context(), storage.NewSession{
		UserID:          userID,
		Token:           sessionToken,
		ExpiresAt:       expires,
		IP:              o.edge.clientIP(r),
		UA:              r.UserAgent(),
		GuestCampaignID: guestCampaign,
	}); err != nil {
		o.log.Error(what+": create session", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	secure := o.edge.SecureCookies
	http.SetCookie(w, sessionCookie(sessionToken, secure, expires))
	http.SetCookie(w, csrfCookie(csrfToken, secure, expires))
	http.SetCookie(w, clearCookie(stateCookieName, true, secure))
	http.Redirect(w, r, o.redirect, http.StatusFound)
}

// loginPlatformAdmin is the ADR-0041 claim-or-create login of a Discord User
// on the Platform Admin list: refresh the user row, bind (or claim, or seed)
// their Tenant, mint the session. Identical in both Admission Modes.
func (o *OAuth) loginPlatformAdmin(w http.ResponseWriter, r *http.Request, du DiscordUser) {
	ctx := r.Context()
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
		IP:        o.edge.clientIP(r),
		UA:        r.UserAgent(),
	}); err != nil {
		o.log.Error("oauth callback: create session", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	secure := o.edge.SecureCookies
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
	o.provision(w, r, du, "oauth signup", storage.SignupParams{
		PlanSlug:      o.adm.SignupPlanSlug,
		AUPAcceptedAt: o.now(),
	})
}

// admitGranted admits an Access Grant holder on an allowlist deployment
// (ADR-0065) through the same provisioning transaction as an open-mode signup,
// minus the Plan (an allowlist deployment runs with zero Plan rows): their
// first sign-in founds a Tenant of their own — create-only, never the seed
// Tenant, which stays the Platform Admin's — whose Platform Keys switch takes
// the grant's value, and lands them on the name-your-Tenant onboarding step.
// A returning holder goes straight to the app.
//
// The acknowledgment is recorded when this round trip carried one (every join
// does; a holder granted by snowflake signs in through the ordinary allowlist
// login, which does not ask). A suspended holder is bounced like everyone else.
func (o *OAuth) admitGranted(w http.ResponseWriter, r *http.Request, du DiscordUser, platformKeys, aupAccepted bool) {
	p := storage.SignupParams{PlatformKeys: &platformKeys}
	if aupAccepted {
		p.AUPAcceptedAt = o.now()
	}
	o.provision(w, r, du, "oauth access grant", p)
}

// provision runs the signup transaction for an admitted Discord User and
// finishes the login. The caller sets the fields that differ by admission
// route (Plan, Platform Keys, acknowledgment time); the identity, the default
// Tenant name and the session are filled in here. what prefixes the log lines.
func (o *OAuth) provision(w http.ResponseWriter, r *http.Request, du DiscordUser, what string, p storage.SignupParams) {
	sessionToken, err := newToken()
	if err != nil {
		o.log.Error(what+": mint session token", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	csrfToken, err := newToken()
	if err != nil {
		o.log.Error(what+": mint csrf token", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	expires := o.now().Add(o.ttl)
	p.User = storage.UpsertUserParams{
		DiscordUserID: du.ID,
		Name:          du.DisplayName(),
		Avatar:        du.AvatarURL,
	}
	p.TenantName = du.DisplayName() + "'s Table"
	p.Session = storage.NewSession{
		Token:     sessionToken,
		ExpiresAt: expires,
		IP:        o.edge.clientIP(r),
		UA:        r.UserAgent(),
	}
	res, err := o.adm.Signup.ProvisionSignup(r.Context(), p)
	if errors.Is(err, storage.ErrUserSuspended) {
		o.log.Warn(what+": rejected suspended Discord user", "discord_user_id", du.ID)
		http.Redirect(w, r, notAuthorizedRedirect, http.StatusFound)
		return
	}
	if err != nil {
		o.log.Error(what+": provision", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	secure := o.edge.SecureCookies
	http.SetCookie(w, sessionCookie(sessionToken, secure, expires))
	http.SetCookie(w, csrfCookie(csrfToken, secure, expires))
	http.SetCookie(w, clearCookie(stateCookieName, true, secure))
	dest := o.redirect
	if res.Created {
		dest = onboardingRedirect
	}
	http.Redirect(w, r, dest, http.StatusFound)
}
