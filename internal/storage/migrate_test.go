package storage_test

import (
	"context"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestMigrateUpDown is the reversibility check ADR-0031 / task #8 require:
// migrations must apply up and roll back down cleanly. Run up → assert version,
// then down → assert version 0 (and that the enum types / tables are gone, so a
// re-up does not collide with leftover CREATE TYPE).
func TestMigrateUpDown(t *testing.T) {
	dsn := startPostgres(t)
	db := openSQL(t, dsn)
	ctx := context.Background()

	if err := storage.MigrateUp(ctx, db); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	v, err := storage.Version(ctx, db)
	if err != nil {
		t.Fatalf("version after up: %v", err)
	}
	if v < 1 {
		t.Fatalf("expected schema version >= 1 after up, got %d", v)
	}

	statuses, err := storage.Status(ctx, db)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(statuses) == 0 {
		t.Fatal("expected at least one migration in status")
	}
	for _, s := range statuses {
		if !s.Applied {
			t.Errorf("migration %d (%s) not applied after up", s.Version, s.Source)
		}
	}

	// EnsureCurrent must pass once the schema is at head.
	if err := storage.EnsureCurrent(ctx, db); err != nil {
		t.Fatalf("EnsureCurrent after up: %v", err)
	}

	// Roll back every applied migration.
	for {
		v, err := storage.Version(ctx, db)
		if err != nil {
			t.Fatalf("version during down loop: %v", err)
		}
		if v == 0 {
			break
		}
		if err := storage.MigrateDown(ctx, db); err != nil {
			t.Fatalf("migrate down from version %d: %v", v, err)
		}
	}

	// A fresh up after a full down must succeed — proves down dropped the enum
	// types and tables (a half-reversed down would fail "type already exists").
	if err := storage.MigrateUp(ctx, db); err != nil {
		t.Fatalf("re-apply up after full down: %v", err)
	}
}
