//go:build integration

// This test drives the CampaignService handler against a real *storage.Store
// backed by a testcontainers Postgres, end to end over Connect-JSON. It is
// tag-isolated behind `integration` so the default `go test ./...` stays
// Docker-free (ADR-0021 / ADR-0033). The Postgres harness is duplicated here in
// miniature because internal/storage's harness_test.go is package-private to
// storage_test.

package rpc_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
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

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
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

	var tenantID uuid.UUID
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
	return storage.New(pool), campaignID
}

func TestGetActiveCampaign_Integration(t *testing.T) {
	dsn := startPostgres(t)
	store, campaignID := seedStore(t, dsn)

	mux := http.NewServeMux()
	mux.Handle(rpc.NewCampaignServer(store).Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := managementv1connect.NewCampaignServiceClient(
		http.DefaultClient, srv.URL, connect.WithProtoJSON(),
	)

	resp, err := client.GetActiveCampaign(
		context.Background(),
		connect.NewRequest(&managementv1.GetActiveCampaignRequest{}),
	)
	if err != nil {
		t.Fatalf("GetActiveCampaign: %v", err)
	}

	got := resp.Msg.GetCampaign()
	if got == nil {
		t.Fatal("response campaign is nil")
	}
	if got.GetId() != campaignID.String() {
		t.Errorf("id = %q, want %q", got.GetId(), campaignID.String())
	}
	if got.GetName() != "Lost Mine" {
		t.Errorf("name = %q, want %q", got.GetName(), "Lost Mine")
	}
	if got.GetSystem() != "dnd5e" {
		t.Errorf("system = %q, want %q", got.GetSystem(), "dnd5e")
	}
	if got.GetLanguage() != "en" {
		t.Errorf("language = %q, want %q", got.GetLanguage(), "en")
	}
}
