//go:build integration

package storage_test

import (
	"context"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestMigration00058_ClearsRetiredLlamaModel pins the data repair for Groq's
// retirement of llama-3.3-70b-versatile: a Provider Config still pinning that id
// is cleared to ” (the adapter default), while any other saved model is left
// alone. Applies the schema up to just before 00058, seeds raw rows, then
// applies 00058 (the 00016 drift-repair test pattern).
func TestMigration00058_ClearsRetiredLlamaModel(t *testing.T) {
	dsn := startPostgres(t)
	db := openSQL(t, dsn)
	pool := openPool(t, dsn)
	ctx := context.Background()

	provider, err := storage.NewMigrationProvider(db)
	if err != nil {
		t.Fatalf("NewMigrationProvider: %v", err)
	}
	if _, err := provider.UpTo(ctx, 57); err != nil {
		t.Fatalf("migrate up to 00057: %v", err)
	}

	var tenantID string
	if err := pool.QueryRow(ctx, `INSERT INTO tenant (name) VALUES ('Retired Model Co') RETURNING id`).Scan(&tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	insert := func(component, prov, model string) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx,
			`INSERT INTO provider_config (tenant_id, component, provider, model, credentials_ciphertext)
			 VALUES ($1, $2, $3, $4, '\x00') RETURNING id`,
			tenantID, component, prov, model).Scan(&id); err != nil {
			t.Fatalf("insert provider_config %s/%s: %v", component, prov, err)
		}
		return id
	}
	retired := insert("llm", "groq", "llama-3.3-70b-versatile")
	kept := insert("llm", "anthropic", "claude-sonnet-4-5")

	if _, err := provider.UpTo(ctx, 58); err != nil {
		t.Fatalf("migrate up to 00058: %v", err)
	}

	model := func(id string) string {
		t.Helper()
		var m string
		if err := pool.QueryRow(ctx, `SELECT model FROM provider_config WHERE id = $1`, id).Scan(&m); err != nil {
			t.Fatalf("read model: %v", err)
		}
		return m
	}
	if got := model(retired); got != "" {
		t.Errorf("retired llama pin = %q after 00058, want '' (adapter default)", got)
	}
	if got := model(kept); got != "claude-sonnet-4-5" {
		t.Errorf("unrelated model = %q after 00058, want it untouched", got)
	}
}
