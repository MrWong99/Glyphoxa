//go:build integration

// These tests stand up a real Postgres (testcontainers) and are tag-isolated
// behind `integration` so the default `go test ./...` stays Docker-free and fast
// (ADR-0021 / ADR-0033). Run them with `go test -tags=integration`. The harness
// mirrors internal/storage/harness_test.go and internal/rpc — start a pgvector
// container, migrate up, then exercise the blob Store against a seeded tenant.

package blob_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

const pgImage = "pgvector/pgvector:pg17"

// startPostgres spins up a pgvector container and returns its DSN, or skips
// loudly when Docker is unavailable (a Docker-less `go test` must not hard-fail).
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
			"Postgres to run these without Docker. underlying error: %v", pgImage, err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	return dsn
}

// newStore migrates the schema up and returns a blob Store plus the raw pool
// (for row-count assertions the seam deliberately does not expose).
func newStore(t *testing.T) (*blob.Postgres, *pgxpool.Pool) {
	t.Helper()
	dsn := startPostgres(t)

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	if err := storage.MigrateUp(context.Background(), db); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return blob.NewPostgres(pool), pool
}

// seedTenant inserts a tenant and returns its id — blob rows FK to tenant(id).
func seedTenant(t *testing.T, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := pool.QueryRow(context.Background(),
		`INSERT INTO tenant (name) VALUES ($1) RETURNING id`, name).Scan(&id)
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return id
}

