//go:build integration

// These tests stand up a real Postgres (testcontainers) to exercise the
// fail-fast-on-stale-schema contract ADR-0031 mandates for serving modes, so
// they are tag-isolated behind `integration` (ADR-0033): the default
// `go test ./...` stays Docker-free per ADR-0021, and a dedicated
// `-tags=integration` CI job runs these with Docker. The runtime t.Skip on a
// missing Postgres (via dsnFromEnvOrContainer) remains for local convenience.
//
// The shared container harness (pgImage, dsnFromEnvOrContainer) lives in
// agentspec_test.go in this package.
package wirenpc

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// openSQL opens a database/sql handle on the pgx stdlib driver (for goose).
func openSQL(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// openPool opens a pgxpool the way the production callers (cmd/glyphoxa) do, so
// the RunFromDB seam is exercised against a real pool, not a dsn string.
func openPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// migrateToHead applies every embedded migration so the DB schema is current.
func migrateToHead(t *testing.T, dsn string) {
	t.Helper()
	db := openSQL(t, dsn)
	if err := storage.MigrateUp(context.Background(), db); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
}

// rollBackOne rolls back the most recently applied migration, leaving the DB
// one version behind head — a deliberately stale schema.
func rollBackOne(t *testing.T, dsn string) {
	t.Helper()
	db := openSQL(t, dsn)
	if err := storage.MigrateDown(context.Background(), db); err != nil {
		t.Fatalf("migrate down: %v", err)
	}
}

// TestEnsureSchemaCurrent_StaleSchemaFailsFast is the red→green core of #32:
// serving startup must verify the DB is at the latest embedded schema BEFORE any
// other DB interaction and refuse to start if the DB is behind (ADR-0031). We
// migrate to head, then roll back one migration so the DB version < embedded
// latest, and assert the schema-check seam RunFromDB calls returns the existing
// actionable version-mismatch error.
func TestEnsureSchemaCurrent_StaleSchemaFailsFast(t *testing.T) {
	dsn := dsnFromEnvOrContainer(t)
	migrateToHead(t, dsn)
	rollBackOne(t, dsn)

	err := ensureSchemaCurrent(context.Background(), dsn)
	if err == nil {
		t.Fatal("ensureSchemaCurrent returned nil on a stale schema; serving must fail fast (ADR-0031)")
	}
	// Assert the EXISTING actionable message is surfaced verbatim, including the
	// remediation hint — operators key on it, and #32 must not reword it.
	const wantSubstr = "schema out of date"
	const wantHint = "run `glyphoxa migrate up`"
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("error %q does not contain %q (version-mismatch message changed)", err, wantSubstr)
	}
	if !strings.Contains(err.Error(), wantHint) {
		t.Errorf("error %q does not contain the remediation hint %q", err, wantHint)
	}
}

// TestEnsureSchemaCurrent_CurrentSchemaPasses is the green half: with the schema
// fully migrated, the same check the serving boot path runs returns nil so boot
// proceeds to load the NPC. This pins that the fail-fast check does not block a
// correctly-migrated serving process.
func TestEnsureSchemaCurrent_CurrentSchemaPasses(t *testing.T) {
	dsn := dsnFromEnvOrContainer(t)
	migrateToHead(t, dsn)

	if err := ensureSchemaCurrent(context.Background(), dsn); err != nil {
		t.Fatalf("ensureSchemaCurrent on a current schema = %v, want nil (boot must proceed)", err)
	}
}

// TestRunFromDB_StaleSchemaFailsFastBeforeNPCLoad asserts the PRODUCTION call
// site: RunFromDB itself must fail fast on a stale schema, returning the
// version-mismatch error WITHOUT proceeding to the NPC load (or Discord). The DB
// here is stale AND empty of any seeded NPC, so if RunFromDB reached
// loadSeededNPCs it would surface a "find tenant" error instead — asserting the
// schema-out-of-date message proves the check runs first. No Discord token is
// needed because the schema check precedes any gateway connection.
func TestRunFromDB_StaleSchemaFailsFastBeforeNPCLoad(t *testing.T) {
	dsn := dsnFromEnvOrContainer(t)
	migrateToHead(t, dsn)
	rollBackOne(t, dsn)

	err := RunFromDB(context.Background(), Config{}, openPool(t, dsn))
	if err == nil {
		t.Fatal("RunFromDB returned nil on a stale schema; serving must fail fast before any other DB query")
	}
	if !strings.Contains(err.Error(), "schema out of date") {
		t.Errorf("RunFromDB error %q is not the schema-out-of-date fail-fast error; "+
			"the check must run before loadSeededNPCs (which would error on the missing tenant)", err)
	}
}
