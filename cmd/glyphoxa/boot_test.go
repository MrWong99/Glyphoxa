package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
)

// fakeOAuthStore is an in-memory auth.OAuthStore recording what seedDevSession
// wrote, so the dev-mode auto-auth boot is testable without Postgres.
type fakeOAuthStore struct {
	upsertedDiscordID string
	tenantForUser     uuid.UUID
	sessions          []storage.NewSession
	userID            uuid.UUID
}

func (f *fakeOAuthStore) UpsertUser(_ context.Context, p storage.UpsertUserParams) (storage.User, error) {
	f.upsertedDiscordID = p.DiscordUserID
	if f.userID == uuid.Nil {
		f.userID = uuid.New()
	}
	return storage.User{ID: f.userID, DiscordUserID: p.DiscordUserID, Name: p.Name, Role: "operator"}, nil
}

func (f *fakeOAuthStore) ResolveOperatorTenant(_ context.Context, userID uuid.UUID) (storage.Tenant, error) {
	f.tenantForUser = userID
	return storage.Tenant{ID: uuid.New()}, nil
}

func (f *fakeOAuthStore) CreateSession(_ context.Context, n storage.NewSession) (storage.Session, error) {
	f.sessions = append(f.sessions, n)
	return storage.Session{ID: uuid.New(), UserID: n.UserID, Token: n.Token, ExpiresAt: n.ExpiresAt}, nil
}

// anyTokenAuth accepts any non-empty session token as the seeded dev operator, so
// devAuthMiddleware can be exercised through the REAL auth.RequireSession guard
// without a DB.
type anyTokenAuth struct{ id uuid.UUID }

func (a anyTokenAuth) AuthenticateSession(_ context.Context, token string) (storage.User, error) {
	if token == "" {
		return storage.User{}, storage.ErrNotFound
	}
	return storage.User{ID: a.id, Name: "Dev Operator"}, nil
}

// envMap adapts a map into a getenv func for the boot-preflight helpers, so the
// tests never mutate the real process environment (which would race t.Parallel
// and leak between cases).
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// TestRequireWebEnv is the ADR-0041 boot preflight: web/all Mode must refuse to
// boot unless all three DISCORD_OAUTH_* vars AND a non-empty GLYPHOXA_OPERATOR_IDS
// are present, and the fatal error must NAME every missing variable so the
// operator can fix the deploy in one pass.
func TestRequireWebEnv(t *testing.T) {
	all := map[string]string{
		"DISCORD_OAUTH_CLIENT_ID":     "cid",
		"DISCORD_OAUTH_CLIENT_SECRET": "secret",
		"DISCORD_OAUTH_REDIRECT_URL":  "https://x/cb",
		"GLYPHOXA_OPERATOR_IDS":       "123",
	}

	// Fully configured → boots cleanly (AC3).
	if err := requireWebEnv(envMap(all), false); err != nil {
		t.Fatalf("requireWebEnv with a full config returned %v, want nil", err)
	}

	// Each required var, when missing, must be named in the fatal error.
	for _, missing := range []string{
		"DISCORD_OAUTH_CLIENT_ID",
		"DISCORD_OAUTH_CLIENT_SECRET",
		"DISCORD_OAUTH_REDIRECT_URL",
		"GLYPHOXA_OPERATOR_IDS",
	} {
		env := map[string]string{}
		for k, v := range all {
			env[k] = v
		}
		delete(env, missing)
		err := requireWebEnv(envMap(env), false)
		if err == nil {
			t.Fatalf("requireWebEnv missing %s returned nil, want a fatal error", missing)
		}
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("requireWebEnv missing %s: error %q does not name the variable", missing, err)
		}
	}

	// A blank/whitespace value counts as missing (an empty allowlist is not a gate).
	blank := map[string]string{
		"DISCORD_OAUTH_CLIENT_ID":     "cid",
		"DISCORD_OAUTH_CLIENT_SECRET": "secret",
		"DISCORD_OAUTH_REDIRECT_URL":  "https://x/cb",
		"GLYPHOXA_OPERATOR_IDS":       "   ",
	}
	if err := requireWebEnv(envMap(blank), false); err == nil || !strings.Contains(err.Error(), "GLYPHOXA_OPERATOR_IDS") {
		t.Errorf("requireWebEnv with a whitespace allowlist returned %v, want an error naming GLYPHOXA_OPERATOR_IDS", err)
	}

	// Present but useless allowlists fail the parse-based check (the same parser
	// as the #103 runtime gate): separators only parses to zero entries, and a
	// non-snowflake entry can never match a login — either way the deploy would
	// look healthy while nobody can log in, the exact state #112 prevents.
	sepOnly := map[string]string{}
	for k, v := range all {
		sepOnly[k] = v
	}
	sepOnly["GLYPHOXA_OPERATOR_IDS"] = " , ,, "
	if err := requireWebEnv(envMap(sepOnly), false); err == nil || !strings.Contains(err.Error(), "GLYPHOXA_OPERATOR_IDS") {
		t.Errorf("requireWebEnv with a separators-only allowlist returned %v, want an error naming GLYPHOXA_OPERATOR_IDS", err)
	}
	malformed := map[string]string{}
	for k, v := range all {
		malformed[k] = v
	}
	malformed["GLYPHOXA_OPERATOR_IDS"] = "MrWong99, 770000000000000000"
	if err := requireWebEnv(envMap(malformed), false); err == nil || !strings.Contains(err.Error(), "MrWong99") {
		t.Errorf("requireWebEnv with a non-snowflake entry returned %v, want an error naming the bad entry", err)
	}

	// Nothing set → every variable is named.
	err := requireWebEnv(envMap(map[string]string{}), false)
	if err == nil {
		t.Fatal("requireWebEnv with an empty env returned nil, want a fatal error")
	}
	for _, want := range []string{
		"DISCORD_OAUTH_CLIENT_ID",
		"DISCORD_OAUTH_CLIENT_SECRET",
		"DISCORD_OAUTH_REDIRECT_URL",
		"GLYPHOXA_OPERATOR_IDS",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("empty-env error %q does not name %s", err, want)
		}
	}
}