func countBlobs(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM blob`).Scan(&n); err != nil {
		t.Fatalf("count blobs: %v", err)
	}
	return n
}

func mustKey(t *testing.T, tenant, owner uuid.UUID, name string) string {
	t.Helper()
	key, err := blob.Key(tenant, "highlight", owner, name)
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	return key
}

// TestPutGetRoundTrip stores a blob and reads it back with correct bytes and
// Meta; an unknown key yields ErrNotFound.
func TestPutGetRoundTrip(t *testing.T) {
	store, pool := newStore(t)
	ctx := context.Background()
	tenant := seedTenant(t, pool, "acme")
	key := mustKey(t, tenant, uuid.New(), "clip.opus")

	payload := []byte("hello blob world")
	if err := store.Put(ctx, key, "audio/opus", bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rc, meta, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("bytes = %q, want %q", got, payload)
	}
	if meta.ContentType != "audio/opus" {
		t.Errorf("ContentType = %q, want audio/opus", meta.ContentType)
	}
	if meta.Size != int64(len(payload)) {
		t.Errorf("Size = %d, want %d", meta.Size, len(payload))
	}
	if meta.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}

	missing := mustKey(t, tenant, uuid.New(), "nope.opus")
	if _, _, err := store.Get(ctx, missing); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("Get unknown err = %v, want ErrNotFound", err)
	}
}

// TestPutUpsert proves a second Put at the same key overwrites and leaves a
// single row.
func TestPutUpsert(t *testing.T) {
	store, pool := newStore(t)
	ctx := context.Background()
	tenant := seedTenant(t, pool, "acme")
	key := mustKey(t, tenant, uuid.New(), "clip.opus")

	if err := store.Put(ctx, key, "text/plain", bytes.NewReader([]byte("first")), 5); err != nil {
		t.Fatalf("Put first: %v", err)
	}
	if err := store.Put(ctx, key, "audio/opus", bytes.NewReader([]byte("second!")), 7); err != nil {
		t.Fatalf("Put second: %v", err)
	}

	if n := countBlobs(t, pool); n != 1 {
		t.Fatalf("row count = %d, want 1 (upsert)", n)
	}
	rc, meta, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "second!" || meta.ContentType != "audio/opus" || meta.Size != 7 {
		t.Fatalf("after upsert got %q/%q/%d, want second!/audio/opus/7", got, meta.ContentType, meta.Size)
	}
}

// TestPutSizeMismatch rejects a stream shorter or longer than the declared size,
// writing no row in either case.
func TestPutSizeMismatch(t *testing.T) {
	store, pool := newStore(t)
	ctx := context.Background()
	tenant := seedTenant(t, pool, "acme")

	// Declared longer than actual (short stream).
	shortKey := mustKey(t, tenant, uuid.New(), "short.bin")
	if err := store.Put(ctx, shortKey, "application/octet-stream", bytes.NewReader([]byte("abc")), 10); err == nil {
		t.Fatal("Put short stream err = nil, want error")
	}
	// Declared shorter than actual (long stream).
	longKey := mustKey(t, tenant, uuid.New(), "long.bin")
	if err := store.Put(ctx, longKey, "application/octet-stream", bytes.NewReader([]byte("abcdefghij")), 3); err == nil {
		t.Fatal("Put long stream err = nil, want error")
	}

	if n := countBlobs(t, pool); n != 0 {
		t.Fatalf("row count = %d, want 0 (no row on size mismatch)", n)
	}
}

// TestDeleteIdempotent removes a blob (Get then ErrNotFound) and proves a second
// Delete of the now-absent key is a no-op nil.
func TestDeleteIdempotent(t *testing.T) {
	store, pool := newStore(t)
	ctx := context.Background()
	tenant := seedTenant(t, pool, "acme")
	key := mustKey(t, tenant, uuid.New(), "clip.opus")

	if err := store.Put(ctx, key, "audio/opus", bytes.NewReader([]byte("data")), 4); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := store.Get(ctx, key); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("Get after Delete err = %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete absent err = %v, want nil (idempotent)", err)
	}
}

// TestTenantIsolation proves two tenants can hold blobs at the same owner-kind /
// owner-id / name suffix without colliding, each reading its own bytes — the key
// carries the tenant, so there is no cross-tenant read. A prefix-less key is
// rejected before any query.
func TestTenantIsolation(t *testing.T) {
	store, pool := newStore(t)
	ctx := context.Background()
	tenantA := seedTenant(t, pool, "tenant-a")
	tenantB := seedTenant(t, pool, "tenant-b")

	owner := uuid.New() // same owner id + name on both sides
	keyA := mustKey(t, tenantA, owner, "clip.opus")
	keyB := mustKey(t, tenantB, owner, "clip.opus")

	if err := store.Put(ctx, keyA, "audio/opus", bytes.NewReader([]byte("AAA")), 3); err != nil {
		t.Fatalf("Put A: %v", err)
	}
	if err := store.Put(ctx, keyB, "audio/opus", bytes.NewReader([]byte("BBB")), 3); err != nil {
		t.Fatalf("Put B: %v", err)
	}

	rcA, _, err := store.Get(ctx, keyA)
	if err != nil {
		t.Fatalf("Get A: %v", err)
	}
	defer rcA.Close()
	gotA, _ := io.ReadAll(rcA)
	rcB, _, err := store.Get(ctx, keyB)
	if err != nil {
		t.Fatalf("Get B: %v", err)
	}
	defer rcB.Close()
	gotB, _ := io.ReadAll(rcB)
	if string(gotA) != "AAA" || string(gotB) != "BBB" {
		t.Fatalf("cross-tenant bleed: A=%q B=%q, want AAA/BBB", gotA, gotB)
	}
	if n := countBlobs(t, pool); n != 2 {
		t.Fatalf("row count = %d, want 2", n)
	}

	// A key without a tenant prefix never reaches SQL.
	if err := store.Put(ctx, "no-prefix-key", "audio/opus", bytes.NewReader([]byte("x")), 1); !errors.Is(err, blob.ErrInvalidKey) {
		t.Fatalf("Put prefix-less err = %v, want ErrInvalidKey", err)
	}
}

// TestSizeCapBoundary proves the cap edge: exactly MaxSize succeeds, MaxSize+1 is
// ErrTooLarge (rejected before any read). One MaxSize buffer only — do not
// table-drive multiple 32 MiB allocations.
func TestSizeCapBoundary(t *testing.T) {
	store, pool := newStore(t)
	ctx := context.Background()
	tenant := seedTenant(t, pool, "acme")

	atCap := bytes.Repeat([]byte{0x7}, int(blob.MaxSize))
	okKey := mustKey(t, tenant, uuid.New(), "big.bin")
	if err := store.Put(ctx, okKey, "application/octet-stream", bytes.NewReader(atCap), blob.MaxSize); err != nil {
		t.Fatalf("Put at cap: %v", err)
	}

	overKey := mustKey(t, tenant, uuid.New(), "toobig.bin")
	if err := store.Put(ctx, overKey, "application/octet-stream", bytes.NewReader(atCap), blob.MaxSize+1); !errors.Is(err, blob.ErrTooLarge) {
		t.Fatalf("Put over cap err = %v, want ErrTooLarge", err)
	}
}
