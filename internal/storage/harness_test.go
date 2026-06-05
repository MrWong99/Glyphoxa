//go:build integration

// These tests stand up a real Postgres (testcontainers) and are tag-isolated
// behind `integration` so the default `go test ./...` stays Docker-free and
// fast (ADR-0021 / ADR-0033). Run them with `go test -tags=integration`.

package storage_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// pgImage carries the pgvector extension; stock postgres can't run
// `CREATE EXTENSION vector` (ADR-0011).
const pgImage = "pgvector/pgvector:pg17"

// startPostgres spins up a pgvector container and returns its connection
// string. The DB tests skip cleanly (t.Skip) when Docker is unavailable — a
// plain `go test ./...` on a box without Docker must not hard-fail. Set
// GLYPHOXA_TEST_DSN to point at an external Postgres (with pgvector) and skip
// the container entirely.
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
		// Docker absent / unreachable: skip LOUDLY rather than fail, so a green
		// `go test` that never touched a real DB can't be mistaken for real
		// coverage. testcontainers surfaces this as a docker-socket connect
		// error. Point at the escape hatch (GLYPHOXA_TEST_DSN) too.
		t.Skipf("SKIPPED DB TEST — NO POSTGRES: could not start the %s container "+
			"(is Docker running?). These tests exercise migrations + queries against a "+
			"real DB and were NOT run. Set GLYPHOXA_TEST_DSN to an external pgvector "+
			"Postgres to run them without Docker. underlying error: %v", pgImage, err)
	}
	t.Cleanup(func() {
		_ = testcontainers.TerminateContainer(container)
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	return dsn
}

// openSQL opens a database/sql handle on the pgx driver (for goose).
func openSQL(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// openPool opens a pgx pool (for the Store).
func openPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