// TestRequireWebEnv_OpenMode pins the ADR-0055 relaxation: `open` Admission
// Mode drops ONLY the allowlist presence/non-emptiness branches — DISCORD_OAUTH_*
// stays mandatory (OAuth is the signup mechanism) and a malformed non-empty
// allowlist still refuses (a broken platform-admin list is the same
// silent-lock-out trap in both modes).
func TestRequireWebEnv_OpenMode(t *testing.T) {
	oauthOnly := map[string]string{
		"DISCORD_OAUTH_CLIENT_ID":     "cid",
		"DISCORD_OAUTH_CLIENT_SECRET": "secret",
		"DISCORD_OAUTH_REDIRECT_URL":  "https://x/cb",
	}

	// No allowlist at all: fine in open mode (warned at boot, not refused).
	if err := requireWebEnv(envMap(oauthOnly), true); err != nil {
		t.Fatalf("open mode with no allowlist returned %v, want nil", err)
	}
	// Separators-only: also fine in open mode (parses to zero entries).
	sep := map[string]string{}
	for k, v := range oauthOnly {
		sep[k] = v
	}
	sep["GLYPHOXA_OPERATOR_IDS"] = " , ,, "
	if err := requireWebEnv(envMap(sep), true); err != nil {
		t.Fatalf("open mode with a separators-only allowlist returned %v, want nil", err)
	}

	// OAuth stays mandatory in open mode.
	for _, missing := range []string{"DISCORD_OAUTH_CLIENT_ID", "DISCORD_OAUTH_CLIENT_SECRET", "DISCORD_OAUTH_REDIRECT_URL"} {
		env := map[string]string{}
		for k, v := range oauthOnly {
			env[k] = v
		}
		delete(env, missing)
		if err := requireWebEnv(envMap(env), true); err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("open mode missing %s returned %v, want an error naming it", missing, err)
		}
	}

	// A malformed NON-EMPTY platform-admin list still refuses in open mode.
	bad := map[string]string{}
	for k, v := range oauthOnly {
		bad[k] = v
	}
	bad["GLYPHOXA_OPERATOR_IDS"] = "MrWong99"
	if err := requireWebEnv(envMap(bad), true); err == nil || !strings.Contains(err.Error(), "MrWong99") {
		t.Errorf("open mode with a malformed allowlist returned %v, want an error naming the bad entry", err)
	}
}

// TestAdmissionModeEnv pins the env half of the ADR-0055 posture switch: unset
// is NOT an error (the DB record then carries), but an unparsable value is a
// loud refusal — never a silent default.
func TestAdmissionModeEnv(t *testing.T) {
	cases := []struct {
		raw      string
		wantMode auth.AdmissionMode
		wantSet  bool
		wantErr  bool
	}{
		{"", "", false, false},
		{"  ", "", false, false},
		{"allowlist", auth.AdmissionAllowlist, true, false},
		{"open", auth.AdmissionOpen, true, false},
		{" OPEN ", auth.AdmissionOpen, true, false},
		{"opne", "", true, true},
		{"1", "", true, true},
	}
	for _, c := range cases {
		mode, set, err := admissionModeEnv(envMap(map[string]string{"GLYPHOXA_ADMISSION_MODE": c.raw}))
		if (err != nil) != c.wantErr || set != c.wantSet || mode != c.wantMode {
			t.Errorf("admissionModeEnv(%q) = (%q, %v, %v), want (%q, %v, err=%v)",
				c.raw, mode, set, err, c.wantMode, c.wantSet, c.wantErr)
		}
	}
}

// fakeSettings is an in-memory admissionSettings recording posture writes.
type fakeSettings struct {
	posture string
	getErr  error
	recErr  error
	records []string
}

func (f *fakeSettings) GetAdmissionPosture(context.Context) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	if f.posture == "" {
		return "", storage.ErrNotFound
	}
	return f.posture, nil
}

