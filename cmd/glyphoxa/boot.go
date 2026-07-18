package main

import (
	"context"
	"errors"
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
// a non-empty operator allowlist. The allowlist is mandatory in `allowlist`
// Admission Mode — a login that authenticates but authorizes nobody is not a
// login. In `open` Admission Mode (ADR-0055) OAuth IS the signup mechanism and
// stays required, but the allowlist-nonempty requirement is relaxed: an empty
// list is a deployment with no platform admins, warned loudly at boot rather
// than refused.
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
//
// open relaxes ONLY the allowlist branches (ADR-0055): presence and
// non-emptiness of GLYPHOXA_OPERATOR_IDS stop being required (the list is then
// the platform-admin list, not the admission gate) while a malformed non-empty
// value still refuses — a broken platform-admin list silently locking the
// operator out is the same trap in both modes. Callers pass the ENV-resolved
// posture: requireWebEnv runs before any DB, so a persisted-only `open`
// posture (env var lost) still demands the allowlist — conservative by design.
func requireWebEnv(getenv func(string) string, open bool) error {
	var missing []string
	for _, k := range webEnvVars {
		if open && k == "GLYPHOXA_OPERATOR_IDS" {
			continue
		}
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
	if !open && allow.Len() == 0 {
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

// admissionModeEnv reads the operator-facing Admission Mode switch,
// GLYPHOXA_ADMISSION_MODE (ADR-0055). Unset is NOT an error — the resolved
// posture then falls back to the DB record ([resolveAdmissionMode]) — but an
// unparsable value is a loud boot refusal, never a silent default: a typo'd
// posture must not run allowlist-locked (mass-revoking signups) or open by
// accident.
func admissionModeEnv(getenv func(string) string) (mode auth.AdmissionMode, set bool, err error) {
	raw := strings.TrimSpace(getenv("GLYPHOXA_ADMISSION_MODE"))
	if raw == "" {
		return "", false, nil
	}
	m, err := auth.ParseAdmissionMode(raw)
	if err != nil {
		return "", true, fmt.Errorf("web/all mode refuses to boot on an ambiguous admission posture (ADR-0055): GLYPHOXA_ADMISSION_MODE: %w", err)
	}
	return m, true, nil
}

// admissionSettings is the persisted-posture surface [resolveAdmissionMode]
// needs. *storage.Store satisfies it.
type admissionSettings interface {
	GetAdmissionPosture(ctx context.Context) (string, error)
	RecordAdmissionPosture(ctx context.Context, mode string) error
}

// resolveAdmissionMode resolves the deployment's EFFECTIVE Admission Mode
// (ADR-0055): the env var is the operator-facing switch and wins when set; when
// unset the DB-persisted posture carries — so a config change that silently
// drops the env var cannot flip an open deployment back to allowlist posture
// and mass-revoke every signup's session at the boot sweep. With neither, the
// default is allowlist (exactly ADR-0041). The effective posture is recorded
// back to the DB, versioned and visible; transitions log loudly — especially
// open → allowlist, which is the deliberate lock-down (the sweep then evicts
// every signup, as ADR-0041's amendment intends). An unparsable PERSISTED
// posture with no env override refuses to boot: it means a newer binary
// recorded a vocabulary this one does not know, and guessing between "sweep
// everyone" and "admit everyone" is not a rollback strategy — the operator
// breaks the tie by setting GLYPHOXA_ADMISSION_MODE explicitly.
func resolveAdmissionMode(ctx context.Context, settings admissionSettings, getenv func(string) string, log *slog.Logger) (auth.AdmissionMode, error) {
	envMode, envSet, err := admissionModeEnv(getenv)
	if err != nil {
		return "", err
	}
	persisted, err := settings.GetAdmissionPosture(ctx)
	havePersisted := err == nil
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return "", fmt.Errorf("web: read admission posture: %w", err)
	}

	effective := auth.AdmissionAllowlist
	switch {
	case envSet:
		effective = envMode
	case havePersisted:
		m, err := auth.ParseAdmissionMode(persisted)
		if err != nil {
			return "", fmt.Errorf("web/all mode refuses to boot on an ambiguous admission posture (ADR-0055): "+
				"the recorded posture %q is unknown to this binary (a newer binary wrote it?) and "+
				"GLYPHOXA_ADMISSION_MODE is unset — set it explicitly to break the tie", persisted)
		}
		effective = m
	}

	if !havePersisted || persisted != string(effective) {
		if err := settings.RecordAdmissionPosture(ctx, string(effective)); err != nil {
			return "", fmt.Errorf("web: record admission posture: %w", err)
		}
	}
	switch {
	case havePersisted && persisted == string(auth.AdmissionOpen) && effective == auth.AdmissionAllowlist:
		log.Warn("admission posture flipped open -> allowlist: LOCK-DOWN — the boot sweep will now revoke every non-allowlisted session (ADR-0055)")
	case havePersisted && persisted == string(auth.AdmissionAllowlist) && effective == auth.AdmissionOpen:
		log.Info("admission posture flipped allowlist -> open: self-signup is live (ADR-0055)")
	}
	return effective, nil
}

// signupPlanGetter is the plan read [signupPlanPreflight] needs.
// *storage.Store satisfies it.
type signupPlanGetter interface {
	GetPlanBySlug(ctx context.Context, slug string) (storage.Plan, error)
}

// signupPlanPreflight is the `open`-Admission-Mode boot gate on the signup
// default plan (ADR-0055): GLYPHOXA_SIGNUP_PLAN_SLUG must name a synced,
// non-archived Plan, else the Web Instance refuses to boot — otherwise every
// signup would fail at runtime, AFTER the user completed OAuth, forever. In the
// FATAL boot-error class deliberately (the ADR says "refuse to boot").
func signupPlanPreflight(ctx context.Context, plans signupPlanGetter, slug string) error {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return errors.New("open admission mode refuses to boot without a signup plan (ADR-0055): " +
			"GLYPHOXA_SIGNUP_PLAN_SLUG is empty — name the free BYOK plan every signup binds to")
	}
	p, err := plans.GetPlanBySlug(ctx, slug)
	if errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("open admission mode refuses to boot (ADR-0055): GLYPHOXA_SIGNUP_PLAN_SLUG "+
			"%q matches no synced plan — run `glyphoxa billing plans-sync` (or enable the chart's "+
			"plans hook) with a catalog containing it", slug)
	}
	if err != nil {
		return fmt.Errorf("web: signup-plan preflight: %w", err)
	}
	if p.Archived {
		return fmt.Errorf("open admission mode refuses to boot (ADR-0055): signup plan %q is archived — "+
			"revive it in the catalog or point GLYPHOXA_SIGNUP_PLAN_SLUG at a live plan", slug)
	}
	return nil
}

// devMode reports whether the GLYPHOXA_DEV_MODE opt-out is enabled: a non-blank
// value that is not an explicit falsy spelling. "0", "false", "no" and "off"
// (any case) count as OFF — an operator writing GLYPHOXA_DEV_MODE=false to
// disable the auth bypass must get it disabled, not enabled; ADR-0041 intends an
// explicit dev opt-IN. When on, the Web Instance boots without OAuth,
// auto-authenticates every request as the dev operator, and binds to loopback
// only (see [enableDevMode]). Since the Butler GM gate armed in standalone
// voice mode (ADR-0055), the flag ALSO means "every voice speaker is GM" on a
// `-mode voice` node — the same admit-all posture the web tier's dev gate has
// always had, now consistently applied.
func devMode(getenv func(string) string) bool {
	switch strings.ToLower(strings.TrimSpace(getenv("GLYPHOXA_DEV_MODE"))) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

// gmSpeakerGate builds the SpeakerID→GM predicate arming the Butler GM-only
// voice-address gate (#280, ADR-0024). In dev mode every speaker is the
// synthetic operator, so the gate admits all — mirroring the dev auto-auth that
// treats every request as the seeded operator ([enableDevMode]). Otherwise the
// GM identity is the checker's verdict — auth.GMIdentity's tenant-operator
// binding union the env allowlist (ADR-0055, amending ADR-0050's
// allowlist-membership clause) — fail-closed on an empty SpeakerID and on any
// snowflake it does not recognize. A checker with no identity source admits
// nobody (Butler unaddressable by voice); the callers warn on that.
func gmSpeakerGate(dev bool, isGM func(string) bool) func(string) bool {
	if dev {
		return func(string) bool { return true }
	}
	return func(speakerID string) bool {
		return speakerID != "" && isGM(speakerID)
	}
}

// armVoiceGMGate builds the standalone voice node's Butler GM-address gate
// (#280, ADR-0024; extracted from runVoice so the arming is testable, like
// [resolveStandaloneCampaign]). The node used to leave the gate nil — absent,
// every speaker able to address the Butler as GM (the fail-open the
// self-signup design note flagged; ADR-0055). It arms from the same GM
// identity the web tier uses: the tenant-operator binding union
// GLYPHOXA_OPERATOR_IDS (a voice node often has no allowlist env — the DB
// binding from the operator's web login carries it). Dev mode admits every
// speaker, mirroring the web tier; otherwise the returned gate is NEVER nil
// and fails closed on unknown or empty SpeakerIDs — including when no GM
// identity source exists at all (warned: the Butler is then unaddressable).
// A failed binding load degrades to the allowlist alone, never to fail-open.
func armVoiceGMGate(ctx context.Context, bindings auth.TenantOperatorLister, getenv func(string) string, log *slog.Logger) func(string) bool {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	dev := devMode(getenv)
	gmID := auth.NewGMIdentity(bindings, auth.ParseOperatorAllowlist(getenv("GLYPHOXA_OPERATOR_IDS")), log)
	if err := gmID.Refresh(ctx); err != nil {
		log.Warn("voice: loading tenant-operator GM bindings failed; Butler GM addressing falls back to GLYPHOXA_OPERATOR_IDS alone until the next refresh", "err", err)
	}
	if !dev && gmID.Empty() {
		log.Warn("butler voice-address gate armed with no GM identity source: the Butler is unaddressable by voice — log into the web UI once (binds the tenant operator) or set GLYPHOXA_OPERATOR_IDS on this node")
	}
	return gmSpeakerGate(dev, gmID.IsGM)
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
// existing policy gate (the Connect stack and the guarded mount table, #446)
// accepts the injected
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
// cookie (satisfying the session check on both the Connect stack and the
// guarded plain mounts, #446) and BOTH the glyphoxa_csrf cookie AND a matching
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
