//go:build integration

// The shared Postgres harness for internal/rpc's integration suites: the
// pgvector testcontainer + the seeded store the suites drive real handlers
// against, tag-isolated behind `integration` so the default `go test ./...`
// stays Docker-free (ADR-0021 / ADR-0033). The harness is duplicated here in
// miniature because internal/storage's harness_test.go is package-private to
// storage_test. The plain GetActiveCampaign round-trip that used to live here
// is unit coverage now (#445) — the handlers' mapping is proven keyless over
// per-feature fakes, and only SQL-shaped behavior stays DB-bound.

package rpc_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// pgImage carries the pgvector extension; stock postgres can't run
// `CREATE EXTENSION vector` (ADR-0011).
const pgImage = "pgvector/pgvector:pg17"

// startPostgres spins up a pgvector container and returns its connection
// string, skipping LOUDLY when Docker is unavailable so a green `go test` that
// never touched a real DB can't be mistaken for real coverage. Set
// GLYPHOXA_TEST_DSN to point at an external pgvector Postgres and skip the
// container entirely.
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
			"(is Docker running?). This test exercises the CampaignService handler "+
			"against a real DB and was NOT run. Set GLYPHOXA_TEST_DSN to an external "+
			"pgvector Postgres to run it without Docker. underlying error: %v", pgImage, err)
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

// seedStore migrates the schema and inserts a single campaign, returning a
// Store over the DB plus the seeded campaign's id.
func seedStore(t *testing.T, dsn string) (st *storage.Store, campaignID uuid.UUID) {
	st, _, campaignID = seedStoreTenant(t, dsn)
	return st, campaignID
}

// seedStoreTenant is seedStore that also returns the seeded tenant id — needed now
// the RPC tier is tenant-scoped (#473): a test must inject the SEEDED tenant (not a
// random one) so the scoped resolver resolves the seeded campaign.
func seedStoreTenant(t *testing.T, dsn string) (st *storage.Store, tenantID, campaignID uuid.UUID) {
	t.Helper()
	ctx := context.Background()

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

	if err := pool.QueryRow(ctx,
		`INSERT INTO tenant (name) VALUES ('Acme TTRPG') RETURNING id`).
		Scan(&tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name, system, language)
		 VALUES ($1, 'Lost Mine', 'dnd5e', 'en') RETURNING id`, tenantID).
		Scan(&campaignID); err != nil {
		t.Fatalf("insert campaign: %v", err)
	}
	return storage.New(pool), tenantID, campaignID
}

// tenantOperatorInterceptor injects the resolved tenant + an operator into the RPC
// context, the way the auth interceptor stack does (ADR-0039). Integration tests
// that mount a bare CampaignServer use it so the tenant-scoped handlers (#473)
// resolve the seeded tenant's campaign.
func tenantOperatorInterceptor(tenantID uuid.UUID, discordUserID string) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			ctx = auth.WithTenant(ctx, tenantID)
			if discordUserID != "" {
				ctx = auth.WithUser(ctx, storage.User{ID: uuid.New(), DiscordUserID: discordUserID})
			}
			return next(ctx, req)
		}
	})
}