func (f *fakeSettings) RecordAdmissionPosture(_ context.Context, mode string) error {
	if f.recErr != nil {
		return f.recErr
	}
	f.records = append(f.records, mode)
	f.posture = mode
	return nil
}

// TestResolveAdmissionMode pins the effective-posture RESOLUTION (ADR-0055) —
// a pure read: env wins when set; an unset env falls back to the DB record
// (the rollback-trap mitigation); neither defaults to allowlist; an unknown
// persisted posture with no env override refuses to boot. Recording is a
// separate step ([admissionResolution.record], tested below) so a flip that
// fails its preflights is never persisted.
func TestResolveAdmissionMode(t *testing.T) {
	ctx := context.Background()

	t.Run("fresh deploy defaults to allowlist without recording", func(t *testing.T) {
		st := &fakeSettings{}
		res, err := resolveAdmissionMode(ctx, st, envMap(nil))
		if err != nil || res.Effective != auth.AdmissionAllowlist {
			t.Fatalf("= (%q, %v), want allowlist", res.Effective, err)
		}
		if len(st.records) != 0 {
			t.Fatalf("records = %v, want none from resolution (record is a separate step)", st.records)
		}
	})

	t.Run("env open wins over a persisted allowlist", func(t *testing.T) {
		st := &fakeSettings{posture: "allowlist"}
		res, err := resolveAdmissionMode(ctx, st, envMap(map[string]string{"GLYPHOXA_ADMISSION_MODE": "open"}))
		if err != nil || res.Effective != auth.AdmissionOpen {
			t.Fatalf("= (%q, %v), want open", res.Effective, err)
		}
	})

	t.Run("unset env falls back to the persisted open posture", func(t *testing.T) {
		// The rollback-trap mitigation: a config change that LOSES the env var
		// must not flip an open deployment back to allowlist (and mass-revoke
		// every signup at the sweep).
		st := &fakeSettings{posture: "open"}
		res, err := resolveAdmissionMode(ctx, st, envMap(nil))
		if err != nil || res.Effective != auth.AdmissionOpen {
			t.Fatalf("= (%q, %v), want the persisted open posture", res.Effective, err)
		}
	})

	t.Run("explicit env allowlist over persisted open is the lock-down", func(t *testing.T) {
		st := &fakeSettings{posture: "open"}
		res, err := resolveAdmissionMode(ctx, st, envMap(map[string]string{"GLYPHOXA_ADMISSION_MODE": "allowlist"}))
		if err != nil || res.Effective != auth.AdmissionAllowlist {
			t.Fatalf("= (%q, %v), want allowlist (lock-down)", res.Effective, err)
		}
	})

	t.Run("unknown persisted posture with no env refuses to boot", func(t *testing.T) {
		st := &fakeSettings{posture: "invite-only"}
		if _, err := resolveAdmissionMode(ctx, st, envMap(nil)); err == nil ||
			!strings.Contains(err.Error(), "invite-only") {
			t.Fatalf("= %v, want a refusal naming the unknown posture", err)
		}
	})

	t.Run("invalid env value refuses to boot", func(t *testing.T) {
		st := &fakeSettings{}
		if _, err := resolveAdmissionMode(ctx, st, envMap(map[string]string{"GLYPHOXA_ADMISSION_MODE": "opne"})); err == nil {
			t.Fatal("want a refusal on an unparsable env posture")
		}
	})

	t.Run("posture read failure is fatal", func(t *testing.T) {
		if _, err := resolveAdmissionMode(ctx, &fakeSettings{getErr: errors.New("db down")}, envMap(nil)); err == nil {
			t.Fatal("want a fatal error on a posture read failure")
		}
	})
}

// TestAdmissionResolutionRecord pins the record step: the effective posture is
// upserted only when it differs from (or has no) DB record, and a record
// failure is fatal. Recording runs after the open-mode preflights in runWeb —
// a flip that refuses to boot is never persisted (pinned by ordering in
// TestRunWebAdmissionPreflightWiring's open-mode path plus this unit).
func TestAdmissionResolutionRecord(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	ctx := context.Background()

	t.Run("first boot records the default", func(t *testing.T) {
		st := &fakeSettings{}
		res := admissionResolution{Effective: auth.AdmissionAllowlist}
		if err := res.record(ctx, st, log); err != nil {
			t.Fatalf("record: %v", err)
		}
		if len(st.records) != 1 || st.records[0] != "allowlist" {
			t.Fatalf("records = %v, want the default posture recorded", st.records)
		}
	})

	t.Run("posture change is recorded", func(t *testing.T) {
		st := &fakeSettings{posture: "allowlist"}
		res := admissionResolution{Effective: auth.AdmissionOpen, persisted: "allowlist", havePersisted: true}
		if err := res.record(ctx, st, log); err != nil {
			t.Fatalf("record: %v", err)
		}
		if st.posture != "open" {
			t.Fatalf("persisted posture = %q, want open", st.posture)
		}
	})

	t.Run("unchanged posture writes nothing", func(t *testing.T) {
		st := &fakeSettings{posture: "open"}
		res := admissionResolution{Effective: auth.AdmissionOpen, persisted: "open", havePersisted: true}
		if err := res.record(ctx, st, log); err != nil {
			t.Fatalf("record: %v", err)
		}
		if len(st.records) != 0 {
			t.Fatalf("records = %v, want none (posture unchanged)", st.records)
		}
	})

	t.Run("record failure is fatal", func(t *testing.T) {
		st := &fakeSettings{recErr: errors.New("db down")}
		res := admissionResolution{Effective: auth.AdmissionAllowlist}
		if err := res.record(ctx, st, log); err == nil {
			t.Fatal("want a fatal error on a posture record failure")
		}
	})
}

