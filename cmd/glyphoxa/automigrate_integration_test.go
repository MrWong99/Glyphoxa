//go:build integration

// These tests stand up a real Postgres (testcontainers) and are tag-isolated
// behind `integration` so the default `go test ./...` stays Docker-free and
// fast (ADR-0033). Run them with `go test -tags=integration`.

package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
)

// pgImage carries the pgvector extension; stock postgres can't run
// `CREATE EXTENSION vector` (ADR-0011).
const pgImage = "pgvector/pgvector:pg17"

// startPostgres spins up a pgvector container and returns its connection string,
// skipping loudly when Docker is unavailable. GLYPHOXA_TEST_DSN points at an
// external pgvector Postgres to skip the container entirely.
func startPostgres(t *testing.T) string {
	t.Helper()
	if dsn := os.Getenv("GLYPHOXA_TEST_DSN"); dsn != "" {
		return dsn
	}
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, pgImage,
		tcpostgres.WithDatabase("glyphoxa_test"),
		tcpostgres.WithUsername("glyphoxa"),
		tcpostgres.WithPassword("glyphoxa"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Skipf("SKIPPED DB TEST — NO POSTGRES: could not start the %s container "+
			"(is Docker running?). Set GLYPHOXA_TEST_DSN to an external pgvector "+
			"Postgres to run it without Docker. underlying error: %v", pgImage, err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	return dsn
}

// TestAutoMigrate is the ADR-0031/ADR-0034 self-host contract that makes a bare
// `glyphoxa -mode all` (and thus `docker compose up` / `systemctl start`) reach a
// migrated DB with no manual `migrate up` step (issue #282): a fresh, empty DB
// starts BEHIND the embedded schema (EnsureSchemaCurrent errors), autoMigrate
// brings it current (EnsureSchemaCurrent then passes), and a second call is a
// no-op (idempotent under the advisory lock).
func TestAutoMigrate(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	// Before: a fresh DB has no schema, so the fail-fast guard rejects it.
	if err := wirenpc.EnsureSchemaCurrent(ctx, dsn); err == nil {
		t.Fatal("EnsureSchemaCurrent on a fresh, unmigrated DB returned nil, want a stale-schema error")
	}

	if err := autoMigrate(ctx, dsn); err != nil {
		t.Fatalf("autoMigrate on a fresh DB: %v", err)
	}

	// After: the schema is current, so the guard every serving Mode runs passes.
	if err := wirenpc.EnsureSchemaCurrent(ctx, dsn); err != nil {
		t.Fatalf("EnsureSchemaCurrent after autoMigrate: %v, want nil (schema should be current)", err)
	}

	// Idempotent: re-running against an already-current DB is a clean no-op.
	if err := autoMigrate(ctx, dsn); err != nil {
		t.Fatalf("autoMigrate second run (already current): %v, want nil", err)
	}
}
