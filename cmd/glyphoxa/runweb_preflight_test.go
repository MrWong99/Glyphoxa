package main

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
)

// TestRunWebPreflightWiring pins the requireWebEnv preflight into runWeb itself
// (issue #112 AC5): a web-Mode boot with no OAuth env must fail with the error
// naming the missing variables, BEFORE any database is needed — deleting the
// preflight block from runWeb previously passed the whole unit suite. Both
// cases run DB-free because they error out before the pool opens.
func TestRunWebPreflightWiring(t *testing.T) {
	for _, k := range []string{
		"DISCORD_OAUTH_CLIENT_ID",
		"DISCORD_OAUTH_CLIENT_SECRET",
		"DISCORD_OAUTH_REDIRECT_URL",
		"GLYPHOXA_OPERATOR_IDS",
		"GLYPHOXA_DEV_MODE",
		"GLYPHOXA_DATABASE_URL",
		"DATABASE_URL",
	} {
		t.Setenv(k, "")
	}
	log := slog.New(slog.DiscardHandler)
	metrics := observe.NewPrometheusRecorder()

	// No OAuth env, no dev mode → the preflight refuses the boot and names the
	// variables; the missing database is NOT the error (preflight runs first).
	err := runWeb(log, wirenpc.Config{}, metrics, "127.0.0.1:0", "", false)
	if err == nil {
		t.Fatal("runWeb with no OAuth env returned nil, want the ADR-0041 preflight error")
	}
	for _, want := range webEnvVars {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("preflight error %q does not name %s", err, want)
		}
	}

	// Dev mode skips the preflight: the boot proceeds past it and fails on the
	// (deliberately unset) database instead.
	t.Setenv("GLYPHOXA_DEV_MODE", "1")
	err = runWeb(log, wirenpc.Config{}, metrics, "127.0.0.1:0", "", false)
	if err == nil {
		t.Fatal("runWeb in dev mode with no DB returned nil, want a database error")
	}
	if !strings.Contains(err.Error(), "database") {
		t.Errorf("dev-mode error = %q, want the missing-database error (preflight must be skipped)", err)
	}
}

// TestRunWebAdmissionPreflightWiring pins the ADR-0055 admission wiring into
// the REAL runWeb, DB-free: an unparsable GLYPHOXA_ADMISSION_MODE refuses the
// boot before anything else (even in dev mode — a posture typo never ships
// dark), and an explicit `open` posture relaxes exactly the allowlist branch
// of the env preflight: with OAuth set and NO allowlist the boot proceeds past
// the preflight and fails on the (deliberately unset) database instead.
func TestRunWebAdmissionPreflightWiring(t *testing.T) {
	for _, k := range []string{
		"DISCORD_OAUTH_CLIENT_ID",
		"DISCORD_OAUTH_CLIENT_SECRET",
		"DISCORD_OAUTH_REDIRECT_URL",
		"GLYPHOXA_OPERATOR_IDS",
		"GLYPHOXA_DEV_MODE",
		"GLYPHOXA_DATABASE_URL",
		"DATABASE_URL",
	} {
		t.Setenv(k, "")
	}
	log := slog.New(slog.DiscardHandler)
	metrics := observe.NewPrometheusRecorder()

	// A typo'd posture refuses the boot, naming the env var.
	t.Setenv("GLYPHOXA_ADMISSION_MODE", "opne")
	err := runWeb(log, wirenpc.Config{}, metrics, "127.0.0.1:0", "", false)
	if err == nil || !strings.Contains(err.Error(), "GLYPHOXA_ADMISSION_MODE") {
		t.Fatalf("boot with a typo'd admission mode = %v, want a refusal naming GLYPHOXA_ADMISSION_MODE", err)
	}
	// Even dev mode refuses a typo'd posture (parse runs before the dev skip).
	t.Setenv("GLYPHOXA_DEV_MODE", "1")
	if err := runWeb(log, wirenpc.Config{}, metrics, "127.0.0.1:0", "", false); err == nil ||
		!strings.Contains(err.Error(), "GLYPHOXA_ADMISSION_MODE") {
		t.Fatalf("dev boot with a typo'd admission mode = %v, want the same refusal", err)
	}
	t.Setenv("GLYPHOXA_DEV_MODE", "")

	// Explicit open mode + OAuth set + NO allowlist: past the env preflight,
	// down to the missing-database error — the ADR-0055 relaxation through the
	// real boot path.
	t.Setenv("GLYPHOXA_ADMISSION_MODE", "open")
	t.Setenv("DISCORD_OAUTH_CLIENT_ID", "cid")
	t.Setenv("DISCORD_OAUTH_CLIENT_SECRET", "secret")
	t.Setenv("DISCORD_OAUTH_REDIRECT_URL", "https://x/cb")
	err = runWeb(log, wirenpc.Config{}, metrics, "127.0.0.1:0", "", false)
	if err == nil {
		t.Fatal("open-mode boot with no DB returned nil, want the database error")
	}
	if !strings.Contains(err.Error(), "database") {
		t.Errorf("open-mode error = %q, want the missing-database error (allowlist requirement must be relaxed)", err)
	}

	// Allowlist mode (unset env) with the same OAuth-only config still refuses
	// on the allowlist — the relaxation is open-mode-only.
	t.Setenv("GLYPHOXA_ADMISSION_MODE", "")
	err = runWeb(log, wirenpc.Config{}, metrics, "127.0.0.1:0", "", false)
	if err == nil || !strings.Contains(err.Error(), "GLYPHOXA_OPERATOR_IDS") {
		t.Errorf("allowlist-mode error = %v, want the GLYPHOXA_OPERATOR_IDS refusal", err)
	}
}

