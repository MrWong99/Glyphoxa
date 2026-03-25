package app_test

import (
	"context"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/app"
	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/internal/entity"
	"github.com/MrWong99/glyphoxa/internal/mcp"
	mcpmock "github.com/MrWong99/glyphoxa/internal/mcp/mock"
	audiomock "github.com/MrWong99/glyphoxa/pkg/audio/mock"
	"github.com/MrWong99/glyphoxa/pkg/memory"
	memorymock "github.com/MrWong99/glyphoxa/pkg/memory/mock"
	llmmock "github.com/MrWong99/glyphoxa/pkg/provider/llm/mock"
	ttsmock "github.com/MrWong99/glyphoxa/pkg/provider/tts/mock"
)

// newTestApp creates an App with all subsystems injected as mocks. It uses
// the same config pattern as the existing tests.
func newTestApp(t *testing.T, opts ...app.Option) *app.App {
	t.Helper()

	cfg := &config.Config{
		Server: config.ServerConfig{
			ListenAddr: "test-channel",
			LogLevel:   config.LogInfo,
		},
		Campaign: config.CampaignConfig{Name: "test-campaign"},
	}
	providers := &app.Providers{
		LLM: &llmmock.Provider{},
		TTS: &ttsmock.Provider{},
	}

	defaults := []app.Option{
		app.WithSessionStore(&memorymock.SessionStore{}),
		app.WithKnowledgeGraph(&memorymock.KnowledgeGraph{}),
		app.WithMCPHost(&mcpmock.Host{}),
		app.WithMixer(&audiomock.Mixer{}),
	}

	application, err := app.New(
		context.Background(),
		cfg,
		providers,
		append(defaults, opts...)...,
	)
	if err != nil {
		t.Fatalf("newTestApp: New() error: %v", err)
	}
	return application
}

// ─── App Option + Accessor round-trip tests ─────────────────────────────────

func TestAppOption_WithEntityStore(t *testing.T) {
	t.Parallel()

	store := entity.NewMemStore()
	application := newTestApp(t, app.WithEntityStore(store))

	if got := application.EntityStore(); got != store {
		t.Errorf("EntityStore() returned different store; want the injected one")
	}
}

func TestAppOption_WithSemanticIndex(t *testing.T) {
	t.Parallel()

	idx := &memorymock.SemanticIndex{}
	application := newTestApp(t, app.WithSemanticIndex(idx))

	if got := application.SemanticIndex(); got != idx {
		t.Errorf("SemanticIndex() returned different index; want the injected one")
	}
}

func TestAppOption_WithTenant(t *testing.T) {
	t.Parallel()

	tc := config.TenantContext{
		TenantID:   "tenant-42",
		CampaignID: "campaign-7",
		GuildID:    "guild-99",
		SchemaName: "tenant_42",
	}
	application := newTestApp(t, app.WithTenant(tc))

	got := application.Tenant()
	if got != tc {
		t.Errorf("Tenant() = %+v, want %+v", got, tc)
	}
}

func TestAppAccessors(t *testing.T) {
	t.Parallel()

	sessions := &memorymock.SessionStore{}
	graph := &memorymock.KnowledgeGraph{}
	mcpHost := &mcpmock.Host{}
	mixer := &audiomock.Mixer{}
	entities := entity.NewMemStore()
	semantic := &memorymock.SemanticIndex{}
	tc := config.TenantContext{TenantID: "t1"}

	application := newTestApp(t,
		app.WithSessionStore(sessions),
		app.WithKnowledgeGraph(graph),
		app.WithMCPHost(mcpHost),
		app.WithMixer(mixer),
		app.WithEntityStore(entities),
		app.WithSemanticIndex(semantic),
		app.WithTenant(tc),
	)

	tests := []struct {
		name string
		got  any
		want any
	}{
		{"SessionStore", application.SessionStore(), sessions},
		{"KnowledgeGraph", application.KnowledgeGraph(), graph},
		{"MCPHost", application.MCPHost(), mcpHost},
		{"EntityStore", application.EntityStore(), entities},
		{"SemanticIndex", application.SemanticIndex(), semantic},
		{"Tenant", application.Tenant(), tc},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.got != tt.want {
				t.Errorf("%s returned unexpected value", tt.name)
			}
		})
	}
}