// fakeSweeper records RevokeSessionsOutsideAllowlist calls.
type fakeSweeper struct {
	calls  int
	gotIDs []string
	err    error
}

func (f *fakeSweeper) RevokeSessionsOutsideAllowlist(_ context.Context, ids []string) (int64, error) {
	f.calls++
	f.gotIDs = ids
	return 3, f.err
}

// TestSweepAllowlistSessions pins the mode-split sweep decision (ADR-0041
// #184 / ADR-0055): allowlist-mode non-dev boots sweep with the parsed ids;
// open-mode and dev boots must NOT sweep (open would log out every signup on
// every restart); a sweep failure stays fatal.
func TestSweepAllowlistSessions(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	ctx := context.Background()
	allow := auth.ParseOperatorAllowlist("42, 77")

	t.Run("allowlist mode sweeps", func(t *testing.T) {
		sw := &fakeSweeper{}
		if err := sweepAllowlistSessions(ctx, sw, false, auth.AdmissionAllowlist, allow, log); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if sw.calls != 1 || len(sw.gotIDs) != 2 {
			t.Fatalf("calls=%d ids=%v, want one sweep with both ids", sw.calls, sw.gotIDs)
		}
	})

	t.Run("open mode must not sweep", func(t *testing.T) {
		sw := &fakeSweeper{}
		if err := sweepAllowlistSessions(ctx, sw, false, auth.AdmissionOpen, allow, log); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if sw.calls != 0 {
			t.Fatal("open-mode boot swept — it would log out every signup on every restart")
		}
	})

	t.Run("dev mode skips", func(t *testing.T) {
		sw := &fakeSweeper{}
		if err := sweepAllowlistSessions(ctx, sw, true, auth.AdmissionAllowlist, allow, log); err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if sw.calls != 0 {
			t.Fatal("dev boot swept")
		}
	})

	t.Run("sweep failure is fatal", func(t *testing.T) {
		sw := &fakeSweeper{err: errors.New("db down")}
		if err := sweepAllowlistSessions(ctx, sw, false, auth.AdmissionAllowlist, allow, log); err == nil {
			t.Fatal("want a fatal error on a sweep failure")
		}
	})
}

// TestPostureWiringHelpers pins the two posture-driven constructions the
// composition root uses (ADR-0054 seam (a) / ADR-0055 gate (b)): allowlist =
// EnvFallbackAllowed + nil allowance (self-host no-ops); open =
// SubscriptionKeyGate + PlanAllowance over the store.
func TestPostureWiringHelpers(t *testing.T) {
	t.Parallel()
	st := &storage.Store{}

	if _, ok := entitlementForMode(auth.AdmissionAllowlist, st).(llmbuild.EnvFallbackAllowed); !ok {
		t.Error("allowlist mode must grant the env fallback (EnvFallbackAllowed)")
	}
	gate, ok := entitlementForMode(auth.AdmissionOpen, st).(llmbuild.SubscriptionKeyGate)
	if !ok || gate.Subs != llmbuild.PlatformSubscriptionChecker(st) {
		t.Error("open mode must gate the env fallback on the store-backed subscription check")
	}

	if got := allowanceForMode(auth.AdmissionAllowlist, st); got != nil {
		t.Errorf("allowlist mode allowance = %v, want nil (no gate)", got)
	}
	pa, ok := allowanceForMode(auth.AdmissionOpen, st).(spend.PlanAllowance)
	if !ok || pa.Reader != spend.AllowanceReader(st) {
		t.Error("open mode must wire the store-backed PlanAllowance")
	}
}

// fakePlanGetter scripts GetPlanBySlug for the signup-plan preflight.
type fakePlanGetter struct {
	plans map[string]storage.Plan
	err   error
}

func (f *fakePlanGetter) GetPlanBySlug(_ context.Context, slug string) (storage.Plan, error) {
	if f.err != nil {
		return storage.Plan{}, f.err
	}
	p, ok := f.plans[slug]
	if !ok {
		return storage.Plan{}, storage.ErrNotFound
	}
	return p, nil
}

