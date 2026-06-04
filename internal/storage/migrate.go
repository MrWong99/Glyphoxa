package storage

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/MrWong99/Glyphoxa/internal/storage/migrations"
)

// migrationLockName is hashed to the advisory-lock ID the goose session locker
// acquires. ADR-0031 mandates a Postgres session locker named
// "glyphoxa_migrations" so concurrent web/voice startups serialize. goose's
// real API (lock.NewPostgresSessionLocker + lock.WithLockID) takes an int64
// lock ID rather than a string — the ADR snippet's lock.NewSessionLocker(db,
// name) signature does not exist in goose v3 — so we honor the ADR's intent by
// deriving a stable int64 from the name.
const migrationLockName = "glyphoxa_migrations"

// migrationLockID derives the int64 advisory-lock ID from migrationLockName.
// FNV-1a is deterministic across processes and Go versions, so every instance
// that boots converges on the same lock and they serialize correctly.
func migrationLockID() int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(migrationLockName))
	return int64(h.Sum64()) //nolint:gosec // intentional wrap to int64; we only need a stable, shared key.
}

// NewMigrationProvider builds the goose Provider over the embedded migrations,
// configured with the Postgres session locker required by ADR-0031.
//
// db must be a *sql.DB on a Postgres driver (e.g. pgx/v5/stdlib). goose needs a
// database/sql handle; the application's own queries use a pgxpool separately.
func NewMigrationProvider(db *sql.DB) (*goose.Provider, error) {
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockID(migrationLockID()))
	if err != nil {
		return nil, fmt.Errorf("storage: build migration session locker: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS,
		goose.WithSessionLocker(locker))
	if err != nil {
		return nil, fmt.Errorf("storage: build migration provider: %w", err)
	}
	return provider, nil
}

// MigrateUp applies all pending migrations (locked). Called at startup in `all`
// Mode (ADR-0031); web/voice Modes do not auto-migrate.
func MigrateUp(ctx context.Context, db *sql.DB) error {
	p, err := NewMigrationProvider(db)
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("storage: migrate up: %w", err)
	}
	return nil
}

// MigrateDown rolls back the most recently applied migration (locked).
func MigrateDown(ctx context.Context, db *sql.DB) error {
	p, err := NewMigrationProvider(db)
	if err != nil {
		return err
	}
	if _, err := p.Down(ctx); err != nil {
		return fmt.Errorf("storage: migrate down: %w", err)
	}
	return nil
}

// MigrationStatus is one migration's applied/pending state, for `migrate status`.
type MigrationStatus struct {
	Version int64
	Source  string
	Applied bool
}

// Status returns the state of every known migration, ordered by version.
func Status(ctx context.Context, db *sql.DB) ([]MigrationStatus, error) {
	p, err := NewMigrationProvider(db)
	if err != nil {
		return nil, err
	}
	raw, err := p.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("storage: migrate status: %w", err)
	}
	out := make([]MigrationStatus, 0, len(raw))
	for _, s := range raw {
		out = append(out, MigrationStatus{
			Version: s.Source.Version,
			Source:  s.Source.Path,
			Applied: s.State == goose.StateApplied,
		})
	}
	return out, nil
}

// Version returns the current (highest applied) schema version, 0 if none.
func Version(ctx context.Context, db *sql.DB) (int64, error) {
	p, err := NewMigrationProvider(db)
	if err != nil {
		return 0, err
	}
	v, err := p.GetDBVersion(ctx)
	if err != nil {
		return 0, fmt.Errorf("storage: db version: %w", err)
	}
	return v, nil
}

// EnsureCurrent verifies the DB schema is at the latest known version. web and
// voice Modes call this at startup and fail fast if the schema is behind
// (ADR-0031) — they never auto-migrate.
func EnsureCurrent(ctx context.Context, db *sql.DB) error {
	p, err := NewMigrationProvider(db)
	if err != nil {
		return err
	}
	current, target, err := p.GetVersions(ctx)
	if err != nil {
		return fmt.Errorf("storage: read versions: %w", err)
	}
	if current < target {
		return fmt.Errorf(
			"storage: schema out of date (db at version %d, want %d); run `glyphoxa migrate up`",
			current, target)
	}
	return nil
}
