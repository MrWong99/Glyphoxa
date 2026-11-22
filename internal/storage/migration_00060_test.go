//go:build integration

package storage_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestMigration00060_UnbindsAgentLLMConfigs pins the #702 data repair: the
// Agent → LLM Provider Config binding only the demo seed ever wrote is
// cleared, so an already-seeded campaign follows its Tenant's LLM in effect
// like every web-created one. The Provider Config row itself, the voice (TTS)
// binding and the Butler's chat-scoped slot are left alone. Applies the schema
// up to just before 00060, seeds rows, then applies 00060 (the 00058 pattern).
func TestMigration00060_UnbindsAgentLLMConfigs(t *testing.T) {
	dsn := startPostgres(t)
	db := openSQL(t, dsn)
	pool := openPool(t, dsn)
	ctx := context.Background()

	provider, err := storage.NewMigrationProvider(db)
	if err != nil {
		t.Fatalf("NewMigrationProvider: %v", err)
	}
	if _, err := provider.UpTo(ctx, 59); err != nil {
		t.Fatalf("migrate up to 00059: %v", err)
	}

	st := storage.New(pool)
	tenantID, err := st.CreateTenant(ctx, "Seeded Co")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	campaignID, err := st.CreateCampaign(ctx, storage.NewCampaign{
		TenantID: tenantID, Name: "Seeded Campaign", System: "dnd5e", Language: "en",
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	cfg := func(component storage.Component, provider string) uuid.UUID {
		t.Helper()
		id, err := st.CreateProviderConfig(ctx, storage.NewProviderConfig{
			TenantID: tenantID, Component: component, Provider: provider,
			CredentialsCiphertext: []byte{}, CredentialsLast4: "env",
		})
		if err != nil {
			t.Fatalf("CreateProviderConfig(%s): %v", component, err)
		}
		return id
	}
	llmID, ttsID := cfg(storage.ComponentLLM, "groq"), cfg(storage.ComponentTTS, "elevenlabs")
	bartID, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID: campaignID, Role: storage.AgentRoleCharacter, Name: "Bart",
		VoiceProviderConfigID: uuid.NullUUID{UUID: ttsID, Valid: true},
		LLMProviderConfigID:   uuid.NullUUID{UUID: llmID, Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agents SET chat_llm_provider_config_id = $1 WHERE campaign_id = $2 AND agent_role = 'butler'`, llmID, campaignID); err != nil {
		t.Fatalf("bind butler chat slot: %v", err)
	}

	if _, err := provider.UpTo(ctx, 60); err != nil {
		t.Fatalf("migrate up to 00060: %v", err)
	}

	var llm, voice uuid.NullUUID
	if err := pool.QueryRow(ctx, `SELECT llm_provider_config_id, voice_provider_config_id FROM agents WHERE id = $1`, bartID).Scan(&llm, &voice); err != nil {
		t.Fatalf("read agent: %v", err)
	}
	if llm.Valid {
		t.Errorf("llm_provider_config_id = %v after 00060, want NULL", llm.UUID)
	}
	if !voice.Valid || voice.UUID != ttsID {
		t.Errorf("voice_provider_config_id = %+v, want it untouched (%v)", voice, ttsID)
	}
	var chat uuid.NullUUID
	if err := pool.QueryRow(ctx, `SELECT chat_llm_provider_config_id FROM agents WHERE campaign_id = $1 AND agent_role = 'butler'`, campaignID).Scan(&chat); err != nil {
		t.Fatalf("read butler: %v", err)
	}
	if !chat.Valid || chat.UUID != llmID {
		t.Errorf("butler chat slot = %+v, want it untouched (%v)", chat, llmID)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM provider_config WHERE id = $1`, llmID).Scan(&rows); err != nil || rows != 1 {
		t.Errorf("the tenant's llm row must survive: count = %d, err = %v", rows, err)
	}
}
