package config_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/config"
)

func TestValidate_DuplicateNPCNames(t *testing.T) {
	t.Parallel()
	yaml := `
providers:
  llm:
    name: openai
  tts:
    name: elevenlabs
npcs:
  - name: Greymantle
    engine: cascaded
  - name: Greymantle
    engine: cascaded
`
	_, err := config.LoadFromReader(strings.NewReader(yaml))
	if err == nil {
		t.Fatal("expected error for duplicate NPC names, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should mention duplicate, got: %v", err)
	}
}

func TestValidate_CascadedRequiresLLMAndTTS(t *testing.T) {
	t.Parallel()
	yaml := `
npcs:
  - name: TestNPC
    engine: cascaded
`
	_, err := config.LoadFromReader(strings.NewReader(yaml))
	if err == nil {
		t.Fatal("expected error for cascaded engine without LLM/TTS providers, got nil")
	}
	if !strings.Contains(err.Error(), "LLM provider") {
		t.Errorf("error should mention LLM provider, got: %v", err)
	}
	if !strings.Contains(err.Error(), "TTS provider") {
		t.Errorf("error should mention TTS provider, got: %v", err)
	}
}

func TestValidate_SentenceCascadeRequiresLLMAndTTS(t *testing.T) {
	t.Parallel()
	yaml := `
npcs:
  - name: TestNPC
    engine: sentence_cascade
`
	_, err := config.LoadFromReader(strings.NewReader(yaml))
	if err == nil {
		t.Fatal("expected error for sentence_cascade engine without LLM/TTS providers, got nil")
	}
	if !strings.Contains(err.Error(), "LLM provider") {
		t.Errorf("error should mention LLM provider, got: %v", err)
	}
}

func TestValidate_S2SRequiresS2SProvider(t *testing.T) {
	t.Parallel()
	yaml := `
npcs:
  - name: TestNPC
    engine: s2s
`
	_, err := config.LoadFromReader(strings.NewReader(yaml))
	if err == nil {
		t.Fatal("expected error for s2s engine without S2S provider, got nil")
	}
	if !strings.Contains(err.Error(), "S2S provider") {
		t.Errorf("error should mention S2S provider, got: %v", err)
	}
}

func TestValidate_CascadedWithProvidersIsValid(t *testing.T) {
	t.Parallel()
	yaml := `
providers:
  llm:
    name: openai
  tts:
    name: elevenlabs
memory:
  postgres_dsn: "postgres://localhost/test"
  embedding_dimensions: 1536
npcs:
  - name: TestNPC
    engine: cascaded
`
	_, err := config.LoadFromReader(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_S2SWithProviderIsValid(t *testing.T) {
	t.Parallel()
	yaml := `
providers:
  s2s:
    name: openai-realtime
memory:
  postgres_dsn: "postgres://localhost/test"
npcs:
  - name: TestNPC
    engine: s2s
`
	_, err := config.LoadFromReader(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_MultipleErrors(t *testing.T) {
	t.Parallel()
	yaml := `
npcs:
  - name: NPC1
    engine: cascaded
  - name: NPC1
    engine: s2s
`
	_, err := config.LoadFromReader(strings.NewReader(yaml))
	if err == nil {
		t.Fatal("expected errors, got nil")
	}
	// Should contain both duplicate and provider errors.
	errStr := err.Error()
	if !strings.Contains(errStr, "duplicate") {
		t.Errorf("error should mention duplicate, got: %v", err)
	}
}

func TestValidate_DuplicateGMHelper(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			LLM: config.ProviderEntry{Name: "openai"},
			TTS: config.ProviderEntry{Name: "elevenlabs"},
		},
		NPCs: []config.NPCConfig{
			{Name: "NPC1", Engine: config.EngineCascaded, GMHelper: true},
			{Name: "NPC2", Engine: config.EngineCascaded, GMHelper: true},
		},
	}

	err := config.Validate(cfg)
	if err == nil {
		t.Fatal("expected error for duplicate gm_helper")
	}
	if !strings.Contains(err.Error(), "gm_helper") {
		t.Errorf("expected error mentioning gm_helper, got: %s", err)
	}
}

func TestValidate_SingleGMHelper(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			LLM: config.ProviderEntry{Name: "openai"},
			TTS: config.ProviderEntry{Name: "elevenlabs"},
		},
		NPCs: []config.NPCConfig{
			{Name: "NPC1", Engine: config.EngineCascaded, GMHelper: true},
			{Name: "NPC2", Engine: config.EngineCascaded},
		},
	}

	if err := config.Validate(cfg); err != nil {
		t.Errorf("expected no error for single gm_helper, got: %s", err)
	}
}

func TestApplyDefaults_GMHelperSetsAddressOnly(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		NPCs: []config.NPCConfig{
			{Name: "Helper", GMHelper: true},
			{Name: "Regular"},
		},
	}
	config.ApplyDefaults(cfg)

	if !cfg.NPCs[0].AddressOnly {
		t.Error("expected GMHelper NPC to have AddressOnly=true after ApplyDefaults")
	}
	if cfg.NPCs[1].AddressOnly {
		t.Error("expected regular NPC to remain AddressOnly=false")
	}
}

func TestApplyDefaults_ExplicitAddressOnlyPreserved(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		NPCs: []config.NPCConfig{
			{Name: "Helper", GMHelper: true, AddressOnly: true},
		},
	}
	config.ApplyDefaults(cfg)

	if !cfg.NPCs[0].AddressOnly {
		t.Error("expected AddressOnly=true to remain true")
	}
}

func TestApplyDefaults_NonGMHelperAddressOnly(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		NPCs: []config.NPCConfig{
			{Name: "Background", AddressOnly: true},
		},
	}
	config.ApplyDefaults(cfg)

	if !cfg.NPCs[0].AddressOnly {
		t.Error("expected non-GM-helper with explicit AddressOnly=true to be preserved")
	}
}

func TestValidProviderNames(t *testing.T) {
	t.Parallel()
	// Sanity-check that the map is populated.
	if len(config.ValidProviderNames) == 0 {
		t.Fatal("ValidProviderNames should not be empty")
	}
	llmNames := config.ValidProviderNames["llm"]
	if len(llmNames) == 0 {
		t.Fatal("ValidProviderNames[\"llm\"] should not be empty")
	}
	// Check that "openai" is in the LLM list.
	found := slices.Contains(llmNames, "openai")
	if !found {
		t.Error("ValidProviderNames[\"llm\"] should contain \"openai\"")
	}
}