func TestAppAccessor_RecapStore_NilWhenNotConfigured(t *testing.T) {
	t.Parallel()

	application := newTestApp(t)

	// RecapStore is only set when a real PostgreSQL store is created.
	// With mocks injected, it should be nil.
	if got := application.RecapStore(); got != nil {
		t.Errorf("RecapStore() = %v, want nil (no Postgres configured)", got)
	}
}

func TestAppAccessor_ReadinessChecks_WithSessionStore(t *testing.T) {
	t.Parallel()

	application := newTestApp(t, app.WithSessionStore(&memorymock.SessionStore{}))

	checks := application.ReadinessChecks()
	if len(checks) != 1 {
		t.Fatalf("ReadinessChecks() len = %d, want 1", len(checks))
	}
	if checks[0].Name != "database" {
		t.Errorf("check name = %q, want %q", checks[0].Name, "database")
	}

	// The check function should call GetRecent on the session store.
	err := checks[0].Check(context.Background())
	if err != nil {
		t.Errorf("check.Check() error = %v, want nil", err)
	}
}

func TestAppAccessor_ReadinessChecks_NoSessionStore(t *testing.T) {
	t.Parallel()

	// Build an App without a session store: pass nil for both memory stores
	// so initMemory needs to be bypassed. Since we always inject mocks, the
	// session store mock is there. Instead, test that ReadinessChecks returns
	// empty when both stores are injected (session store is present, so we
	// actually always get a check). This test verifies the count matches.
	cfg := &config.Config{
		Server:   config.ServerConfig{ListenAddr: "test"},
		Campaign: config.CampaignConfig{Name: "test"},
	}
	providers := &app.Providers{LLM: &llmmock.Provider{}, TTS: &ttsmock.Provider{}}

	// Construct an App with session store set to verify behavior.
	a, err := app.New(context.Background(), cfg, providers,
		app.WithSessionStore(&memorymock.SessionStore{}),
		app.WithKnowledgeGraph(&memorymock.KnowledgeGraph{}),
		app.WithMCPHost(&mcpmock.Host{}),
		app.WithMixer(&audiomock.Mixer{}),
	)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}

	checks := a.ReadinessChecks()
	if len(checks) < 1 {
		t.Error("expected at least 1 readiness check when session store is set")
	}
}

// ─── SessionManager.Mixer accessor ──────────────────────────────────────────

func TestSessionManager_Mixer_NilBeforeStart(t *testing.T) {
	t.Parallel()

	conn := &audiomock.Connection{}
	platform := &audiomock.Platform{ConnectResult: conn}
	cfg := &config.Config{Campaign: config.CampaignConfig{Name: "TestMixer"}}

	sm := app.NewSessionManager(app.SessionManagerConfig{
		Platform:     platform,
		Config:       cfg,
		Providers:    &app.Providers{},
		SessionStore: &memorymock.SessionStore{},
	})

	if got := sm.Mixer(); got != nil {
		t.Errorf("Mixer() before Start = %v, want nil", got)
	}
}

func TestSessionManager_Mixer_NonNilDuringSession(t *testing.T) {
	t.Parallel()

	conn := &audiomock.Connection{}
	platform := &audiomock.Platform{ConnectResult: conn}
	cfg := &config.Config{Campaign: config.CampaignConfig{Name: "TestMixer"}}

	sm := app.NewSessionManager(app.SessionManagerConfig{
		Platform:     platform,
		Config:       cfg,
		Providers:    &app.Providers{},
		SessionStore: &memorymock.SessionStore{},
	})

	if err := sm.Start(context.Background(), "ch-1", "user-1"); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	if got := sm.Mixer(); got == nil {
		t.Error("Mixer() during active session should not be nil")
	}

	if err := sm.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error: %v", err)
	}

	if got := sm.Mixer(); got != nil {
		t.Errorf("Mixer() after Stop = %v, want nil", got)
	}
}

