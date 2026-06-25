//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestUpsertProviderConfigsInsertThenReplace asserts the (tenant, component,
// provider) upsert: a first save inserts, a second save with the same key
// replaces the credential + last4 + model in place (no duplicate row), and the
// updated_at advances — the Configuration "Replace" flow at the storage layer.
func TestUpsertProviderConfigsInsertThenReplace(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	first, err := st.UpsertProviderConfigs(ctx, []storage.NewProviderConfig{{
		TenantID:              tenantID,
		Component:             storage.ComponentLLM,
		Provider:              "groq",
		Model:                 "llama-3.3-70b-versatile",
		CredentialsCiphertext: []byte{0x01, 0xAA},
		CredentialsLast4:      "aaaa",
	}})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if len(first) != 1 || first[0].CredentialsLast4 != "aaaa" {
		t.Fatalf("first upsert = %+v, want one row last4=aaaa", first)
	}

	second, err := st.UpsertProviderConfigs(ctx, []storage.NewProviderConfig{{
		TenantID:              tenantID,
		Component:             storage.ComponentLLM,
		Provider:              "groq",
		Model:                 "llama-3.1-8b-instant",
		CredentialsCiphertext: []byte{0x02, 0xBB},
		CredentialsLast4:      "bbbb",
	}})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second[0].ID != first[0].ID {
		t.Errorf("replace created a new row (id %s != %s); want in-place upsert", second[0].ID, first[0].ID)
	}
	if second[0].CredentialsLast4 != "bbbb" || second[0].Model != "llama-3.1-8b-instant" {
		t.Errorf("replace did not update last4/model: %+v", second[0])
	}
	if !second[0].UpdatedAt.After(first[0].UpdatedAt) {
		t.Errorf("updated_at did not advance on replace: %v !> %v", second[0].UpdatedAt, first[0].UpdatedAt)
	}

	// Exactly one row for (tenant, llm, groq) — the replace did not duplicate.
	all, err := st.ListProviderConfigs(ctx, tenantID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	groqRows := 0
	for _, c := range all {
		if c.Component == storage.ComponentLLM && c.Provider == "groq" {
			groqRows++
		}
	}
	if groqRows != 1 {
		t.Fatalf("groq rows = %d, want 1 (upsert must not duplicate)", groqRows)
	}
}

// TestUpsertProviderConfigsBatchAtomic asserts the ElevenLabs case: one save
// upserts the stt + tts rows together so a per-Component read resolves the same
// key for both (ADR-0004 shared key).
func TestUpsertProviderConfigsBatchAtomic(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	sealed := []byte{0x01, 0xDE, 0xAD}
	rows, err := st.UpsertProviderConfigs(ctx, []storage.NewProviderConfig{
		{TenantID: tenantID, Component: storage.ComponentTTS, Provider: "elevenlabs", CredentialsCiphertext: sealed, CredentialsLast4: "9zZ8"},
		{TenantID: tenantID, Component: storage.ComponentSTT, Provider: "elevenlabs", CredentialsCiphertext: sealed, CredentialsLast4: "9zZ8"},
	})
	if err != nil {
		t.Fatalf("batch upsert: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}

	for _, comp := range []storage.Component{storage.ComponentTTS, storage.ComponentSTT} {
		got, err := st.GetProviderConfigByComponent(ctx, tenantID, comp)
		if err != nil {
			t.Fatalf("get by component %s: %v", comp, err)
		}
		if got.Provider != "elevenlabs" || got.CredentialsLast4 != "9zZ8" {
			t.Errorf("component %s resolved to %+v, want elevenlabs/9zZ8", comp, got)
		}
	}
}

// TestGetProviderConfigByComponentNotFound asserts the empty case maps to
// ErrNotFound (the RPC layer renders that slot as key-needed).
func TestGetProviderConfigByComponentNotFound(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, _ := seedCampaign(t, dsn)
	st := storage.New(pool)
	if _, err := st.GetProviderConfigByComponent(context.Background(), tenantID, storage.ComponentEmbeddings); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

// TestDeploymentConfigTokenAndChannels asserts the deployment_config upserts are
// column-isolated: saving the Bot token leaves the IDs untouched and vice versa,
// so the screen's separate Save buttons never clobber each other.
func TestDeploymentConfigTokenAndChannels(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// No row yet → ErrNotFound.
	if _, err := st.GetDeploymentConfig(ctx, tenantID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("get empty: got %v, want ErrNotFound", err)
	}

	// Save the IDs first.
	if _, err := st.SaveDiscordChannels(ctx, tenantID, "472093001100", "472093774421"); err != nil {
		t.Fatalf("save channels: %v", err)
	}
	// Then the token — must not wipe the IDs.
	tokRow, err := st.SaveDiscordBotToken(ctx, tenantID, []byte{0x01, 0x99}, "tok9")
	if err != nil {
		t.Fatalf("save token: %v", err)
	}
	if tokRow.GuildID != "472093001100" || tokRow.VoiceChannelID != "472093774421" {
		t.Errorf("token save clobbered IDs: %+v", tokRow)
	}
	if tokRow.DiscordBotTokenLast4 != "tok9" {
		t.Errorf("token last4 = %q, want tok9", tokRow.DiscordBotTokenLast4)
	}

	// Re-saving the IDs must not wipe the token.
	idRow, err := st.SaveDiscordChannels(ctx, tenantID, "999", "888")
	if err != nil {
		t.Fatalf("re-save channels: %v", err)
	}
	if idRow.DiscordBotTokenLast4 != "tok9" {
		t.Errorf("channels save clobbered token: last4 = %q, want tok9", idRow.DiscordBotTokenLast4)
	}
	if idRow.GuildID != "999" || idRow.VoiceChannelID != "888" {
		t.Errorf("channels not updated: %+v", idRow)
	}
}

// TestDeploymentConfigCascade asserts the tenant FK cascade so a deleted tenant
// takes its deployment_config with it (no orphan secret rows).
func TestDeploymentConfigCascade(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	if _, err := st.SaveDiscordBotToken(ctx, tenantID, []byte{0x01}, "x"); err != nil {
		t.Fatalf("save token: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM tenant WHERE id = $1`, tenantID); err != nil {
		t.Fatalf("delete tenant: %v", err)
	}
	if _, err := st.GetDeploymentConfig(ctx, tenantID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("deployment_config survived tenant delete: %v", err)
	}
}
