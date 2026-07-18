//go:build integration

// Dev-mode auto-auth over a real Postgres (testcontainers): GLYPHOXA_DEV_MODE
// seeds the synthetic operator + a real session, and devAuthMiddleware injects it
// so a cookieless caller passes the UNCHANGED auth.Stack (auth + CSRF + tenant)
// and reaches the gated API as the seeded operator (ADR-0041, issue #112).
// Tag-isolated behind `integration` so the default `go test ./...` stays
// Docker-free (ADR-0033). The Postgres harness is duplicated in miniature
// (storage's harness_test.go is package-private), mirroring auth_e2e_test.go.

package main

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

const bootPGImage = "pgvector/pgvector:pg17"

func bootMigratedStore(t *testing.T) *storage.Store {
	t.Helper()
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, bootPGImage,
		tcpostgres.WithDatabase("glyphoxa_test"),
		tcpostgres.WithUsername("glyphoxa"),
		tcpostgres.WithPassword("glyphoxa"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Skipf("SKIPPED DB TEST — NO POSTGRES: could not start %s (is Docker running?): %v", bootPGImage, err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.MigrateUp(ctx, db); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return storage.New(pool)
}

// TestDevModeAutoAuthEndToEnd drives the whole GLYPHOXA_DEV_MODE path against a
// real DB: seed the dev session, wrap the real AuthService handler (mounted with
// the real interceptor stack) in devAuthMiddleware, and prove a cookieless client
// is auto-authenticated as the seeded operator on a read AND that a mutation
// passes the CSRF double-submit — all through the unchanged gate.
func TestDevModeAutoAuthEndToEnd(t *testing.T) {
	store := bootMigratedStore(t)
	ctx := context.Background()

	stack := auth.NewStack(store, store, managementv1connect.AuthServiceGetCurrentUserProcedure)
	authServer := auth.NewAuthServer(store, store, nil)
	mux := http.NewServeMux()
	mux.Handle(authServer.Handler(stack.HandlerOptions()...))
	// The whole tier is wrapped exactly as runWeb wraps its mounts in dev-mode:
	// devSessions seeds lazily on the first request and re-seeds after a logout.
	d := &devSessions{store: store, authn: store, now: time.Now}
	srv := httptest.NewServer(devAuthMiddleware(d, slog.New(slog.DiscardHandler), mux))
	t.Cleanup(srv.Close)

	client := managementv1connect.NewAuthServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())

	// A cookieless read is auto-authenticated as the seeded operator (AC2).
	resp, err := client.GetCurrentUser(ctx, connect.NewRequest(&managementv1.GetCurrentUserRequest{}))
	if err != nil {
		t.Fatalf("GetCurrentUser (dev-mode, no cookie): %v", err)
	}
	if got := resp.Msg.GetUser().GetName(); got != "Dev Operator" {
		t.Errorf("auto-authenticated user = %q, want %q", got, "Dev Operator")
	}

	// A mutation with NO client-supplied CSRF header still passes: the middleware
	// injects the matching double-submit pair, so the unchanged CSRF interceptor
	// admits it (AC2 — mutations are not 403'd in dev-mode).
	if _, err := client.Logout(ctx, connect.NewRequest(&managementv1.LogoutRequest{})); err != nil {
		t.Fatalf("Logout (dev-mode, no client CSRF header): %v", err)
	}

	// Logout deleted the seeded session row. The next request must be re-seeded
	// and authenticate again — without re-seeding, a dev instance would 401
	// every request until a process restart.
	resp, err = client.GetCurrentUser(ctx, connect.NewRequest(&managementv1.GetCurrentUserRequest{}))
	if err != nil {
		t.Fatalf("GetCurrentUser after Logout (re-seeded dev session): %v", err)
	}
	if got := resp.Msg.GetUser().GetName(); got != "Dev Operator" {
		t.Errorf("re-seeded user = %q, want %q", got, "Dev Operator")
	}
}
