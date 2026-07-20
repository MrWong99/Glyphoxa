//go:build integration

// These tests exercise the issue-#69 credential bridge end-to-end against a real
// Postgres (testcontainers): a saved BYOK key in provider_config is read back,
// decrypted, and turned into the per-component session keys — and a key that
// cannot be decrypted fails RunFromDB clearly, before any Discord connection.
// Tag-isolated behind `integration` (ADR-0033) like the rest of this package's
// DB tests; the keyless resolver logic lives in credentials_test.go. The shared
// container harness (startDB, dsnFromEnvOrContainer, testCipher) lives in
// agentspec_test.go.
package wirenpc

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
)

// seedRealKeyNPC seeds the demo Tenant/Campaign with provider_config rows whose
// credentials are REAL sealed keys (last4 != "env" — the web-app BYOK path),
// bound to one Character NPC, so loadSeededNPCs/RunFromDB read them back. It uses
// the demo names loadSeededNPCs looks up. Returns the tenant ID, the campaign ID
// (the #323 campaign-scoped RunFromDB needs it in Config.CampaignID) and the three
// plaintext keys the bridge must reproduce.
func seedRealKeyNPC(t *testing.T, ctx context.Context, st *storage.Store, cipher *crypto.Cipher) (uuid.UUID, uuid.UUID, providerKeys) {
	t.Helper()
	want := providerKeys{
		llm: "sk-llm-REALsecret-0001",
		tts: "sk-tts-REALsecret-0002",
		stt: "sk-stt-REALsecret-0003",
	}

	tenantID, err := st.CreateTenant(ctx, SeedTenantName)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	campaignID, err := st.CreateCampaign(ctx, storage.NewCampaign{
		TenantID: tenantID,
		Name:     SeedCampaignName,
		System:   "dnd5e",
		Language: "en",
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	sealCfg := func(component storage.Component, provider, model, key string) uuid.UUID {
		ct, err := cipher.Seal([]byte(key))
		if err != nil {
			t.Fatalf("seal %s: %v", component, err)
		}
		id, err := st.CreateProviderConfig(ctx, storage.NewProviderConfig{
			TenantID:              tenantID,
			Component:             component,
			Provider:              provider,
			Model:                 model,
			CredentialsCiphertext: ct,
			CredentialsLast4:      crypto.Last4(key),
		})
		if err != nil {
			t.Fatalf("CreateProviderConfig %s: %v", component, err)
		}
		return id
	}

	llmCfgID := sealCfg(storage.ComponentLLM, llmProvider, llmModel, want.llm)
	ttsCfgID := sealCfg(storage.ComponentTTS, "elevenlabs", ttsModel, want.tts)
	sealCfg(storage.ComponentSTT, "elevenlabs", sttModel, want.stt)

	if _, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID:            campaignID,
		Role:                  storage.AgentRoleCharacter,
		Name:                  "Bart",
		Persona:               BartPersona,
		VoiceProviderConfigID: uuid.NullUUID{UUID: ttsCfgID, Valid: true},
		LLMProviderConfigID:   uuid.NullUUID{UUID: llmCfgID, Valid: true},
		AddressOnly:           false,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	return tenantID, campaignID, want
}

// TestRunFromDB_DecryptsSavedKeys is the AC1 proof against a real DB: a saved
// BYOK key (last4 != "env") is read back from provider_config and reaches the
// session DECRYPTED. We assert via the exact seam RunFromDB drives
// (loadSeededNPCs -> resolveSessionKeys) rather than running the Discord loop, so
// no credential getter is exposed on the public adapters.
func TestRunFromDB_DecryptsSavedKeys(t *testing.T) {
	pool := startDB(t)
	ctx := context.Background()
	st := storage.New(pool)
	cipher := testCipher(t)

	tenantID, _, want := seedRealKeyNPC(t, ctx, st, cipher)

	_, primary, gotCampaign, err := loadSeededNPCs(ctx, st)
	if err != nil {
		t.Fatalf("loadSeededNPCs: %v", err)
	}
	if gotCampaign.TenantID != tenantID {
		t.Fatalf("loadSeededNPCs tenant = %s, want %s", gotCampaign.TenantID, tenantID)
	}

	keys, _, err := resolveSessionKeys(ctx, st, tenantID, primary, cipher, nil)
	if err != nil {
		t.Fatalf("resolveSessionKeys: %v", err)
	}
	if keys.llm != want.llm {
		t.Errorf("llm key = %q, want decrypted %q", keys.llm, want.llm)
	}
	if keys.tts != want.tts {
		t.Errorf("tts key = %q, want decrypted %q", keys.tts, want.tts)
	}
	if keys.stt != want.stt {
		t.Errorf("stt key = %q, want decrypted %q (resolved per-Component)", keys.stt, want.stt)
	}
}

// TestRunFromDB_EnvPlaceholderFallsBack is the AC1/AC3 env-path proof: the demo
// seed writes "env" placeholders (real keys live in the OS keyring), and the
// bridge must resolve them to "" so every adapter reads its own env var — the
// pre-#69 behavior, unchanged. No cipher is even needed.
func TestRunFromDB_EnvPlaceholderFallsBack(t *testing.T) {
	pool := startDB(t)
	ctx := context.Background()
	st := storage.New(pool)

	if err := SeedNPC(ctx, pool, testCipher(t), nil); err != nil {
		t.Fatalf("SeedNPC: %v", err)
	}

	_, primary, campaign, err := loadSeededNPCs(ctx, st)
	if err != nil {
		t.Fatalf("loadSeededNPCs: %v", err)
	}

	// A nil cipher is deliberate: the env-placeholder path must not need one.
	keys, _, err := resolveSessionKeys(ctx, st, campaign.TenantID, primary, nil, nil)
	if err != nil {
		t.Fatalf("resolveSessionKeys on env placeholders: %v (must fall back to ENV, not error)", err)
	}
	if keys != (providerKeys{}) {
		t.Errorf("env-placeholder keys = %+v, want all empty (adapter ENV fallback)", keys)
	}
}

// TestResolveSessionKeys_WebCreatedAgentUsesTenantKeys reproduces the live
// #513-era failure: a WEB-created Character Agent carries NO
// llm_provider_config_id / voice_provider_config_id (campaign_crud never binds
// them — only the demo seed does), while the Configuration screen saved the
// tenant's real BYOK keys as per-Component provider_config rows. The session
// bridge must fall back to those tenant-Component rows — exactly like assist's
// and recap's resolveLLMConfig — instead of resolving a nil config to "" and
// tripping the ADR-0054/0055 platform-key entitlement gate ("tenant has no
// platform-key entitlement (BYOK plan)") on a tenant whose key is sitting right
// there. The refusingEntitlement stub plays the plan-less tenant's gate: with
// the fallback in place it is never consulted, because a resolved real key
// short-circuits the gate.
func TestResolveSessionKeys_WebCreatedAgentUsesTenantKeys(t *testing.T) {
	pool := startDB(t)
	ctx := context.Background()
	st := storage.New(pool)
	cipher := testCipher(t)

	want := providerKeys{
		llm: "sk-llm-TENANTsecret-0001",
		tts: "sk-tts-TENANTsecret-0002",
		stt: "sk-stt-TENANTsecret-0003",
	}

	tenantID, err := st.CreateTenant(ctx, "Web Tenant")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	campaignID, err := st.CreateCampaign(ctx, storage.NewCampaign{
		TenantID: tenantID,
		Name:     "Schachteln",
		System:   "dnd5e",
		Language: "de",
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}

	sealCfg := func(component storage.Component, provider, model, key string) {
		ct, err := cipher.Seal([]byte(key))
		if err != nil {
			t.Fatalf("seal %s: %v", component, err)
		}
		if _, err := st.CreateProviderConfig(ctx, storage.NewProviderConfig{
			TenantID:              tenantID,
			Component:             component,
			Provider:              provider,
			Model:                 model,
			CredentialsCiphertext: ct,
			CredentialsLast4:      crypto.Last4(key),
		}); err != nil {
			t.Fatalf("CreateProviderConfig %s: %v", component, err)
		}
	}
	sealCfg(storage.ComponentLLM, llmProvider, llmModel, want.llm)
	sealCfg(storage.ComponentTTS, "elevenlabs", ttsModel, want.tts)
	sealCfg(storage.ComponentSTT, "elevenlabs", sttModel, want.stt)

	// The web path: CreateAgent with NO provider-config bindings (both NullUUIDs
	// stay invalid), exactly like rpc/campaign_crud's CreateAgent.
	if _, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID:  campaignID,
		Role:        storage.AgentRoleCharacter,
		Name:        "Jule Brandt",
		Persona:     "Leitende Systemadministratorin.",
		AddressOnly: false,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	_, primary, campaign, err := loadCampaignNPCs(ctx, st, campaignID)
	if err != nil {
		t.Fatalf("loadCampaignNPCs: %v", err)
	}

	keys, llmCfg, err := resolveSessionKeys(ctx, st, campaign.TenantID, primary, cipher, refusingEntitlement{})
	if err != nil {
		t.Fatalf("resolveSessionKeys with unbound web Agent = %v; must fall back to the tenant's saved per-Component keys, not the entitlement gate", err)
	}
	if keys != want {
		t.Errorf("resolveSessionKeys keys = %+v, want the tenant's decrypted keys %+v", keys, want)
	}
	if llmCfg == nil || llmCfg.Provider != llmProvider {
		t.Errorf("resolveSessionKeys effective LLM config = %+v, want the tenant's %q row (drives llmProviderID / adapter selection)", llmCfg, llmProvider)
	}
}

// TestRunFromDB_DecryptFailureSurfacesClearError is the AC2 proof at the PRODUCTION
// call site: a real saved key the configured cipher cannot open makes RunFromDB
// return a CLEAR error BEFORE any Discord connection — never a silent empty key
// that degrades the NPC to the wrong (env) credential. The schema is current and
// an NPC is seeded, so RunFromDB gets past the fail-fast + load and fails AT key
// resolution; a wrong cipher (different key) cannot decrypt the sealed blob.
func TestRunFromDB_DecryptFailureSurfacesClearError(t *testing.T) {
	pool := startDB(t)
	ctx := context.Background()
	st := storage.New(pool)

	_, campaignID, _ := seedRealKeyNPC(t, ctx, st, testCipher(t)) // sealed with one cipher...
	wrong := testCipher(t)                                        // ...opened with another -> fails

	// CampaignID is set so RunFromDB gets past the #323 campaign-scoped load and
	// fails AT key resolution (the AC2 seam), not at the loader.
	err := RunFromDB(ctx, Config{CampaignID: campaignID}, pool, wrong)
	if err == nil {
		t.Fatal("RunFromDB returned nil with an undecryptable saved key; a broken key must fail clearly, not silently fall back to ENV (AC2)")
	}
	if !strings.Contains(err.Error(), "decrypt") {
		t.Errorf("RunFromDB error = %q, want a clear decrypt failure (AC2)", err)
	}
}