// TestSignupPlanPreflight pins the ADR-0055 open-mode boot gate: an empty,
// unknown, or archived signup plan slug refuses the boot with an actionable
// message — never a runtime signup failure after OAuth.
func TestSignupPlanPreflight(t *testing.T) {
	ctx := context.Background()
	plans := &fakePlanGetter{plans: map[string]storage.Plan{
		"byok-free": {Slug: "byok-free"},
		"legacy":    {Slug: "legacy", Archived: true},
	}}

	if err := signupPlanPreflight(ctx, plans, "byok-free"); err != nil {
		t.Fatalf("live plan: %v, want nil", err)
	}
	if err := signupPlanPreflight(ctx, plans, ""); err == nil || !strings.Contains(err.Error(), "GLYPHOXA_SIGNUP_PLAN_SLUG") {
		t.Errorf("empty slug: %v, want an error naming the env var", err)
	}
	if err := signupPlanPreflight(ctx, plans, "nope"); err == nil || !strings.Contains(err.Error(), "plans-sync") {
		t.Errorf("unknown slug: %v, want an error pointing at plans-sync", err)
	}
	if err := signupPlanPreflight(ctx, plans, "legacy"); err == nil || !strings.Contains(err.Error(), "archived") {
		t.Errorf("archived slug: %v, want an archived refusal", err)
	}
	if err := signupPlanPreflight(ctx, &fakePlanGetter{err: errors.New("db down")}, "byok-free"); err == nil {
		t.Error("store failure: want a fatal error")
	}
}

// TestDefaultMode pins ADR-0005 / ADR-0034: `glyphoxa` with no -mode flag boots
// `all` (the self-host default), while an explicit -mode still wins. The default
// flows through flag parsing exactly as main() registers it, so a regression of
// the constant back to `voice` fails here (issue #282, AC3 first half).
func TestDefaultMode(t *testing.T) {
	if defaultMode != "all" {
		t.Fatalf("defaultMode = %q, want %q (ADR-0005/0034 self-host default)", defaultMode, "all")
	}
}

// TestVoiceEntryRequiresTarget drives the REAL voice entry path (runVoice) to
// pin AC3's second half: the default flip to `all` must not relax voice mode's
// -guild/-channel requirement. With a token present but a missing guild/channel,
// runVoice returns the target error before any DB/network work; with both
// present it proceeds PAST that gate and fails later for a different reason (no
// DB configured) — proving the gate itself passed, not the whole boot.
func TestVoiceEntryRequiresTarget(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	metrics := observe.NewPrometheusRecorder()
	t.Setenv("DISCORD_BOT_TOKEN", "test-token")
	// Force the no-DB early return for the both-present case so runVoice never
	// dials Postgres or the network from this unit test.
	t.Setenv("GLYPHOXA_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "")

	const targetMsg = "-guild and -channel are required"
	cases := []struct {
		name           string
		guild, channel string
		wantTargetErr  bool
	}{
		{"missing both", "", "", true},
		{"missing channel", "g", "", true},
		{"missing guild", "", "c", true},
		{"both present", "g", "c", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// metricsAddr "" keeps runVoice off any listener; the target gate is
			// reached before that anyway.
			err := runVoice(log, wirenpc.Config{Guild: c.guild, Channel: c.channel}, false, metrics, "")
			targetErr := err != nil && strings.Contains(err.Error(), targetMsg)
			if c.wantTargetErr && !targetErr {
				t.Errorf("runVoice(guild=%q, channel=%q) err = %v, want the %q target error", c.guild, c.channel, err, targetMsg)
			}
			if !c.wantTargetErr && targetErr {
				t.Errorf("runVoice(guild=%q, channel=%q) returned the target error despite both flags set: %v", c.guild, c.channel, err)
			}
		})
	}
}

// TestRequireVoiceTarget pins the target-flag predicate directly: voice mode
// demands BOTH -guild and -channel. Each missing half is an error; both present
// passes.
func TestRequireVoiceTarget(t *testing.T) {
	cases := []struct {
		guild, channel string
		wantErr        bool
	}{
		{"g", "c", false},
		{"", "c", true},
		{"g", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		err := requireVoiceTarget(wirenpc.Config{Guild: c.guild, Channel: c.channel})
		if c.wantErr && err == nil {
			t.Errorf("requireVoiceTarget(guild=%q, channel=%q) = nil, want an error", c.guild, c.channel)
		}
		if !c.wantErr && err != nil {
			t.Errorf("requireVoiceTarget(guild=%q, channel=%q) = %v, want nil", c.guild, c.channel, err)
		}
	}
}

// TestDevMode: the opt-out is on only when GLYPHOXA_DEV_MODE holds a non-blank
// value that is not an explicit falsy spelling — an operator writing
// GLYPHOXA_DEV_MODE=false to disable the auth bypass must get it disabled.
func TestDevMode(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"   ", false},
		{"0", false},
		{"false", false},
		{"FALSE", false},
		{" no ", false},
		{"off", false},
		{"1", true},
		{"true", true},
		{"yes", true},
	}
	for _, c := range cases {
		if got := devMode(envMap(map[string]string{"GLYPHOXA_DEV_MODE": c.val})); got != c.want {
			t.Errorf("devMode(%q) = %v, want %v", c.val, got, c.want)
		}
	}
}

