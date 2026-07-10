//go:build integration

// These tests stand up a real Postgres (testcontainers) and are tag-isolated
// behind `integration` so a plain `go test ./...` stays Docker-free (ADR-0021 /
// ADR-0033). Run with `go test -tags=integration ./internal/bundle/...`.
package bundle_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
)

const pgImage = "pgvector/pgvector:pg17"

// startPostgres spins up a pgvector container and returns its DSN, or skips
// LOUDLY when Docker is unavailable so a green run can't be mistaken for real DB
// coverage. GLYPHOXA_TEST_DSN points at an external pgvector Postgres instead.
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
		t.Skipf("SKIPPED DB TEST — NO POSTGRES: could not start %s (is Docker running?). "+
			"Set GLYPHOXA_TEST_DSN to an external pgvector Postgres. error: %v", pgImage, err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	return dsn
}

// migratedPool returns a migrated pgx pool against a fresh Postgres.
func migratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := startPostgres(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := storage.MigrateUp(context.Background(), db); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	_ = db.Close()
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// testCipher builds a random-key AES-GCM cipher for the seed's sealed placeholders.
func testCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	c, err := crypto.New(key)
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	return c
}
