//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/MrWong99/Glyphoxa/internal/bundle"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
)

// TestRunExportHeadless is TEST 6 (integration): `glyphoxa export` writes a
// decodable bundle file against a configured DB, with NO $GLYPHOXA_SECRET set —
// the exporter never decrypts, so a headless backup needs only the DSN.
func TestRunExportHeadless(t *testing.T) {
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

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	cipher, err := crypto.New(key)
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	if err := wirenpc.SeedNPC(ctx, pool, cipher, nil); err != nil {
		t.Fatalf("SeedNPC: %v", err)
	}
	st := storage.New(pool)
	tenant, err := st.FindTenantByName(ctx, wirenpc.SeedTenantName)
	if err != nil {
		t.Fatalf("FindTenantByName: %v", err)
	}
	campaign, err := st.FindCampaignByName(ctx, tenant.ID, wirenpc.SeedCampaignName)
	if err != nil {
		t.Fatalf("FindCampaignByName: %v", err)
	}

	// Headless invocation: DSN via env, and $GLYPHOXA_SECRET explicitly absent.
	t.Setenv("GLYPHOXA_DATABASE_URL", dsn)
	os.Unsetenv("GLYPHOXA_SECRET")
	out := filepath.Join(t.TempDir(), "pony.glyphoxa.json.gz")

	if err := RunExport(ctx, []string{"-campaign", campaign.ID.String(), "-o", out}); err != nil {
		t.Fatalf("RunExport: %v", err)
	}

	f, err := os.Open(out)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	defer f.Close()
	b, err := bundle.Decode(f)
	if err != nil {
		t.Fatalf("Decode written file: %v", err)
	}
	if b.Campaign.Name != wirenpc.SeedCampaignName {
		t.Errorf("bundle campaign = %q, want %q", b.Campaign.Name, wirenpc.SeedCampaignName)
	}
	var hasBart bool
	for _, a := range b.Campaign.Agents {
		if a.Name == "Bart" {
			hasBart = true
		}
	}
	if !hasBart {
		t.Errorf("exported bundle missing Bart")
	}
}
