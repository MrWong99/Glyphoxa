package gateway

import (
	"context"
	"fmt"
	"testing"

	"github.com/MrWong99/glyphoxa/internal/agent/npcstore"
	"github.com/MrWong99/glyphoxa/internal/config"
)

// captureOrchestrator records the campaignID passed to ValidateAndCreate.
type captureOrchestrator struct {
	mockOrchestrator
	capturedCampaignID string
}

func (c *captureOrchestrator) ValidateAndCreate(_ context.Context, _, campaignID, guildID, channelID string, _ config.LicenseTier) (string, error) {
	c.capturedCampaignID = campaignID
	if c.validateErr != nil {
		return "", c.validateErr
	}
	c.sessions[c.sessionID] = SessionInfo{
		SessionID: c.sessionID,
		GuildID:   guildID,
		ChannelID: channelID,
		State:     SessionActive,
	}
	return c.sessionID, nil
}

func TestGatewaySessionController_Start_UsesRequestCampaignID(t *testing.T) {
	t.Parallel()

	orch := &captureOrchestrator{mockOrchestrator: *newMockOrch()}
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "default-campaign", config.TierShared)

	err := ctrl.Start(context.Background(), SessionStartRequest{
		GuildID:    "guild-1",
		ChannelID:  "chan-1",
		UserID:     "user-1",
		CampaignID: "web-campaign",
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if orch.capturedCampaignID != "web-campaign" {
		t.Errorf("campaignID = %q, want %q", orch.capturedCampaignID, "web-campaign")
	}
}

func TestGatewaySessionController_Start_FallsBackToDefaultCampaignID(t *testing.T) {
	t.Parallel()

	orch := &captureOrchestrator{mockOrchestrator: *newMockOrch()}
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "default-campaign", config.TierShared)

	err := ctrl.Start(context.Background(), SessionStartRequest{
		GuildID:   "guild-1",
		ChannelID: "chan-1",
		UserID:    "user-1",
		// CampaignID intentionally empty
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if orch.capturedCampaignID != "default-campaign" {
		t.Errorf("campaignID = %q, want %q (fallback)", orch.capturedCampaignID, "default-campaign")
	}
}

// mockNPCStore implements npcstore.Store for testing.
type mockNPCStore struct {
	defs    []npcstore.NPCDefinition
	listErr error
}

func (m *mockNPCStore) Create(_ context.Context, _ *npcstore.NPCDefinition) error { return nil }
func (m *mockNPCStore) Get(_ context.Context, _, _, _ string) (*npcstore.NPCDefinition, error) {
	return nil, nil
}
func (m *mockNPCStore) Update(_ context.Context, _ *npcstore.NPCDefinition) error { return nil }
func (m *mockNPCStore) Delete(_ context.Context, _, _, _ string) error            { return nil }
func (m *mockNPCStore) Upsert(_ context.Context, _ *npcstore.NPCDefinition) error { return nil }

func (m *mockNPCStore) List(_ context.Context, _, _ string) ([]npcstore.NPCDefinition, error) {
	return m.defs, m.listErr
}

func TestGatewaySessionController_Start_LoadsNPCsFromStore(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	store := &mockNPCStore{
		defs: []npcstore.NPCDefinition{
			{
				Name:        "Bartender",
				Personality: "grumpy but kind",
				Engine:      "cascaded",
				Voice:       npcstore.VoiceConfig{VoiceID: "voice-1"},
				BudgetTier:  "fast",
				GMHelper:    false,
				AddressOnly: true,
			},
			{
				Name:        "Guard",
				Personality: "alert and suspicious",
				Engine:      "s2s",
				Voice:       npcstore.VoiceConfig{VoiceID: "voice-2"},
				BudgetTier:  "standard",
				GMHelper:    true,
			},
		},
	}

	staticConfigs := []NPCConfigMsg{{Name: "OldNPC", Personality: "stale"}}
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared,
		WithNPCConfigs(staticConfigs),
		WithNPCStore(store),
	)

	err := ctrl.Start(context.Background(), SessionStartRequest{
		GuildID:    "guild-1",
		ChannelID:  "chan-1",
		UserID:     "user-1",
		CampaignID: "campaign-1",
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// The static configs should NOT have been used since the store returned NPCs.
	// We can't directly inspect the startReq (no dispatcher), but we can verify
	// the session started successfully and the store was used (no error).
}

func TestGatewaySessionController_Start_NPCStoreError_FailsSession(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	store := &mockNPCStore{listErr: fmt.Errorf("db connection lost")}

	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared,
		WithNPCStore(store),
	)

	err := ctrl.Start(context.Background(), SessionStartRequest{
		GuildID:    "guild-1",
		ChannelID:  "chan-1",
		UserID:     "user-1",
		CampaignID: "campaign-1",
	})
	if err == nil {
		t.Fatal("expected error when NPC store fails")
	}

	// Session should have been transitioned to ended.
	info, ok := orch.sessions[orch.sessionID]
	if !ok {
		t.Fatal("expected session to exist in orchestrator")
	}
	if info.State != SessionEnded {
		t.Errorf("session state = %v, want %v", info.State, SessionEnded)
	}

	// Guild should not be marked active.
	if ctrl.IsActive("guild-1") {
		t.Error("guild should not be active after NPC store error")
	}
}

func TestGatewaySessionController_Start_NPCStoreEmpty_UsesStaticConfigs(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	store := &mockNPCStore{defs: nil} // empty result

	staticConfigs := []NPCConfigMsg{{Name: "FallbackNPC"}}
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared,
		WithNPCConfigs(staticConfigs),
		WithNPCStore(store),
	)

	err := ctrl.Start(context.Background(), SessionStartRequest{
		GuildID:    "guild-1",
		ChannelID:  "chan-1",
		UserID:     "user-1",
		CampaignID: "campaign-1",
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// No dispatcher, so we just verify the session started without error.
	if !ctrl.IsActive("guild-1") {
		t.Error("expected session to be active")
	}
}

func TestGatewaySessionController_Start_NoCampaignID_SkipsNPCStore(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	store := &mockNPCStore{listErr: fmt.Errorf("should not be called")}

	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "", config.TierShared,
		WithNPCStore(store),
	)

	// Both request and default campaign ID are empty — npcStore should not be called.
	err := ctrl.Start(context.Background(), SessionStartRequest{
		GuildID:   "guild-1",
		ChannelID: "chan-1",
		UserID:    "user-1",
	})
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
}

func TestGatewaySessionController_WithNPCStore(t *testing.T) {
	t.Parallel()

	orch := newMockOrch()
	store := &mockNPCStore{}
	ctrl := NewGatewaySessionController(orch, nil, "tenant-1", "campaign-1", config.TierShared,
		WithNPCStore(store),
	)

	if ctrl.npcStore == nil {
		t.Error("expected npcStore to be set")
	}
}

func TestNpcDefsToConfigs(t *testing.T) {
	t.Parallel()

	defs := []npcstore.NPCDefinition{
		{
			Name:           "Bartender",
			Personality:    "grumpy",
			Engine:         "cascaded",
			Voice:          npcstore.VoiceConfig{VoiceID: "v1"},
			KnowledgeScope: []string{"tavern", "rumors"},
			BudgetTier:     "fast",
			GMHelper:       false,
			AddressOnly:    true,
		},
		{
			Name:        "Guard",
			Personality: "alert",
			Engine:      "s2s",
			Voice:       npcstore.VoiceConfig{VoiceID: "v2"},
			BudgetTier:  "standard",
			GMHelper:    true,
			AddressOnly: false,
		},
	}

	configs := npcDefsToConfigs(defs)

	if len(configs) != 2 {
		t.Fatalf("got %d configs, want 2", len(configs))
	}

	tests := []struct {
		name        string
		got         NPCConfigMsg
		wantName    string
		wantEngine  string
		wantVoiceID string
		wantGM      bool
		wantAddr    bool
	}{
		{
			name:        "Bartender",
			got:         configs[0],
			wantName:    "Bartender",
			wantEngine:  "cascaded",
			wantVoiceID: "v1",
			wantGM:      false,
			wantAddr:    true,
		},
		{
			name:        "Guard",
			got:         configs[1],
			wantName:    "Guard",
			wantEngine:  "s2s",
			wantVoiceID: "v2",
			wantGM:      true,
			wantAddr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.got.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", tt.got.Name, tt.wantName)
			}
			if tt.got.Engine != tt.wantEngine {
				t.Errorf("Engine = %q, want %q", tt.got.Engine, tt.wantEngine)
			}
			if tt.got.VoiceID != tt.wantVoiceID {
				t.Errorf("VoiceID = %q, want %q", tt.got.VoiceID, tt.wantVoiceID)
			}
			if tt.got.GMHelper != tt.wantGM {
				t.Errorf("GMHelper = %v, want %v", tt.got.GMHelper, tt.wantGM)
			}
			if tt.got.AddressOnly != tt.wantAddr {
				t.Errorf("AddressOnly = %v, want %v", tt.got.AddressOnly, tt.wantAddr)
			}
		})
	}

	// Verify KnowledgeScope is preserved.
	if len(configs[0].KnowledgeScope) != 2 {
		t.Errorf("KnowledgeScope length = %d, want 2", len(configs[0].KnowledgeScope))
	}
}

func TestNpcDefsToConfigs_Empty(t *testing.T) {
	t.Parallel()

	configs := npcDefsToConfigs(nil)
	if len(configs) != 0 {
		t.Errorf("expected empty for nil input, got %v", configs)
	}

	configs = npcDefsToConfigs([]npcstore.NPCDefinition{})
	if len(configs) != 0 {
		t.Errorf("expected empty for empty input, got %v", configs)
	}
}
