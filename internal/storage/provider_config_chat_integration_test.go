//go:build integration

package storage_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// chatRows returns the Tenant's chat_llm Provider Configs.
func chatRows(t *testing.T, st *storage.Store, tenantID uuid.UUID) []storage.ProviderConfig {
	t.Helper()
	all, err := st.ListProviderConfigs(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("ListProviderConfigs: %v", err)
	}
	var out []storage.ProviderConfig
	for _, pc := range all {
		if pc.Component == storage.ComponentChatLLM {
			out = append(out, pc)
		}
	}
	return out
}

// A keyless choice creates one row that stores no key (ADR-0062 amendment
// 2026-11-22): the key resolution reads KeylessLast4 as "use another key".
func TestSaveChatLLMChoice_KeylessCreatesAnEnvRow(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	got, err := st.SaveChatLLMChoice(ctx, storage.ChatLLMChoice{TenantID: tenantID, Provider: "anthropic"})
	if err != nil {
		t.Fatalf("SaveChatLLMChoice: %v", err)
	}
	if got.Component != storage.ComponentChatLLM || got.Provider != "anthropic" || got.Model != "" {
		t.Errorf("row = %+v, want chat_llm/anthropic with no model", got)
	}
	if got.CredentialsLast4 != storage.KeylessLast4 || len(got.CredentialsCiphertext) != 0 {
		t.Errorf("row stores a credential: last4=%q ciphertext=%x", got.CredentialsLast4, got.CredentialsCiphertext)
	}
	if rows := chatRows(t, st, tenantID); len(rows) != 1 {
		t.Errorf("chat rows = %d, want 1", len(rows))
	}
}

// Switching provider leaves exactly one chat_llm row, with and without a key,
// and never touches another Component or another Tenant.
func TestSaveChatLLMChoice_SwitchingProviderLeavesOneRow(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	otherTenant, err := st.CreateTenant(ctx, "Other table")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := st.SaveChatLLMChoice(ctx, storage.ChatLLMChoice{TenantID: otherTenant, Provider: "groq"}); err != nil {
		t.Fatalf("other tenant's choice: %v", err)
	}
	if _, err := st.UpsertProviderConfigs(ctx, []storage.NewProviderConfig{{
		TenantID: tenantID, Component: storage.ComponentLLM, Provider: "groq",
		CredentialsCiphertext: []byte{0x01}, CredentialsLast4: "voic",
	}}); err != nil {
		t.Fatalf("seed llm row: %v", err)
	}

	steps := []storage.ChatLLMChoice{
		{TenantID: tenantID, Provider: "groq", Ciphertext: []byte{0xAA}, Last4: "aaaa"},
		{TenantID: tenantID, Provider: "anthropic"},
		{TenantID: tenantID, Provider: "groq"},
		{TenantID: tenantID, Provider: "anthropic", Ciphertext: []byte{0xBB}, Last4: "bbbb"},
	}
	for i, step := range steps {
		if _, err := st.SaveChatLLMChoice(ctx, step); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		rows := chatRows(t, st, tenantID)
		if len(rows) != 1 || rows[0].Provider != step.Provider {
			t.Fatalf("step %d: chat rows = %+v, want exactly one on %s", i, rows, step.Provider)
		}
	}
	// The secret save replaced the other provider's row and stored its key.
	if rows := chatRows(t, st, tenantID); rows[0].CredentialsLast4 != "bbbb" {
		t.Errorf("last4 = %q, want bbbb", rows[0].CredentialsLast4)
	}

	voice, err := st.GetProviderConfigByComponent(ctx, tenantID, storage.ComponentLLM)
	if err != nil || voice.CredentialsLast4 != "voic" {
		t.Errorf("the llm row changed: %+v, err %v", voice, err)
	}
	if rows := chatRows(t, st, otherTenant); len(rows) != 1 || rows[0].Provider != "groq" {
		t.Errorf("another tenant's chat row changed: %+v", rows)
	}
}

// A model-only save on a keyed row keeps ciphertext and last4 byte-identical:
// the credential columns are not part of the update (#712 item 3).
func TestSaveChatLLMChoice_ModelOnlyKeepsTheKey(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	ciphertext := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	keyed, err := st.SaveChatLLMChoice(ctx, storage.ChatLLMChoice{
		TenantID: tenantID, Provider: "anthropic", Model: "claude-sonnet-5-5", Ciphertext: ciphertext, Last4: "1234",
	})
	if err != nil {
		t.Fatalf("keyed save: %v", err)
	}

	for _, keep := range []bool{false, true} {
		got, err := st.SaveChatLLMChoice(ctx, storage.ChatLLMChoice{
			TenantID: tenantID, Provider: "anthropic", Model: "claude-opus-5-5", KeepKey: keep,
		})
		if err != nil {
			t.Fatalf("model-only save (KeepKey=%v): %v", keep, err)
		}
		if got.ID != keyed.ID {
			t.Errorf("KeepKey=%v created a new row", keep)
		}
		if got.Model != "claude-opus-5-5" {
			t.Errorf("KeepKey=%v: model = %q, want claude-opus-5-5", keep, got.Model)
		}
		if !bytes.Equal(got.CredentialsCiphertext, ciphertext) || got.CredentialsLast4 != "1234" {
			t.Errorf("KeepKey=%v overwrote the key: last4=%q ciphertext=%x", keep, got.CredentialsLast4, got.CredentialsCiphertext)
		}
	}
}

