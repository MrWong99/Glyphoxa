package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/Glyphoxa/internal/bundle"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
)

// seedBundleTenantName is the Tenant the `-bundle` seed mints when the DB holds
// none yet (ADR-0053): a self-host operator's first `seed -bundle` lands the
// campaign under a fresh "Glyphoxa" Tenant; a subsequent seed reuses whatever
// Tenant already exists ([storage.Store.FirstTenant]) rather than duplicating it.
const seedBundleTenantName = "Glyphoxa"

// RunSeed is the `glyphoxa seed` subcommand. With no arguments it seeds the demo
// Tenant/Campaign and the live Character NPC (task #5), idempotently — the legacy
// path, byte-for-byte unchanged. With `-bundle <file>` it instead imports a
// campaign bundle (ADR-0053, #293): a canonical demo bundle ships one playable
// campaign (roster + KG + a player character) that an operator logs into and then
// configures BYOK keys against. The schema must already be applied
// (`glyphoxa migrate up`).
//
// Connection string: $GLYPHOXA_DATABASE_URL (or $DATABASE_URL).
// Credential-encryption secret: $GLYPHOXA_SECRET (ADR-0004 single app secret) is
// required only by the legacy path — it seals the placeholder credentials that
// path writes. The bundle path carries NO secrets (they are excluded from a
// bundle, ADR-0053 §2), so it never touches the cipher.
func RunSeed(ctx context.Context, log *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	bundlePath := fs.String("bundle", "", "import a campaign bundle (.glyphoxa.json[.gz]) instead of the built-in demo NPC")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Distinguish flag-absent (legacy demo-NPC seed) from flag-present-but-empty
	// (`-bundle ""`, a mistake): the empty-string default alone can't tell them
	// apart, so consult fs.Visit for whether -bundle was actually given.
	bundleSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "bundle" {
			bundleSet = true
		}
	})
	if bundleSet {
		if *bundlePath == "" {
			return fmt.Errorf("seed: -bundle requires a bundle file path (got empty)")
		}
		return runSeedBundle(ctx, log, *bundlePath)
	}
	return runSeedLegacy(ctx, log)
}

// runSeedLegacy is the original demo-NPC seed (task #5): it requires the app
// cipher and delegates to [wirenpc.SeedNPC], which is idempotent on the demo
// Tenant name.
func runSeedLegacy(ctx context.Context, log *slog.Logger) error {
	dsn := databaseURL()
	if dsn == "" {
		return fmt.Errorf("seed: set $GLYPHOXA_DATABASE_URL (or $DATABASE_URL)")
	}

	cipher, err := appCipher()
	if err != nil {
		return err
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("seed: open db: %w", err)
	}
	defer pool.Close()

	if err := wirenpc.SeedNPC(ctx, pool, cipher, log); err != nil {
		return err
	}
	return nil
}

// runSeedBundle imports a campaign bundle from path (ADR-0053, #293). It decodes
// the file BEFORE opening the DB so a mistyped path fails fast without a
// connection. The importer is always-mint (ADR-0053 §4): idempotence lives HERE,
// in a campaign-name precheck — if the resolved Tenant already holds a campaign
// of the bundle's name the seed logs and skips (exit 0), so re-running `seed
// -bundle` on a provisioned DB is a no-op rather than a duplicate campaign.
func runSeedBundle(ctx context.Context, log *slog.Logger, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("seed: open bundle %s: %w", path, err)
	}
	defer f.Close()
	b, err := bundle.Decode(f)
	if err != nil {
		return fmt.Errorf("seed: decode bundle %s: %w", path, err)
	}

	dsn := databaseURL()
	if dsn == "" {
		return fmt.Errorf("seed: set $GLYPHOXA_DATABASE_URL (or $DATABASE_URL)")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("seed: open db: %w", err)
	}
	defer pool.Close()
	st := storage.New(pool)

	tenantID, err := resolveSeedTenant(ctx, st)
	if err != nil {
		return err
	}

	// Idempotence gate (ADR-0053 §4): the importer never dedups, so a name
	// precheck is the only skip. Existing campaign of this name -> log + exit 0.
	if _, err := st.FindCampaignByName(ctx, tenantID, b.Campaign.Name); err == nil {
		log.Info("seed: campaign already present, skipping", "campaign", b.Campaign.Name)
		return nil
	} else if !errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("seed: check campaign %q: %w", b.Campaign.Name, err)
	}

	res, err := bundle.Import(ctx, st, tenantID, b)
	if err != nil {
		return fmt.Errorf("seed: import bundle: %w", err)
	}
	log.Info("seed: imported campaign bundle",
		"campaign", res.Name, "agents", res.Agents, "nodes", res.Nodes,
		"edges", res.Edges, "characters", res.Characters)
	return nil
}

// resolveSeedTenant returns the Tenant the bundle imports under: the earliest
// existing Tenant if the DB has one, else a freshly-minted "Glyphoxa" Tenant.
func resolveSeedTenant(ctx context.Context, st *storage.Store) (uuid.UUID, error) {
	tenant, err := st.FirstTenant(ctx)
	if errors.Is(err, storage.ErrNotFound) {
		id, err := st.CreateTenant(ctx, seedBundleTenantName)
		if err != nil {
			return uuid.Nil, fmt.Errorf("seed: create tenant: %w", err)
		}
		return id, nil
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("seed: resolve tenant: %w", err)
	}
	return tenant.ID, nil
}

// databaseURL resolves the Postgres connection string from the environment.
func databaseURL() string {
	if dsn := os.Getenv("GLYPHOXA_DATABASE_URL"); dsn != "" {
		return dsn
	}
	return os.Getenv("DATABASE_URL")
}

// appCipher builds the AES-256-GCM cipher from the single app secret
// ($GLYPHOXA_SECRET, ADR-0004). The secret must be a base64-encoded 32-byte
// random key — NOT a passphrase: an unsalted, unstretched hash of a human
// passphrase would let anyone holding leaked ciphertext brute-force the
// credentials offline. Requiring full-entropy keys removes that class of
// attack instead of papering over it with a KDF cost factor.
//
// Generate one with: openssl rand -base64 32
func appCipher() (*crypto.Cipher, error) {
	secret := os.Getenv("GLYPHOXA_SECRET")
	if secret == "" {
		return nil, fmt.Errorf("seed: set $GLYPHOXA_SECRET (the app credential-encryption secret, ADR-0004); generate one with `openssl rand -base64 32`")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(secret))
	if err != nil {
		return nil, fmt.Errorf("seed: $GLYPHOXA_SECRET must be base64 (generate with `openssl rand -base64 32`): %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("seed: $GLYPHOXA_SECRET must decode to exactly 32 bytes (a full-entropy AES-256 key, e.g. `openssl rand -base64 32`), got %d", len(key))
	}
	return crypto.New(key)
}
