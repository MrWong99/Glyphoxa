//go:build integration

package wirenpc

import (
	"context"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestSeededCampaign_TenantLLMChoiceMovesTheWholeSession is the #702 review
// bar: a `glyphoxa seed` tenant that switches its voice LLM in Configuration
// (a newer (llm, anthropic) row beside the seeded groq one) gets a session
// whose adapter AND every model — the seeded NPC, a web-created NPC, the
// Butler — are Anthropic's. Before, the seed bound Bart to the groq row: the
// adapter stayed Groq while the Butler and unbound NPCs were handed the
// Anthropic row's model, and the screen said "Anthropic".
func TestSeededCampaign_TenantLLMChoiceMovesTheWholeSession(t *testing.T) {
	pool := startDB(t)
	ctx := context.Background()
	st := storage.New(pool)

	if err := SeedNPC(ctx, pool, testCipher(t), nil); err != nil {
		t.Fatalf("SeedNPC: %v", err)
	}
	tenant, err := st.FindTenantByName(ctx, SeedTenantName)
	if err != nil {
		t.Fatalf("FindTenantByName: %v", err)
	}
	campaign, err := st.FindCampaignByName(ctx, tenant.ID, SeedCampaignName)
	if err != nil {
		t.Fatalf("FindCampaignByName: %v", err)
	}
	if _, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID: campaign.ID, Role: storage.AgentRoleCharacter, Name: "WebNPC", Persona: "A web-created NPC.",
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	// "Use for voice" on the Anthropic slot: a keyless row, newer than groq's.
	const model = "claude-haiku-4-5"
	if _, err := st.UpsertProviderConfigs(ctx, []storage.NewProviderConfig{{
		TenantID: tenant.ID, Component: storage.ComponentLLM, Provider: "anthropic", Model: model,
		CredentialsCiphertext: []byte{}, CredentialsLast4: llmbuild.EnvPlaceholderLast4,
	}}); err != nil {
		t.Fatalf("UpsertProviderConfigs: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE provider_config SET updated_at = now() + interval '1 minute' WHERE provider = 'anthropic'`); err != nil {
		t.Fatalf("age rows: %v", err)
	}

	specs, primary, _, err := loadSeededNPCs(ctx, st)
	if err != nil {
		t.Fatalf("loadSeededNPCs: %v", err)
	}
	_, llmCfg, err := resolveSessionKeys(ctx, st, tenant.ID, primary, testCipher(t), nil)
	if err != nil {
		t.Fatalf("resolveSessionKeys: %v", err)
	}
	cfg := Config{npcs: specs}.withSessionLLM(llmCfg)

	if cfg.llmProviderID != "anthropic" {
		t.Errorf("session provider = %q, want anthropic — the Tenant's LLM in effect", cfg.llmProviderID)
	}
	if len(cfg.npcs) != 3 {
		t.Fatalf("roster = %d specs, want 3 (Bart, WebNPC, Butler)", len(cfg.npcs))
	}
	for _, spec := range cfg.npcs {
		if spec.model != model {
			t.Errorf("%s runs model %q, want %q — a model of the session's provider", spec.name, spec.model, model)
		}
	}
}