// TestRunWebSchemaPreflightWiring pins the ADR-0031/ADR-0034 schema preflight
// into runWeb as the FIRST DB touch of a web/all boot (issue #282, finding #1).
// A web-only replica (withVoice=false) must verify the schema BEFORE the #184
// session sweep, so a behind/unreachable DB fails fast at the preflight with the
// actionable message rather than surfacing a raw "relation does not exist" from
// a later query. With a fully-configured gate and an unreachable database the
// boot fails at "schema preflight"; deleting the preflight block would push the
// first error into the sweep instead. DB-free: the DSN points at a closed
// loopback port.
func TestRunWebSchemaPreflightWiring(t *testing.T) {
	t.Setenv("DISCORD_OAUTH_CLIENT_ID", "cid")
	t.Setenv("DISCORD_OAUTH_CLIENT_SECRET", "secret")
	t.Setenv("DISCORD_OAUTH_REDIRECT_URL", "https://x/cb")
	t.Setenv("GLYPHOXA_OPERATOR_IDS", "770000000000000000")
	t.Setenv("GLYPHOXA_DEV_MODE", "")
	t.Setenv("GLYPHOXA_DATABASE_URL", "postgres://glyphoxa:x@127.0.0.1:1/glyphoxa?sslmode=disable&connect_timeout=1")
	t.Setenv("DATABASE_URL", "")

	log := slog.New(slog.DiscardHandler)
	metrics := observe.NewPrometheusRecorder()

	err := runWeb(log, wirenpc.Config{}, metrics, "127.0.0.1:0", "", false)
	if err == nil {
		t.Fatal("runWeb with an unreachable DB returned nil, want the schema-preflight error")
	}
	if !strings.Contains(err.Error(), "schema preflight") {
		t.Errorf("boot error = %q, want it to fail at the schema preflight (the first DB touch, before the #184 sweep)", err)
	}
	// The preflight is a strict fail-fast: the sweep must NOT have run yet.
	if strings.Contains(err.Error(), "revoke sessions outside the operator allowlist") {
		t.Errorf("boot error = %q reached the #184 sweep; the schema preflight must fail first", err)
	}

	// Dev mode still runs the preflight (the throwaway DB needs a schema too); the
	// point is only that the boot never reaches the allowlist sweep.
	t.Setenv("GLYPHOXA_DEV_MODE", "1")
	err = runWeb(log, wirenpc.Config{}, metrics, "127.0.0.1:0", "", false)
	if err == nil {
		t.Fatal("runWeb (dev mode) with an unreachable DB returned nil, want an error")
	}
	if strings.Contains(err.Error(), "allowlist") {
		t.Errorf("dev-mode boot error = %q, must not come from the allowlist sweep", err)
	}
}
