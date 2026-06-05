package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
)

// RunSeed is the `glyphoxa seed` subcommand: it seeds the demo Tenant/Campaign
// and the live Character NPC into Postgres (task #5), idempotently. The schema
// must already be applied (`glyphoxa migrate up`).
//
// Connection string: $GLYPHOXA_DATABASE_URL (or $DATABASE_URL).
// Credential-encryption secret: $GLYPHOXA_SECRET (ADR-0004 single app secret) —
// used only to seal the placeholder credentials the seed writes (real provider
// keys live in the OS keyring, not the DB).
func RunSeed(ctx context.Context, log *slog.Logger, _ []string) error {
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

// databaseURL resolves the Postgres connection string from the environment.
func databaseURL() string {
	if dsn := os.Getenv("GLYPHOXA_DATABASE_URL"); dsn != "" {
		return dsn
	}
	return os.Getenv("DATABASE_URL")
}

// appCipher builds the AES-256-GCM cipher from the single app secret
// ($GLYPHOXA_SECRET, ADR-0004). The secret is hashed to a 32-byte key with
// SHA-256 so any-length secret yields a valid AES-256 key.
func appCipher() (*crypto.Cipher, error) {
	secret := os.Getenv("GLYPHOXA_SECRET")
	if secret == "" {
		return nil, fmt.Errorf("seed: set $GLYPHOXA_SECRET (the app credential-encryption secret, ADR-0004)")
	}
	key := sha256.Sum256([]byte(secret))
	return crypto.New(key[:])
}