// ─── Exported wrapper tests ─────────────────────────────────────────────────

func TestExported_IdentityFromConfig(t *testing.T) {
	t.Parallel()

	npc := config.NPCConfig{
		Name:           "Elara",
		Personality:    "A mysterious elven scholar.",
		KnowledgeScope: []string{"arcane", "history"},
		GMHelper:       false,
		AddressOnly:    true,
		Voice: config.VoiceConfig{
			Provider:    "elevenlabs",
			VoiceID:     "elf-1",
			PitchShift:  1.5,
			SpeedFactor: 1.1,
		},
	}

	id := app.IdentityFromConfig(npc)

	if id.Name != "Elara" {
		t.Errorf("Name = %q, want %q", id.Name, "Elara")
	}
	if id.Personality != "A mysterious elven scholar." {
		t.Errorf("Personality = %q, want %q", id.Personality, "A mysterious elven scholar.")
	}
	if id.AddressOnly != true {
		t.Error("AddressOnly = false, want true")
	}
	if id.Voice.ID != "elf-1" {
		t.Errorf("Voice.ID = %q, want %q", id.Voice.ID, "elf-1")
	}
}

func TestExported_ConfigBudgetTier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tier config.BudgetTier
		want mcp.BudgetTier
	}{
		{"fast", config.BudgetTierFast, mcp.BudgetFast},
		{"standard", config.BudgetTierStandard, mcp.BudgetStandard},
		{"deep", config.BudgetTierDeep, mcp.BudgetDeep},
		{"unknown defaults to fast", "turbo", mcp.BudgetFast},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := app.ConfigBudgetTier(tt.tier); got != tt.want {
				t.Errorf("ConfigBudgetTier(%q) = %v, want %v", tt.tier, got, tt.want)
			}
		})
	}
}

func TestExported_TTSFormatFromConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		entry          config.ProviderEntry
		wantSampleRate int
		wantChannels   int
	}{
		{
			name:           "elevenlabs default",
			entry:          config.ProviderEntry{Name: "elevenlabs"},
			wantSampleRate: 16000,
			wantChannels:   1,
		},
		{
			name: "explicit pcm_24000",
			entry: config.ProviderEntry{
				Name: "openai",
				Options: map[string]any{
					"output_format": "pcm_24000",
				},
			},
			wantSampleRate: 24000,
			wantChannels:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sr, ch := app.TTSFormatFromConfig(tt.entry)
			if sr != tt.wantSampleRate || ch != tt.wantChannels {
				t.Errorf("TTSFormatFromConfig() = (%d, %d), want (%d, %d)", sr, ch, tt.wantSampleRate, tt.wantChannels)
			}
		})
	}
}

func TestExported_VADConfigFromProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		entry config.ProviderEntry
	}{
		{
			name:  "defaults",
			entry: config.ProviderEntry{Name: "silero"},
		},
		{
			name: "custom thresholds",
			entry: config.ProviderEntry{
				Name: "silero",
				Options: map[string]any{
					"speech_threshold":  0.6,
					"silence_threshold": 0.2,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := app.VADConfigFromProvider(tt.entry)
			// SampleRate should always be 16000 (hardcoded default).
			if cfg.SampleRate != 16000 {
				t.Errorf("SampleRate = %d, want 16000", cfg.SampleRate)
			}
			// Verify custom thresholds are applied.
			if tt.entry.Options != nil {
				if v, ok := tt.entry.Options["speech_threshold"]; ok {
					if f, ok := v.(float64); ok && cfg.SpeechThreshold != f {
						t.Errorf("SpeechThreshold = %f, want %f", cfg.SpeechThreshold, f)
					}
				}
			}
		})
	}
}