// KeepKey on a keyless or missing row is ErrNotFound and writes NOTHING: the
// model stays, and the other provider's row the save would have removed is
// still there (one transaction).
func TestSaveChatLLMChoice_KeepKeyNeedsASavedKey(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// Missing row.
	_, err := st.SaveChatLLMChoice(ctx, storage.ChatLLMChoice{TenantID: tenantID, Provider: "anthropic", Model: "x", KeepKey: true})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("KeepKey on a missing row: err = %v, want ErrNotFound", err)
	}
	if rows := chatRows(t, st, tenantID); len(rows) != 0 {
		t.Fatalf("KeepKey on a missing row wrote %+v", rows)
	}

	// Keyless row on the same provider.
	if _, err := st.SaveChatLLMChoice(ctx, storage.ChatLLMChoice{TenantID: tenantID, Provider: "anthropic", Model: "claude-sonnet-5-5"}); err != nil {
		t.Fatalf("keyless save: %v", err)
	}
	_, err = st.SaveChatLLMChoice(ctx, storage.ChatLLMChoice{TenantID: tenantID, Provider: "anthropic", Model: "free-text", KeepKey: true})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("KeepKey on a keyless row: err = %v, want ErrNotFound", err)
	}
	if rows := chatRows(t, st, tenantID); len(rows) != 1 || rows[0].Model != "claude-sonnet-5-5" {
		t.Fatalf("KeepKey on a keyless row changed it: %+v", rows)
	}

	// A refused KeepKey save on ANOTHER provider must not have removed this row.
	_, err = st.SaveChatLLMChoice(ctx, storage.ChatLLMChoice{TenantID: tenantID, Provider: "groq", Model: "free-text", KeepKey: true})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("KeepKey on another provider: err = %v, want ErrNotFound", err)
	}
	if rows := chatRows(t, st, tenantID); len(rows) != 1 || rows[0].Provider != "anthropic" {
		t.Fatalf("a refused save removed the existing choice: %+v", rows)
	}
}

// Delete returns the Tenant to "same as voice": every chat row goes, the call
// is idempotent, other Components stay, and a Butler bound to a removed row is
// unbound by the foreign key.
func TestDeleteChatLLMConfig(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	if _, err := st.UpsertProviderConfigs(ctx, []storage.NewProviderConfig{
		{TenantID: tenantID, Component: storage.ComponentLLM, Provider: "groq", CredentialsCiphertext: []byte{0x01}, CredentialsLast4: "voic"},
		{TenantID: tenantID, Component: storage.ComponentTTS, Provider: "elevenlabs", CredentialsCiphertext: []byte{0x02}, CredentialsLast4: "ttsk"},
	}); err != nil {
		t.Fatalf("seed rows: %v", err)
	}
	chat, err := st.SaveChatLLMChoice(ctx, storage.ChatLLMChoice{TenantID: tenantID, Provider: "anthropic", Ciphertext: []byte{0xCC}, Last4: "chat"})
	if err != nil {
		t.Fatalf("SaveChatLLMChoice: %v", err)
	}
	// A second chat row, as a race between two saves could leave behind.
	if _, err := pool.Exec(ctx,
		`INSERT INTO provider_config (tenant_id, component, provider, credentials_ciphertext, credentials_last4)
		 VALUES ($1, 'chat_llm', 'groq', ''::bytea, 'env')`, tenantID); err != nil {
		t.Fatalf("insert second chat row: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE agents SET chat_llm_provider_config_id = $1 WHERE campaign_id = $2 AND agent_role = 'butler'`,
		chat.ID, campaignID); err != nil {
		t.Fatalf("bind butler: %v", err)
	}

	for range 2 { // the second call deletes nothing and is still not an error
		if err := st.DeleteChatLLMConfig(ctx, tenantID); err != nil {
			t.Fatalf("DeleteChatLLMConfig: %v", err)
		}
	}
	if rows := chatRows(t, st, tenantID); len(rows) != 0 {
		t.Errorf("chat rows left: %+v", rows)
	}
	all, err := st.ListProviderConfigs(ctx, tenantID)
	if err != nil {
		t.Fatalf("ListProviderConfigs: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("rows after delete = %+v, want the llm and tts rows", all)
	}
	butler, err := st.GetButler(ctx, campaignID)
	if err != nil {
		t.Fatalf("GetButler: %v", err)
	}
	if butler.ChatLLMProviderConfigID.Valid {
		t.Errorf("butler still bound to a deleted chat row: %v", butler.ChatLLMProviderConfigID)
	}
}
