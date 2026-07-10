//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"log/slog"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// migrateSeedDB stands up a freshly migrated DB, points $GLYPHOXA_DATABASE_URL at
// it, and returns a Store for assertions.
func migrateSeedDB(t *testing.T) *storage.Store {
	t.Helper()
	ctx := context.Background()
	dsn := startPostgres(t)

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := storage.MigrateUp(ctx, db); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	_ = db.Close()

	t.Setenv("GLYPHOXA_DATABASE_URL", dsn)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return storage.New(pool)
}

// unsetSecret removes $GLYPHOXA_SECRET for the duration of one test and restores
// the prior value on cleanup — the bundle seed path must work with no secret set,
// but the unset must not leak into other package tests (t.Setenv can only SET, not
// unset, so we save/restore by hand).
func unsetSecret(t *testing.T) {
	t.Helper()
	prev, had := os.LookupEnv("GLYPHOXA_SECRET")
	if err := os.Unsetenv("GLYPHOXA_SECRET"); err != nil {
		t.Fatalf("unset GLYPHOXA_SECRET: %v", err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("GLYPHOXA_SECRET", prev)
		} else {
			_ = os.Unsetenv("GLYPHOXA_SECRET")
		}
	})
}

// TestRunSeedBundle_FreshDB is TEST 3: `seed -bundle demo.glyphoxa.json` against a
// fresh DB mints the "Glyphoxa" Tenant and imports the demo campaign — two agents
// (Butler + Bart), the KG, and the player character — with NO $GLYPHOXA_SECRET set
// (the bundle path carries no secrets).
func TestRunSeedBundle_FreshDB(t *testing.T) {
	ctx := context.Background()
	st := migrateSeedDB(t)
	unsetSecret(t)

	if err := RunSeed(ctx, slog.Default(), []string{"-bundle", demoBundlePath}); err != nil {
		t.Fatalf("seed -bundle: %v", err)
	}

	tenant, err := st.FirstTenant(ctx)
	if err != nil {
		t.Fatalf("FirstTenant: %v", err)
	}
	if tenant.Name != "Glyphoxa" {
		t.Errorf("tenant name = %q, want Glyphoxa", tenant.Name)
	}

	campaign, err := st.FindCampaignByName(ctx, tenant.ID, "The Prancing Pony")
	if err != nil {
		t.Fatalf("FindCampaignByName: %v", err)
	}

	agents, err := st.ListAgents(ctx, campaign.ID)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Errorf("agent count = %d, want 2 (Butler + Bart)", len(agents))
	}
	var hasBart bool
	for _, a := range agents {
		if a.Name == "Bart" {
			hasBart = true
		}
	}
	if !hasBart {
		t.Errorf("imported roster missing Bart")
	}

	nodes, err := st.ListNodes(ctx, campaign.ID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) < 3 {
		t.Errorf("node count = %d, want >= 3", len(nodes))
	}
	edges, err := st.ListEdges(ctx, campaign.ID)
	if err != nil {
		t.Fatalf("ListEdges: %v", err)
	}
	if len(edges) < 2 {
		t.Errorf("edge count = %d, want >= 2", len(edges))
	}
	chars, err := st.ListCharacters(ctx, campaign.ID)
	if err != nil {
		t.Fatalf("ListCharacters: %v", err)
	}
	if len(chars) < 1 {
		t.Errorf("player character count = %d, want >= 1", len(chars))
	}
}

// TestRunSeedBundle_Idempotent is TEST 4: a second `seed -bundle` is a no-op — the
// campaign-name precheck skips (exit 0) and no duplicate campaign appears.
func TestRunSeedBundle_Idempotent(t *testing.T) {
	ctx := context.Background()
	st := migrateSeedDB(t)
	unsetSecret(t)

	for i := 0; i < 2; i++ {
		if err := RunSeed(ctx, slog.Default(), []string{"-bundle", demoBundlePath}); err != nil {
			t.Fatalf("seed -bundle run %d: %v", i, err)
		}
	}

	campaigns, err := st.ListCampaigns(ctx)
	if err != nil {
		t.Fatalf("ListCampaigns: %v", err)
	}
	if len(campaigns) != 1 {
		t.Errorf("campaign count after two seeds = %d, want 1", len(campaigns))
	}
}

// TestRunSeedBundle_CoexistsWithLegacySeed is TEST 5: the legacy demo-NPC seed
// runs first (its Tenant + "The Prancing Pony" campaign), then `seed -bundle`
// reuses that Tenant, finds the campaign name, and skips — no second campaign.
func TestRunSeedBundle_CoexistsWithLegacySeed(t *testing.T) {
	ctx := context.Background()
	st := migrateSeedDB(t)

	// Legacy path needs a valid app secret to seal its placeholder credentials.
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	t.Setenv("GLYPHOXA_SECRET", base64.StdEncoding.EncodeToString(key))

	if err := RunSeed(ctx, slog.Default(), nil); err != nil {
		t.Fatalf("legacy seed: %v", err)
	}
	if err := RunSeed(ctx, slog.Default(), []string{"-bundle", demoBundlePath}); err != nil {
		t.Fatalf("seed -bundle after legacy: %v", err)
	}

	campaigns, err := st.ListCampaigns(ctx)
	if err != nil {
		t.Fatalf("ListCampaigns: %v", err)
	}
	if len(campaigns) != 1 {
		t.Errorf("campaign count = %d, want 1 (bundle skipped the existing name)", len(campaigns))
	}
}
