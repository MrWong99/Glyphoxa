//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/MrWong99/Glyphoxa/internal/blob"
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

// mapImageBytes is the picture the round-trip carries. Its exact content does not
// matter — only that the same bytes come out the far end.
var mapImageBytes = []byte("\x89PNG\r\n\x1a\n not really a png, but bytes are bytes")

// seedMapCampaign creates a Campaign with one Map whose image is in the blob seam,
// exactly as the CreateMap RPC leaves it: bytes first, then the row that names
// them. It returns the tenant, the campaign, and the blob store over the same pool.
func seedMapCampaign(ctx context.Context, t *testing.T, st *storage.Store, name string) (uuid.UUID, uuid.UUID, *blob.Postgres) {
	t.Helper()
	pool, err := pgxpool.New(ctx, os.Getenv("GLYPHOXA_DATABASE_URL"))
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	blobs := blob.NewPostgres(pool)

	tenantID, err := st.CreateTenant(ctx, "Round Trip")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	campaignID, err := st.CreateCampaign(ctx, storage.NewCampaign{
		TenantID: tenantID, Name: name, System: "dnd5e", Language: "en",
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	key, err := blob.Key(tenantID, "map", uuid.New(), "image")
	if err != nil {
		t.Fatalf("blob.Key: %v", err)
	}
	if err := blobs.Put(ctx, key, "image/png", bytes.NewReader(mapImageBytes), int64(len(mapImageBytes))); err != nil {
		t.Fatalf("blob Put: %v", err)
	}
	if _, err := st.CreateMap(ctx, storage.NewCampaignMap{
		CampaignID: campaignID, Name: "Saltmarsh", BlobKey: key, WidthPx: 800, HeightPx: 600,
	}); err != nil {
		t.Fatalf("CreateMap: %v", err)
	}
	return tenantID, campaignID, blobs
}

// TestRunSeedBundle_ImportsAnImagesIncludedBundle is the regression for the gap
// #547 shipped with: `export -include-images` produced a bundle that ONLY the web
// UI could import. The CLI built its bundle.PGStore with a nil Blobs field, so the
// first map image hit "bundle: no blob store configured" and the whole seed rolled
// back — the flag's own output was unusable through the CLI, which is the natural
// place to restore a backup.
//
// It is the reported reproduction end to end: real `export -include-images`, real
// `seed -bundle`, real bytes compared on the far side. The source campaign is
// RENAMED between the two so the seed's name precheck does not treat the restore
// as an already-provisioned DB and skip it.
func TestRunSeedBundle_ImportsAnImagesIncludedBundle(t *testing.T) {
	ctx := context.Background()
	st := migrateSeedDB(t)
	unsetSecret(t)

	const campaignName = "The Drowned Coast"
	tenantID, campaignID, blobs := seedMapCampaign(ctx, t, st, campaignName)

	bundlePath := filepath.Join(t.TempDir(), "backup.glyphoxa.json")
	if err := RunExport(ctx, []string{
		"-campaign", campaignID.String(), "-include-images", "-o", bundlePath,
	}); err != nil {
		t.Fatalf("export -include-images: %v", err)
	}

	if _, err := st.UpdateCampaign(ctx, storage.CampaignUpdate{
		TenantID: tenantID, ID: campaignID, Name: "The Drowned Coast (source)",
		System: "dnd5e", Language: "en",
	}); err != nil {
		t.Fatalf("rename source campaign: %v", err)
	}

	if err := RunSeed(ctx, slog.Default(), []string{"-bundle", bundlePath}); err != nil {
		t.Fatalf("seed -bundle of an images-included bundle: %v", err)
	}

	imported, err := st.FindCampaignByName(ctx, tenantID, campaignName)
	if err != nil {
		t.Fatalf("FindCampaignByName after seed: %v", err)
	}
	maps, err := st.ListMaps(ctx, imported.ID)
	if err != nil {
		t.Fatalf("ListMaps: %v", err)
	}
	if len(maps) != 1 {
		t.Fatalf("imported maps = %d, want 1", len(maps))
	}
	if maps[0].BlobKey == "" {
		t.Fatal("the imported map has no blob key, so its picture went nowhere")
	}

	// The bytes are actually THERE. A key alone would be the very failure the
	// imageless-import fix exists to prevent.
	rc, meta, err := blobs.Get(ctx, maps[0].BlobKey)
	if err != nil {
		t.Fatalf("the imported map's key names no blob: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read imported blob: %v", err)
	}
	if !bytes.Equal(got, mapImageBytes) {
		t.Errorf("imported image = %q, want %q", got, mapImageBytes)
	}
	if meta.ContentType != "image/png" {
		t.Errorf("imported content type = %q, want image/png", meta.ContentType)
	}
}

// TestRunSeedBundle_ImagelessBundleLeavesNoBlobKey is the other half of the same
// decision: an export WITHOUT -include-images restores the map — name, size,
// nesting, pins — and no picture, so its row must not claim one. A minted key here
// would name bytes that were never written: every image request 404s, the Maps tab
// draws a broken image, and nothing ever repairs it (a re-upload mints its own
// key). Empty is the honest state, and the one the image mount and the blob sweep
// both already handle.
func TestRunSeedBundle_ImagelessBundleLeavesNoBlobKey(t *testing.T) {
	ctx := context.Background()
	st := migrateSeedDB(t)
	unsetSecret(t)

	const campaignName = "The Drowned Coast"
	tenantID, campaignID, _ := seedMapCampaign(ctx, t, st, campaignName)

	bundlePath := filepath.Join(t.TempDir(), "setup.glyphoxa.json")
	if err := RunExport(ctx, []string{"-campaign", campaignID.String(), "-o", bundlePath}); err != nil {
		t.Fatalf("export: %v", err)
	}
	if _, err := st.UpdateCampaign(ctx, storage.CampaignUpdate{
		TenantID: tenantID, ID: campaignID, Name: "The Drowned Coast (source)",
		System: "dnd5e", Language: "en",
	}); err != nil {
		t.Fatalf("rename source campaign: %v", err)
	}
	if err := RunSeed(ctx, slog.Default(), []string{"-bundle", bundlePath}); err != nil {
		t.Fatalf("seed -bundle of an imageless bundle: %v", err)
	}

	imported, err := st.FindCampaignByName(ctx, tenantID, campaignName)
	if err != nil {
		t.Fatalf("FindCampaignByName after seed: %v", err)
	}
	maps, err := st.ListMaps(ctx, imported.ID)
	if err != nil {
		t.Fatalf("ListMaps: %v", err)
	}
	if len(maps) != 1 {
		t.Fatalf("imported maps = %d, want 1 (the map survives without its picture)", len(maps))
	}
	if maps[0].BlobKey != "" {
		t.Errorf("imported map claims blob %q, but no bytes were written for it", maps[0].BlobKey)
	}
	// And the dimensions still came across, so the surface renders at the right
	// aspect ratio and every pin lands where the GM left it.
	if maps[0].WidthPx != 800 || maps[0].HeightPx != 600 {
		t.Errorf("imported map dimensions = %dx%d, want 800x600", maps[0].WidthPx, maps[0].HeightPx)
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
