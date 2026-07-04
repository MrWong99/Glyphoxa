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

// TestRunWebSessionSweepWiring pins the #184 boot-time revocation sweep into
// runWeb: with a fully-configured gate and an unreachable database, the boot
// must fail INSIDE the sweep (it is the first DB touch — the pool dials
// lazily), proving the wiring exists; deleting the sweep block would surface a
// different error. Dev mode must skip the sweep entirely (its first DB touch is
// the dev-session seed instead). DB-free: the DSN points at a closed loopback
// port.
func TestRunWebSessionSweepWiring(t *testing.T) {
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
		t.Fatal("runWeb with an unreachable DB returned nil, want the sweep error")
	}
	if !strings.Contains(err.Error(), "revoke sessions outside the operator allowlist") {
		t.Errorf("boot error = %q, want it to fail inside the #184 session sweep", err)
	}

	// Dev mode has no allowlist: the sweep is skipped and the first DB touch is
	// the dev-session seed.
	t.Setenv("GLYPHOXA_DEV_MODE", "1")
	err = runWeb(log, wirenpc.Config{}, metrics, "127.0.0.1:0", "", false)
	if err == nil {
		t.Fatal("runWeb (dev mode) with an unreachable DB returned nil, want an error")
	}
	if strings.Contains(err.Error(), "allowlist") {
		t.Errorf("dev-mode boot error = %q, must not come from the allowlist sweep", err)
	}
}