// TestGMSpeakerGate pins the Butler GM-address gate predicate wiring (#280,
// ADR-0024; GM identity per ADR-0055): dev mode auto-authorizes every speaker
// as GM (mirroring the dev auto-auth operator), and otherwise the gate is the
// GM-identity checker's verdict — fail-closed on an empty SpeakerID and on a
// speaker it does not recognize.
func TestGMSpeakerGate(t *testing.T) {
	isGM := func(id string) bool { return id == "111" || id == "222" }

	// Dev mode: every speaker is the operator, so the gate admits all — including
	// an empty SpeakerID and an id no checker would recognize.
	dev := gmSpeakerGate(true, isGM)
	for _, id := range []string{"111", "999", ""} {
		if !dev(id) {
			t.Errorf("dev gmSpeakerGate(%q) = false, want true (dev auto-authorizes every speaker)", id)
		}
	}

	// Non-dev: the checker's verdict, fail closed on unknown and on empty.
	prod := gmSpeakerGate(false, isGM)
	if !prod("111") {
		t.Error("gmSpeakerGate(false).(\"111\") = false, want true (a GM)")
	}
	if prod("999") {
		t.Error("gmSpeakerGate(false).(\"999\") = true, want false (not a GM)")
	}
	if prod("") {
		t.Error("gmSpeakerGate(false).(\"\") = true, want false (empty SpeakerID never a GM)")
	}

	// The empty-SpeakerID drop is the GATE's own guard, not the checker's: even
	// a checker that admits everything must not make "" a GM.
	permissive := gmSpeakerGate(false, func(string) bool { return true })
	if permissive("") {
		t.Error("gmSpeakerGate(false, admit-all).(\"\") = true, want false (the gate itself fails closed on empty)")
	}
	if !permissive("999") {
		t.Error("gmSpeakerGate(false, admit-all).(\"999\") = false, want the checker's verdict for non-empty ids")
	}
}

// TestSTTStreaming: the streaming-STT opt-in (ADR-0042, issue #180) is on only
// when GLYPHOXA_STT_STREAMING holds a non-blank, non-falsy value, so the batch
// path stays the byte-for-byte default until an operator explicitly opts in.
func TestSTTStreaming(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"   ", false},
		{"0", false},
		{"false", false},
		{"OFF", false},
		{" no ", false},
		{"1", true},
		{"true", true},
		{"yes", true},
	}
	for _, c := range cases {
		if got := sttStreaming(envMap(map[string]string{"GLYPHOXA_STT_STREAMING": c.val})); got != c.want {
			t.Errorf("sttStreaming(%q) = %v, want %v", c.val, got, c.want)
		}
	}
}

// TestGatewayIdentifyWarnThreshold: the IDENTIFY-budget warning threshold (#486)
// defaults to 500 (well below Discord's 1000/token/24h reset limit) and accepts a
// positive integer override; blank, non-numeric, zero and negative values fall
// back to the default so a fat-fingered env var never disables the alarm.
func TestGatewayIdentifyWarnThreshold(t *testing.T) {
	const def = 500
	cases := []struct {
		val  string
		want int
	}{
		{"", def},
		{"   ", def},
		{"abc", def},
		{"0", def},
		{"-5", def},
		{"1", 1},
		{"250", 250},
		{" 900 ", 900},
	}
	for _, c := range cases {
		if got := gatewayIdentifyWarnThreshold(envMap(map[string]string{"GLYPHOXA_GATEWAY_IDENTIFY_WARN_THRESHOLD": c.val})); got != c.want {
			t.Errorf("gatewayIdentifyWarnThreshold(%q) = %d, want %d", c.val, got, c.want)
		}
	}
}

