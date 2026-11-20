package storage

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sync"

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

	// WithAllowOutofOrder: migrations are numbered by whoever opens the PR, and
	// parallel PRs merge in any order, so a database can already be at 00058
	// when 00056 first reaches it. goose's default refuses that ("found missing
	// migrations") and would brick `migrate up`; with the option a lower-numbered
	// migration that is not yet applied is simply applied. Each migration must
	// therefore be written to hold regardless of its neighbours' order — which
	// independent PRs' migrations are by construction. The option also makes
	// HasPending compare the applied SET rather than the highest version, which
	// [EnsureCurrent] depends on.
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS,
		goose.WithSessionLocker(locker), goose.WithAllowOutofOrder(true))
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

// EnsureCurrent verifies that EVERY embedded migration has been applied. web
// and voice Modes call this at startup and fail fast if the schema is behind
// (ADR-0031) — they never auto-migrate.
//
// "Behind" is judged on the applied set, not the highest version: comparing
// maxima alone called a database current when a lower-numbered migration had
// merged after a higher one and was never applied — the process then booted
// against a schema missing that migration.
//
// A schema AHEAD of this binary (the database carries a version newer than any
// migration embedded here) is allowed: it is the normal state of an old pod
// during a rolling upgrade, and of a rollback. It is logged at WARN, once per
// process: the binary is running against tables a newer release has changed,
// and "everything started fine" is otherwise all an operator sees before the
// first query fails.
func EnsureCurrent(ctx context.Context, db *sql.DB) error {
	p, err := NewMigrationProvider(db)
	if err != nil {
		return err
	}
	raw, err := p.Status(ctx)
	if err != nil {
		return fmt.Errorf("storage: read migration status: %w", err)
	}
	var pending []int64
	var embeddedMax int64
	for _, st := range raw {
		if st.State != goose.StateApplied {
			pending = append(pending, st.Source.Version)
		}
		embeddedMax = max(embeddedMax, st.Source.Version)
	}
	if len(pending) > 0 {
		return fmt.Errorf(
			"storage: schema out of date (%d unapplied migration(s): %v); run `glyphoxa migrate up`",
			len(pending), pending)
	}
	// Every embedded migration is applied. Is the database further along still?
	// Best-effort: a failed version read only costs the warning.
	if dbVersion, err := p.GetDBVersion(ctx); err == nil && schemaAhead(dbVersion, embeddedMax) {
		// Once per process: EnsureCurrent runs at every Voice Session start, and
		// the condition does not change while the process lives.
		aheadWarnOnce.Do(func() {
			slog.WarnContext(ctx, "storage: database schema is NEWER than this binary; it was migrated by a later release. Queries may fail on changed tables: upgrade this binary, or finish the rollback by migrating down",
				"db_version", dbVersion, "binary_version", embeddedMax)
		})
	}
	return nil
}

// aheadWarnOnce limits EnsureCurrent's "schema newer than binary" warning to one
// line per process.
var aheadWarnOnce sync.Once

// schemaAhead reports whether the database's highest applied version is newer
// than every migration this binary embeds: it was migrated by a later release.
func schemaAhead(dbVersion, embeddedMax int64) bool {
	return dbVersion > embeddedMax
}
