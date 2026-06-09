package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strings"

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