// TestForceLoopback pins the listen host to 127.0.0.1 while preserving the port,
// so a GLYPHOXA_DEV_MODE instance is structurally unreachable from a container
// port-mapping (ADR-0041) regardless of the configured -web-addr.
func TestForceLoopback(t *testing.T) {
	cases := []struct{ in, want string }{
		{":8080", "127.0.0.1:8080"},
		{"0.0.0.0:8080", "127.0.0.1:8080"},
		{"0.0.0.0:0", "127.0.0.1:0"},
		{"[::]:9000", "127.0.0.1:9000"},
		{"example:1234", "127.0.0.1:1234"},
	}
	for _, c := range cases {
		if got := forceLoopback(c.in); got != c.want {
			t.Errorf("forceLoopback(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSeedDevSession: the dev-mode boot upserts the fixed synthetic operator,
// binds its tenant, and mints a real session — the same row the OAuth callback
// creates — so the injected cookies flow through the existing gate.
func TestSeedDevSession(t *testing.T) {
	store := &fakeOAuthStore{}
	fixed := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)

	sess, csrf, err := seedDevSession(context.Background(), store, func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("seedDevSession: %v", err)
	}
	if sess == "" || csrf == "" || sess == csrf {
		t.Fatalf("tokens must be non-empty and distinct: session=%q csrf=%q", sess, csrf)
	}
	if store.upsertedDiscordID != storage.DevOperatorDiscordID {
		t.Errorf("upserted discord id = %q, want %q", store.upsertedDiscordID, storage.DevOperatorDiscordID)
	}
	if store.tenantForUser != store.userID {
		t.Errorf("ResolveOperatorTenant called with %v, want the upserted user %v", store.tenantForUser, store.userID)
	}
	if len(store.sessions) != 1 {
		t.Fatalf("created %d sessions, want 1", len(store.sessions))
	}
	got := store.sessions[0]
	if got.Token != sess {
		t.Errorf("session row token = %q, want the returned session token %q", got.Token, sess)
	}
	if got.UserID != store.userID {
		t.Errorf("session row user = %v, want the seeded operator %v", got.UserID, store.userID)
	}
	if want := fixed.Add(devSessionTTL); !got.ExpiresAt.Equal(want) {
		t.Errorf("session expiry = %v, want now+TTL %v", got.ExpiresAt, want)
	}
}

// TestDevAuthMiddleware: every request reaches the inner handler stamped with the
// session cookie AND a CSRF cookie whose value matches the X-CSRF-Token header
// (the double-submit pair), regardless of what the client sent.
func TestDevAuthMiddleware(t *testing.T) {
	var gotSess, gotCSRFCookie, gotCSRFHeader string
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(auth.SessionCookieName); err == nil {
			gotSess = c.Value
		}
		if c, err := r.Cookie(auth.CSRFCookieName); err == nil {
			gotCSRFCookie = c.Value
		}
		gotCSRFHeader = r.Header.Get("X-CSRF-Token")
	})
	// A cached, still-valid pair (anyTokenAuth accepts it) is injected as-is.
	d := &devSessions{
		store: &fakeOAuthStore{}, authn: anyTokenAuth{}, now: time.Now,
		session: "sess-tok", csrf: "csrf-tok",
	}
	h := devAuthMiddleware(d, slog.New(slog.DiscardHandler), inner)

	// A request carrying a STALE session cookie must be overwritten, not merged.
	req := httptest.NewRequest(http.MethodPost, "/api/anything", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "stale"})
	h.ServeHTTP(httptest.NewRecorder(), req)

	if gotSess != "sess-tok" {
		t.Errorf("injected session cookie = %q, want %q (stale cookie must be replaced)", gotSess, "sess-tok")
	}
	if gotCSRFCookie != "csrf-tok" {
		t.Errorf("injected csrf cookie = %q, want %q", gotCSRFCookie, "csrf-tok")
	}
	if gotCSRFHeader != gotCSRFCookie {
		t.Errorf("X-CSRF-Token header %q must match the csrf cookie %q (double-submit)", gotCSRFHeader, gotCSRFCookie)
	}
}

// deadTokenAuth rejects every session token, standing in for a session row that
// a Logout deleted or a TTL expired.
type deadTokenAuth struct{}

func (deadTokenAuth) AuthenticateSession(_ context.Context, _ string) (storage.User, error) {
	return storage.User{}, storage.ErrNotFound
}

// TestDevAuthMiddleware_ReseedsDeadSession: when the cached dev session no
// longer authenticates (the SPA's Logout deleted the row, or the 24h TTL
// expired), the middleware re-seeds a fresh session instead of 401ing every
// request until a process restart, and injects the NEW token.
func TestDevAuthMiddleware_ReseedsDeadSession(t *testing.T) {
	store := &fakeOAuthStore{}
	d := &devSessions{
		store: store, authn: deadTokenAuth{}, now: time.Now,
		session: "logged-out-tok", csrf: "old-csrf",
	}
	var gotSess string
	inner := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(auth.SessionCookieName); err == nil {
			gotSess = c.Value
		}
	})
	h := devAuthMiddleware(d, slog.New(slog.DiscardHandler), inner)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/anything", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(store.sessions) != 1 {
		t.Fatalf("re-seed created %d sessions, want 1", len(store.sessions))
	}
	if gotSess == "logged-out-tok" || gotSess != store.sessions[0].Token {
		t.Errorf("injected session = %q, want the freshly minted %q (not the dead token)", gotSess, store.sessions[0].Token)
	}
}

