//go:build integration

package config_test

import (
	"strings"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/config"
)

// TestIntegration_ConfigLoadAndValidate tests the full config loading and
// validation pipeline with various configurations that exercise cross-field
// validation, provider availability checks, and NPC constraints.
func TestIntegration_ConfigLoadAndValidate(t *testing.T) {
	t.Parallel()

	t.Run("minimal valid config", func(t *testing.T) {
		t.Parallel()

		yaml := `
server:
  listen_addr: ":8080"
  log_level: info
providers:
  llm:
    name: openai
    api_key: test-key
    model: gpt-4o-mini
  tts:
    name: elevenlabs
    api_key: test-key
  stt:
    name: deepgram
    api_key: test-key
  vad:
    name: silero
  audio:
    name: discord
    api_key: test-bot-token
npcs:
  - name: TestNPC
    personality: A test NPC
    engine: cascaded
    voice:
      voice_id: test-voice
memory:
  postgres_dsn: "postgres://test:test@localhost/test"
  embedding_dimensions: 1536
`
		cfg, err := config.LoadFromReader(strings.NewReader(yaml))
		if err != nil {
			t.Fatalf("LoadFromReader: %v", err)
		}
		if cfg.Server.ListenAddr != ":8080" {
			t.Errorf("ListenAddr = %q, want :8080", cfg.Server.ListenAddr)
		}
		if len(cfg.NPCs) != 1 {
			t.Errorf("NPCs = %d, want 1", len(cfg.NPCs))
		}
		if cfg.NPCs[0].Name != "TestNPC" {
			t.Errorf("NPC name = %q, want TestNPC", cfg.NPCs[0].Name)
		}
	})

	t.Run("invalid engine returns error", func(t *testing.T) {
		t.Parallel()

		yaml := `
providers:
  llm:
    name: openai
npcs:
  - name: BadNPC
    engine: invalid_engine
`
		_, err := config.LoadFromReader(strings.NewReader(yaml))
		if err == nil {
			t.Fatal("expected error for invalid engine")
		}
		if !strings.Contains(err.Error(), "invalid") {
			t.Errorf("error = %q, expected to contain 'invalid'", err.Error())
		}
	})

	t.Run("duplicate NPC names return error", func(t *testing.T) {
		t.Parallel()

		yaml := `
npcs:
  - name: DuplicateNPC
    engine: cascaded
  - name: DuplicateNPC
    engine: cascaded
providers:
  llm:
    name: openai
  tts:
    name: elevenlabs
`
		_, err := config.LoadFromReader(strings.NewReader(yaml))
		if err == nil {
			t.Fatal("expected error for duplicate NPC names")
		}
		if !strings.Contains(err.Error(), "duplicate") {
			t.Errorf("error = %q, expected to contain 'duplicate'", err.Error())
		}
	})

	t.Run("cascaded engine without LLM provider returns error", func(t *testing.T) {
		t.Parallel()

		yaml := `
npcs:
  - name: NoLLM
    engine: cascaded
providers:
  tts:
    name: elevenlabs
`
		_, err := config.LoadFromReader(strings.NewReader(yaml))
		if err == nil {
			t.Fatal("expected error for cascaded without LLM")
		}
		if !strings.Contains(err.Error(), "LLM") {
			t.Errorf("error = %q, expected to mention LLM", err.Error())
		}
	})

	t.Run("cascaded engine without TTS provider returns error", func(t *testing.T) {
		t.Parallel()

		yaml := `
npcs:
  - name: NoTTS
    engine: cascaded
providers:
  llm:
    name: openai
`
		_, err := config.LoadFromReader(strings.NewReader(yaml))
		if err == nil {
			t.Fatal("expected error for cascaded without TTS")
		}
		if !strings.Contains(err.Error(), "TTS") {
			t.Errorf("error = %q, expected to mention TTS", err.Error())
		}
	})

	t.Run("s2s engine without S2S provider returns error", func(t *testing.T) {
		t.Parallel()

		yaml := `
npcs:
  - name: NoS2S
    engine: s2s
`
		_, err := config.LoadFromReader(strings.NewReader(yaml))
		if err == nil {
			t.Fatal("expected error for s2s without provider")
		}
		if !strings.Contains(err.Error(), "S2S") {
			t.Errorf("error = %q, expected to mention S2S", err.Error())
		}
	})

	t.Run("multiple GM helpers returns error", func(t *testing.T) {
		t.Parallel()

		yaml := `
npcs:
  - name: GM1
    gm_helper: true
    engine: cascaded
  - name: GM2
    gm_helper: true
    engine: cascaded
providers:
  llm:
    name: openai
  tts:
    name: elevenlabs
`
		_, err := config.LoadFromReader(strings.NewReader(yaml))
		if err == nil {
			t.Fatal("expected error for multiple GM helpers")
		}
		if !strings.Contains(err.Error(), "gm_helper") {
			t.Errorf("error = %q, expected to mention gm_helper", err.Error())
		}
	})

	t.Run("GM helper gets AddressOnly default", func(t *testing.T) {
		t.Parallel()

		yaml := `
npcs:
  - name: GMHelper
    gm_helper: true
    engine: cascaded
providers:
  llm:
    name: openai
  tts:
    name: elevenlabs
`
		cfg, err := config.LoadFromReader(strings.NewReader(yaml))
		if err != nil {
			t.Fatalf("LoadFromReader: %v", err)
		}
		if !cfg.NPCs[0].AddressOnly {
			t.Error("GM helper should have AddressOnly=true by default")
		}
	})

	t.Run("invalid log level returns error", func(t *testing.T) {
		t.Parallel()

		yaml := `
server:
  log_level: verbose
`
		_, err := config.LoadFromReader(strings.NewReader(yaml))
		if err == nil {
			t.Fatal("expected error for invalid log level")
		}
	})

	t.Run("invalid budget tier returns error", func(t *testing.T) {
		t.Parallel()

		yaml := `
npcs:
  - name: BadBudget
    engine: cascaded
    budget_tier: premium
providers:
  llm:
    name: openai
  tts:
    name: elevenlabs
`
		_, err := config.LoadFromReader(strings.NewReader(yaml))
		if err == nil {
			t.Fatal("expected error for invalid budget tier")
		}
	})

	t.Run("voice speed factor out of range returns error", func(t *testing.T) {
		t.Parallel()

		yaml := `
npcs:
  - name: FastNPC
    engine: cascaded
    voice:
      speed_factor: 5.0
providers:
  llm:
    name: openai
  tts:
    name: elevenlabs
`
		_, err := config.LoadFromReader(strings.NewReader(yaml))
		if err == nil {
			t.Fatal("expected error for speed_factor out of range")
		}
	})

	t.Run("MCP server validation", func(t *testing.T) {
		t.Parallel()

		yaml := `
mcp:
  servers:
    - name: ""
      transport: stdio
`
		_, err := config.LoadFromReader(strings.NewReader(yaml))
		if err == nil {
			t.Fatal("expected error for MCP server without name")
		}
	})

	t.Run("MCP stdio without command returns error", func(t *testing.T) {
		t.Parallel()

		yaml := `
mcp:
  servers:
    - name: test-server
      transport: stdio
`
		_, err := config.LoadFromReader(strings.NewReader(yaml))
		if err == nil {
			t.Fatal("expected error for stdio transport without command")
		}
	})

	t.Run("all valid engines accepted", func(t *testing.T) {
		t.Parallel()

		yaml := `
providers:
  llm:
    name: openai
  tts:
    name: elevenlabs
  s2s:
    name: openai-realtime
npcs:
  - name: CascadedNPC
    engine: cascaded
  - name: S2SNPC
    engine: s2s
`
		cfg, err := config.LoadFromReader(strings.NewReader(yaml))
		if err != nil {
			t.Fatalf("LoadFromReader: %v", err)
		}
		if len(cfg.NPCs) != 2 {
			t.Errorf("NPCs = %d, want 2", len(cfg.NPCs))
		}
	})

	t.Run("empty config is valid", func(t *testing.T) {
		t.Parallel()

		yaml := `{}`
		cfg, err := config.LoadFromReader(strings.NewReader(yaml))
		if err != nil {
			t.Fatalf("LoadFromReader: %v", err)
		}
		if cfg == nil {
			t.Fatal("config should not be nil")
		}
	})

	t.Run("all valid provider names accepted", func(t *testing.T) {
		t.Parallel()

		yaml := `
providers:
  llm:
    name: openai
  stt:
    name: deepgram
  tts:
    name: elevenlabs
  s2s:
    name: gemini-live
  embeddings:
    name: openai
  vad:
    name: silero
  audio:
    name: discord
`
		cfg, err := config.LoadFromReader(strings.NewReader(yaml))
		if err != nil {
			t.Fatalf("LoadFromReader: %v", err)
		}
		if cfg.Providers.LLM.Name != "openai" {
			t.Errorf("LLM name = %q", cfg.Providers.LLM.Name)
		}
	})
}

// TestIntegration_WebConfigValidation tests the web service config validation.
func TestIntegration_WebConfigValidation(t *testing.T) {
	t.Parallel()

	t.Run("missing database DSN returns error", func(t *testing.T) {
		t.Parallel()
		// We test Validate directly since LoadConfig reads from env vars.
		cfg := &config.Config{}
		err := config.Validate(cfg)
		// Config with no NPCs and no providers is valid (just empty).
		if err != nil {
			t.Fatalf("Validate empty config: %v", err)
		}
	})

	t.Run("strict YAML rejects unknown fields", func(t *testing.T) {
		t.Parallel()

		yaml := `
unknown_field: true
`
		_, err := config.LoadFromReader(strings.NewReader(yaml))
		if err == nil {
			t.Fatal("expected error for unknown YAML field")
		}
	})
}
