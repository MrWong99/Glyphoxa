package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/internal/app"
	"github.com/MrWong99/glyphoxa/internal/config"
	mcpmock "github.com/MrWong99/glyphoxa/internal/mcp/mock"
	audiomock "github.com/MrWong99/glyphoxa/pkg/audio/mock"
	"github.com/MrWong99/glyphoxa/pkg/memory"
	memorymock "github.com/MrWong99/glyphoxa/pkg/memory/mock"
	llmmock "github.com/MrWong99/glyphoxa/pkg/provider/llm/mock"
	ttsmock "github.com/MrWong99/glyphoxa/pkg/provider/tts/mock"
)

// testConfig returns a minimal config with one cascaded NPC for tests.
func testConfig() *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			ListenAddr: "test-channel",
			LogLevel:   config.LogInfo,
		},
		NPCs: []config.NPCConfig{
			{
				Name:        "Grimjaw",
				Personality: "A gruff dwarven bartender.",
				Engine:      config.EngineCascaded,
				BudgetTier:  config.BudgetTierFast,
				Voice: config.VoiceConfig{
					Provider: "test",
					VoiceID:  "dwarf-1",
				},
			},
		},
		Campaign: config.CampaignConfig{
			Name: "test-campaign",
		},
	}
}

// testProviders returns providers with mock LLM/TTS for a cascaded engine.
func testProviders() *app.Providers {
	return &app.Providers{
		LLM: &llmmock.Provider{},
		TTS: &ttsmock.Provider{},
	}
}

func TestNew_WithMocks(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	providers := testProviders()
	sessions := &memorymock.SessionStore{}
	graph := &memorymock.KnowledgeGraph{}
	mcpHost := &mcpmock.Host{}
	mixer := &audiomock.Mixer{}

	application, err := app.New(
		context.Background(),
		cfg,
		providers,
		app.WithSessionStore(sessions),
		app.WithKnowledgeGraph(graph),
		app.WithMCPHost(mcpHost),
		app.WithMixer(mixer),
	)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if application == nil {
		t.Fatal("New() returned nil app")
	}

	// MCP host should have been calibrated during New().
	if got := mcpHost.CallCount("Calibrate"); got != 1 {
		t.Errorf("Calibrate call count = %d, want 1", got)
	}
}

func TestNew_NoNPCs(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.NPCs = nil

	providers := testProviders()
	sessions := &memorymock.SessionStore{}
	graph := &memorymock.KnowledgeGraph{}
	mcpHost := &mcpmock.Host{}
	mixer := &audiomock.Mixer{}

	application, err := app.New(
		context.Background(),
		cfg,
		providers,
		app.WithSessionStore(sessions),
		app.WithKnowledgeGraph(graph),
		app.WithMCPHost(mcpHost),
		app.WithMixer(mixer),
	)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if application == nil {
		t.Fatal("New() returned nil app")
	}
}

func TestApp_Shutdown(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	providers := testProviders()
	sessions := &memorymock.SessionStore{}
	graph := &memorymock.KnowledgeGraph{}
	mcpHost := &mcpmock.Host{}
	mixer := &audiomock.Mixer{}

	application, err := app.New(
		context.Background(),
		cfg,
		providers,
		app.WithSessionStore(sessions),
		app.WithKnowledgeGraph(graph),
		app.WithMCPHost(mcpHost),
		app.WithMixer(mixer),
	)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := application.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error: %v", err)
	}

	// MCP host Close should have been called during shutdown.
	if got := mcpHost.CallCount("Close"); got != 1 {
		t.Errorf("MCP Host Close call count = %d, want 1", got)
	}
}

func TestNew_RegistersNPCRelationships(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	// Two NPCs with a bidirectional "knows" relationship.
	cfg.NPCs = []config.NPCConfig{
		{
			Name:        "Grimjaw",
			Personality: "A gruff dwarven bartender.",
			Engine:      config.EngineCascaded,
			BudgetTier:  config.BudgetTierFast,
			Voice:       config.VoiceConfig{Provider: "test", VoiceID: "dwarf-1"},
			Relationships: []config.RelationshipConfig{
				{TargetName: "Elara", Type: "knows", Bidirectional: true},
			},
		},
		{
			Name:        "Elara",
			Personality: "A mysterious elven scholar.",
			Engine:      config.EngineCascaded,
			BudgetTier:  config.BudgetTierFast,
			Voice:       config.VoiceConfig{Provider: "test", VoiceID: "elf-1"},
		},
	}

	providers := testProviders()
	sessions := &memorymock.SessionStore{}
	graph := &memorymock.KnowledgeGraph{}
	mcpHost := &mcpmock.Host{}
	mixer := &audiomock.Mixer{}

	application, err := app.New(
		context.Background(),
		cfg,
		providers,
		app.WithSessionStore(sessions),
		app.WithKnowledgeGraph(graph),
		app.WithMCPHost(mcpHost),
		app.WithMixer(mixer),
	)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if application == nil {
		t.Fatal("New() returned nil app")
	}

	// Should have 2 AddEntity calls (one per NPC).
	if got := graph.CallCount("AddEntity"); got != 2 {
		t.Errorf("AddEntity calls = %d, want 2", got)
	}

	// Should have 2 AddRelationship calls (forward + reverse for bidirectional).
	if got := graph.CallCount("AddRelationship"); got != 2 {
		t.Errorf("AddRelationship calls = %d, want 2 (bidirectional)", got)
	}

	// Verify the relationship details.
	var relCalls []memorymock.Call
	for _, c := range graph.Calls() {
		if c.Method == "AddRelationship" {
			relCalls = append(relCalls, c)
		}
	}
	if len(relCalls) != 2 {
		t.Fatalf("expected 2 AddRelationship calls, got %d", len(relCalls))
	}

	// Forward: Grimjaw → Elara.
	fwd := relCalls[0].Args[0].(memory.Relationship)
	if fwd.SourceID != "npc-0-Grimjaw" || fwd.TargetID != "npc-1-Elara" || fwd.RelType != "knows" {
		t.Errorf("forward relationship = %+v, want Grimjaw→Elara/knows", fwd)
	}
	// Reverse: Elara → Grimjaw.
	rev := relCalls[1].Args[0].(memory.Relationship)
	if rev.SourceID != "npc-1-Elara" || rev.TargetID != "npc-0-Grimjaw" || rev.RelType != "knows" {
		t.Errorf("reverse relationship = %+v, want Elara→Grimjaw/knows", rev)
	}
}

func TestApp_RunAndShutdown(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	providers := testProviders()

	sessions := &memorymock.SessionStore{}
	graph := &memorymock.KnowledgeGraph{}
	mcpHost := &mcpmock.Host{}
	mixer := &audiomock.Mixer{}

	application, err := app.New(
		context.Background(),
		cfg,
		providers,
		app.WithSessionStore(sessions),
		app.WithKnowledgeGraph(graph),
		app.WithMCPHost(mcpHost),
		app.WithMixer(mixer),
	)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Run in background.
	errCh := make(chan error, 1)
	go func() {
		errCh <- application.Run(ctx)
	}()

	// Give Run a moment to set up goroutines.
	time.Sleep(50 * time.Millisecond)

	// Cancel context to trigger shutdown.
	cancel()

	select {
	case err := <-errCh:
		if err != nil && err != context.Canceled {
			t.Fatalf("Run() returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return within 5s after context cancellation")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := application.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error: %v", err)
	}
}