// TestDevAuthMiddleware_RefusesProxiedRequests: a request carrying reverse-proxy
// evidence is 403'd BEFORE any session is stamped — the loopback bind stops
// container port-mappings, but a same-host proxy still dials 127.0.0.1, and
// auto-authenticating proxied traffic would hand every visitor the operator
// console (ADR-0041).
func TestDevAuthMiddleware_RefusesProxiedRequests(t *testing.T) {
	for _, header := range []string{"X-Forwarded-For", "X-Forwarded-Proto", "Forwarded"} {
		t.Run(header, func(t *testing.T) {
			reached := false
			inner := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { reached = true })
			d := &devSessions{
				store: &fakeOAuthStore{}, authn: anyTokenAuth{}, now: time.Now,
				session: "sess-tok", csrf: "csrf-tok",
			}
			h := devAuthMiddleware(d, slog.New(slog.DiscardHandler), inner)

			req := httptest.NewRequest(http.MethodGet, "/api/anything", nil)
			req.Header.Set(header, "203.0.113.7")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403 for a request carrying %s", rec.Code, header)
			}
			if reached {
				t.Errorf("inner handler reached despite proxy evidence (%s)", header)
			}
		})
	}
}

// TestEnableDevMode ties the opt-out together: it forces the loopback bind, logs a
// loud insecure-mode Warn, and returns a wrapper that auto-authenticates a
// cookieless request through the REAL auth.RequireSession guard (AC2).
func TestEnableDevMode(t *testing.T) {
	store := &fakeOAuthStore{}
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	addr, wrap, err := enableDevMode(context.Background(), store, anyTokenAuth{}, "0.0.0.0:8080", log, time.Now)
	if err != nil {
		t.Fatalf("enableDevMode: %v", err)
	}
	if addr != "127.0.0.1:8080" {
		t.Errorf("dev-mode addr = %q, want the forced loopback bind 127.0.0.1:8080", addr)
	}

	logged := buf.String()
	if !strings.Contains(logged, "level=WARN") || !strings.Contains(logged, "INSECURE") {
		t.Errorf("dev-mode must log a loud WARN insecure-mode warning; got %q", logged)
	}
	if !strings.Contains(logged, "127.0.0.1:8080") {
		t.Errorf("dev-mode warning should name the loopback bind; got %q", logged)
	}

	// The wrapper auto-authenticates: a request with NO cookies passes the real
	// RequireSession guard and reaches the protected handler.
	reached := false
	protected := auth.RequireSession(anyTokenAuth{id: store.userID}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	wrap(protected).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/x/events", nil))
	if !reached || rec.Code != http.StatusOK {
		t.Errorf("cookieless request was not auto-authenticated: reached=%v code=%d", reached, rec.Code)
	}
}

// listerFunc adapts a func to auth.TenantOperatorLister for gate-arming tests.
type listerFunc func(context.Context) ([]string, error)

func (f listerFunc) ListTenantOperatorDiscordIDs(ctx context.Context) ([]string, error) {
	return f(ctx)
}

// TestArmVoiceGMGate pins the standalone voice node's Butler GM-gate arming
// (ADR-0055): the fail-open this closes was runVoice leaving cfg.GMSpeaker nil,
// so the armed gate must NEVER be nil, must admit the tenant-bound operator and
// the env-allowlisted snowflake (the union), and must fail closed on strangers,
// empty SpeakerIDs, an empty identity union, and a failed binding load (which
// degrades to the allowlist, never to fail-open). Dev mode admits every speaker.
func TestArmVoiceGMGate(t *testing.T) {
	ctx := context.Background()
	bound := listerFunc(func(context.Context) ([]string, error) { return []string{"111"}, nil })
	env := envMap(map[string]string{"GLYPHOXA_OPERATOR_IDS": "222"})

	gate := armVoiceGMGate(ctx, bound, env, nil)
	if gate == nil {
		t.Fatal("armVoiceGMGate = nil — the fail-open ADR-0055 closes")
	}
	if !gate("111") {
		t.Error("tenant-bound operator denied, want admitted")
	}
	if !gate("222") {
		t.Error("env-allowlisted snowflake denied, want admitted (union)")
	}
	if gate("999") || gate("") {
		t.Error("stranger or empty SpeakerID admitted, want fail-closed")
	}

	// No identity source at all: armed and closed, not nil/fail-open.
	empty := listerFunc(func(context.Context) ([]string, error) { return nil, nil })
	gate = armVoiceGMGate(ctx, empty, envMap(map[string]string{}), nil)
	if gate == nil || gate("111") {
		t.Error("empty-union gate must be armed and deny everyone")
	}

	// A failed binding load degrades to the allowlist alone — never fail-open.
	broken := listerFunc(func(context.Context) ([]string, error) { return nil, errors.New("db down") })
	gate = armVoiceGMGate(ctx, broken, env, nil)
	if !gate("222") {
		t.Error("allowlisted snowflake denied after a failed binding load, want the allowlist fallback")
	}
	if gate("111") {
		t.Error("bound-only snowflake admitted though the binding load failed, want denied")
	}

	// Dev mode admits every speaker (mirrors the web tier's dev auto-auth).
	gate = armVoiceGMGate(ctx, bound, envMap(map[string]string{"GLYPHOXA_DEV_MODE": "1"}), nil)
	for _, id := range []string{"111", "999", ""} {
		if !gate(id) {
			t.Errorf("dev gate(%q) = false, want admit-all", id)
		}
	}
}