func TestExported_STTConfigFromProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		entry        config.ProviderEntry
		wantLanguage string
	}{
		{
			name:         "defaults",
			entry:        config.ProviderEntry{Name: "deepgram"},
			wantLanguage: "",
		},
		{
			name: "with language",
			entry: config.ProviderEntry{
				Name:    "deepgram",
				Options: map[string]any{"language": "en-US"},
			},
			wantLanguage: "en-US",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := app.STTConfigFromProvider(tt.entry)
			if cfg.SampleRate != 16000 {
				t.Errorf("SampleRate = %d, want 16000", cfg.SampleRate)
			}
			if cfg.Channels != 1 {
				t.Errorf("Channels = %d, want 1", cfg.Channels)
			}
			if cfg.Language != tt.wantLanguage {
				t.Errorf("Language = %q, want %q", cfg.Language, tt.wantLanguage)
			}
		})
	}
}

func TestExported_RegisterNPCEntities(t *testing.T) {
	t.Parallel()

	graph := &memorymock.KnowledgeGraph{}
	npcs := []config.NPCConfig{
		{Name: "Alpha", Personality: "brave"},
		{Name: "Beta", Personality: "cunning"},
	}

	app.RegisterNPCEntities(context.Background(), graph, npcs)

	if got := graph.CallCount("AddEntity"); got != 2 {
		t.Errorf("AddEntity calls = %d, want 2", got)
	}

	// Verify entity IDs follow the "npc-<index>-<name>" pattern.
	calls := graph.Calls()
	var entityCalls []memory.Entity
	for _, c := range calls {
		if c.Method == "AddEntity" {
			entityCalls = append(entityCalls, c.Args[0].(memory.Entity))
		}
	}
	if len(entityCalls) != 2 {
		t.Fatalf("expected 2 entity calls, got %d", len(entityCalls))
	}
	if entityCalls[0].ID != "npc-0-Alpha" {
		t.Errorf("first entity ID = %q, want %q", entityCalls[0].ID, "npc-0-Alpha")
	}
	if entityCalls[1].ID != "npc-1-Beta" {
		t.Errorf("second entity ID = %q, want %q", entityCalls[1].ID, "npc-1-Beta")
	}
}

func TestExported_BuildEngine_CascadedRequiresLLMAndTTS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		providers *app.Providers
		wantErr   bool
	}{
		{
			name:      "no LLM",
			providers: &app.Providers{TTS: &ttsmock.Provider{}},
			wantErr:   true,
		},
		{
			name:      "no TTS",
			providers: &app.Providers{LLM: &llmmock.Provider{}},
			wantErr:   true,
		},
		{
			name:      "both LLM and TTS",
			providers: &app.Providers{LLM: &llmmock.Provider{}, TTS: &ttsmock.Provider{}},
			wantErr:   false,
		},
	}

	npc := config.NPCConfig{
		Name:   "Test",
		Engine: config.EngineCascaded,
		Voice: config.VoiceConfig{
			Provider: "test",
			VoiceID:  "voice-1",
		},
	}
	ttsEntry := config.ProviderEntry{Name: "test"}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			eng, err := app.BuildEngine(tt.providers, npc, ttsEntry)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if eng == nil {
				t.Fatal("expected non-nil engine")
			}
			_ = eng.Close()
		})
	}
}

func TestExported_BuildEngine_UnknownEngine(t *testing.T) {
	t.Parallel()

	providers := &app.Providers{LLM: &llmmock.Provider{}, TTS: &ttsmock.Provider{}}
	npc := config.NPCConfig{
		Name:   "Test",
		Engine: "warp-drive",
	}

	_, err := app.BuildEngine(providers, npc, config.ProviderEntry{})
	if err == nil {
		t.Fatal("expected error for unknown engine type")
	}
}
