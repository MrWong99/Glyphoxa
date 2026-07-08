//go:build integration

// Proves the `/glyphoxa use` surface excludes archived campaigns against a real
// Postgres (#269, decided on #265). The exclusion is transitive — UseCommand's
// autocomplete + free-text match both read store.ListCampaigns, which filters
// archived rows — so this pins it at the presence layer: a future change that
// swapped the source for an archive-inclusive read would re-expose archived
// campaigns and this test would catch it. Tag-isolated behind `integration`.

package presence

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/disgoorg/disgo/discord"
)

// startArchivePostgres spins up a pgvector Postgres, migrates up, and returns a
// Store. It skips (not fails) when Docker is unavailable, mirroring the storage
// harness so a Docker-free `go test ./...` never hard-fails.
func startArchivePostgres(t *testing.T) *storage.Store {
	t.Helper()
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "pgvector/pgvector:pg17",
		tcpostgres.WithDatabase("glyphoxa_test"),
		tcpostgres.WithUsername("glyphoxa"),
		tcpostgres.WithPassword("glyphoxa"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Skipf("SKIPPED DB TEST — NO POSTGRES: could not start the container (is Docker running?): %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
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
	return storage.New(pool)
}

func TestUseCommandExcludesArchivedCampaign(t *testing.T) {
	st := startArchivePostgres(t)
	ctx := context.Background()

	tenantID, err := st.CreateTenant(ctx, "Acme TTRPG")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	activeID, err := st.CreateCampaign(ctx, storage.NewCampaign{TenantID: tenantID, Name: "Active Quest", System: "dnd5e", Language: "en"})
	if err != nil {
		t.Fatalf("CreateCampaign(active): %v", err)
	}
	archivedID, err := st.CreateCampaign(ctx, storage.NewCampaign{TenantID: tenantID, Name: "Zombie Vault", System: "dnd5e", Language: "en"})
	if err != nil {
		t.Fatalf("CreateCampaign(archived): %v", err)
	}
	if _, err := st.ArchiveCampaign(ctx, archivedID); err != nil {
		t.Fatalf("ArchiveCampaign: %v", err)
	}

	cmd := UseCommand(st)

	// --- Autocomplete must NOT offer the archived campaign (empty typed = all).
	ac := &Autocomplete{
		guildID: testGuild,
		userID:  operatorID,
		data: discord.AutocompleteInteractionData{
			CommandName: "glyphoxa use",
			Options: map[string]discord.AutocompleteOption{
				"campaign": {Name: "campaign", Type: discord.ApplicationCommandOptionTypeString, Focused: true},
			},
		},
	}
	choices, err := cmd.Autocomplete(ctx, ac)
	if err != nil {
		t.Fatalf("autocomplete: %v", err)
	}
	names := make(map[string]bool, len(choices))
	for _, ch := range choices {
		names[ch.ChoiceName()] = true
	}
	if !names["Active Quest"] {
		t.Errorf("autocomplete missing the active campaign: %v", names)
	}
	if names["Zombie Vault"] {
		t.Errorf("autocomplete leaked the ARCHIVED campaign %q: %v", "Zombie Vault", names)
	}

	// --- Free-text match can't resolve the archived campaign either (same
	// archive-excluding source): neither its name nor its id resolves.
	list, err := st.ListCampaigns(ctx)
	if err != nil {
		t.Fatalf("ListCampaigns: %v", err)
	}
	if _, ok := matchCampaign(list, "Zombie Vault"); ok {
		t.Errorf("free-text name resolved an archived campaign, want not-found")
	}
	if _, ok := matchCampaign(list, archivedID.String()); ok {
		t.Errorf("free-text id resolved an archived campaign, want not-found")
	}
	// Sanity: the active one still resolves by name.
	if c, ok := matchCampaign(list, "Active Quest"); !ok || c.ID != activeID {
		t.Errorf("active campaign no longer resolves: ok=%v", ok)
	}
}
