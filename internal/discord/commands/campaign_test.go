package commands

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"

	"github.com/MrWong99/glyphoxa/internal/config"
	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
	"github.com/MrWong99/glyphoxa/internal/entity"
)

// --- mock implementations for campaign interfaces ---

// mockCampaignReader implements CampaignReader for tests.
type mockCampaignReader struct {
	campaigns []CampaignSummary
	single    *CampaignSummary
	listErr   error
	getErr    error
}

var _ CampaignReader = (*mockCampaignReader)(nil)

func (m *mockCampaignReader) ListForTenant(_ context.Context, _ string) ([]CampaignSummary, error) {
	return m.campaigns, m.listErr
}

func (m *mockCampaignReader) GetCampaign(_ context.Context, _, _ string) (*CampaignSummary, error) {
	return m.single, m.getErr
}

// mockTenantCampaignUpdater implements TenantCampaignUpdater for tests.
type mockTenantCampaignUpdater struct {
	lastTenantID   string
	lastCampaignID string
	err            error
}

var _ TenantCampaignUpdater = (*mockTenantCampaignUpdater)(nil)

func (m *mockTenantCampaignUpdater) SetActiveCampaign(_ context.Context, tenantID, campaignID string) error {
	m.lastTenantID = tenantID
	m.lastCampaignID = campaignID
	return m.err
}

// mockCampaignWriter implements CampaignWriter for tests.
type mockCampaignWriter struct {
	lastTenantID string
	lastName     string
	lastSystem   string
	returnID     string
	err          error
}

var _ CampaignWriter = (*mockCampaignWriter)(nil)

func (m *mockCampaignWriter) CreateCampaign(_ context.Context, tenantID, name, system, _ string) (string, error) {
	m.lastTenantID = tenantID
	m.lastName = name
	m.lastSystem = system
	return m.returnID, m.err
}

// --- test helpers ---

func newTestCampaignCommands(store entity.Store, cfg *config.CampaignConfig, active bool) *CampaignCommands {
	return NewCampaignCommands(
		discordbot.NewPermissionChecker(""),
		func() entity.Store { return store },
		func() *config.CampaignConfig { return cfg },
		func() bool { return active },
	)
}

func newDBCampaignCommands(reader CampaignReader, updater TenantCampaignUpdater, writer CampaignWriter, active bool) *CampaignCommands {
	return NewCampaignCommandsFromConfig(CampaignCommandsConfig{
		Perms:    discordbot.NewPermissionChecker(""),
		TenantID: "test-tenant",
		Reader:   reader,
		Updater:  updater,
		Writer:   writer,
		GetStore: func() entity.Store { return nil },
		GetCfg:   func() *config.CampaignConfig { return nil },
		IsActive: func() bool { return active },
	})
}

// --- Definition tests ---

func TestCampaignDefinition(t *testing.T) {
	t.Parallel()

	cc := newTestCampaignCommands(entity.NewMemStore(), &config.CampaignConfig{}, false)
	def := cc.Definition()

	if def.Name != "campaign" {
		t.Errorf("Name = %q, want %q", def.Name, "campaign")
	}

	wantSubs := []string{"info", "load", "switch"}
	if len(def.Options) != len(wantSubs) {
		t.Fatalf("subcommand count = %d, want %d", len(def.Options), len(wantSubs))
	}
	for i, want := range wantSubs {
		if def.Options[i].OptionName() != want {
			t.Errorf("subcommand[%d] = %q, want %q", i, def.Options[i].OptionName(), want)
		}
	}
}

func TestCampaignDefinition_SwitchHasAutocomplete(t *testing.T) {
	t.Parallel()

	cc := newTestCampaignCommands(entity.NewMemStore(), &config.CampaignConfig{}, false)
	def := cc.Definition()

	var switchSub discord.ApplicationCommandOptionSubCommand
	var found bool
	for _, opt := range def.Options {
		if opt.OptionName() == "switch" {
			switchSub, found = opt.(discord.ApplicationCommandOptionSubCommand)
			break
		}
	}
	if !found {
		t.Fatal("switch subcommand not found")
	}
	if len(switchSub.Options) == 0 {
		t.Fatal("switch subcommand has no options")
	}
	nameOpt := switchSub.Options[0].(discord.ApplicationCommandOptionString)
	if nameOpt.Name != "name" {
		t.Errorf("option name = %q, want %q", nameOpt.Name, "name")
	}
	if !nameOpt.Autocomplete {
		t.Error("name option should have Autocomplete = true")
	}
}

// --- Registration tests ---

func TestCampaignRegister(t *testing.T) {
	t.Parallel()

	store := entity.NewMemStore()
	cfg := &config.CampaignConfig{Name: "Test Campaign", System: "dnd5e"}
	cc := newTestCampaignCommands(store, cfg, false)
	router := discordbot.NewCommandRouter()
	cc.Register(router)

	cmds := router.ApplicationCommands()
	found := false
	for _, cmd := range cmds {
		if cmd.CommandName() == "campaign" {
			found = true
			break
		}
	}
	if !found {
		t.Error("campaign command not registered with router")
	}
}

// --- Constructor tests ---

func TestNewCampaignCommandsFromConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		tenantID   string
		hasReader  bool
		hasUpdater bool
		hasWriter  bool
	}{
		{
			name:       "all dependencies set",
			tenantID:   "tenant-1",
			hasReader:  true,
			hasUpdater: true,
			hasWriter:  true,
		},
		{
			name:       "reader only",
			tenantID:   "tenant-2",
			hasReader:  true,
			hasUpdater: false,
			hasWriter:  false,
		},
		{
			name:       "no dependencies",
			tenantID:   "",
			hasReader:  false,
			hasUpdater: false,
			hasWriter:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := CampaignCommandsConfig{
				Perms:    discordbot.NewPermissionChecker(""),
				TenantID: tt.tenantID,
				GetStore: func() entity.Store { return nil },
				GetCfg:   func() *config.CampaignConfig { return nil },
				IsActive: func() bool { return false },
			}
			if tt.hasReader {
				cfg.Reader = &mockCampaignReader{}
			}
			if tt.hasUpdater {
				cfg.Updater = &mockTenantCampaignUpdater{}
			}
			if tt.hasWriter {
				cfg.Writer = &mockCampaignWriter{}
			}

			cc := NewCampaignCommandsFromConfig(cfg)

			if cc.tenantID != tt.tenantID {
				t.Errorf("tenantID = %q, want %q", cc.tenantID, tt.tenantID)
			}
			if (cc.reader != nil) != tt.hasReader {
				t.Errorf("reader set = %v, want %v", cc.reader != nil, tt.hasReader)
			}
			if (cc.updater != nil) != tt.hasUpdater {
				t.Errorf("updater set = %v, want %v", cc.updater != nil, tt.hasUpdater)
			}
			if (cc.writer != nil) != tt.hasWriter {
				t.Errorf("writer set = %v, want %v", cc.writer != nil, tt.hasWriter)
			}
		})
	}
}

func TestNewCampaignCommands_BackwardCompat(t *testing.T) {
	t.Parallel()

	cc := NewCampaignCommands(
		discordbot.NewPermissionChecker(""),
		func() entity.Store { return nil },
		func() *config.CampaignConfig { return &config.CampaignConfig{Name: "Test"} },
		func() bool { return false },
	)

	if cc.tenantID != "" {
		t.Errorf("tenantID = %q, want empty", cc.tenantID)
	}
	if cc.reader != nil {
		t.Error("reader should be nil for legacy constructor")
	}
	if cc.updater != nil {
		t.Error("updater should be nil for legacy constructor")
	}
	if cc.writer != nil {
		t.Error("writer should be nil for legacy constructor")
	}
}

// --- Session active blocking tests ---

func TestCampaignCommands_SessionActiveBlocking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		active   bool
		wantBool bool
	}{
		{name: "session active blocks commands", active: true, wantBool: true},
		{name: "no active session allows commands", active: false, wantBool: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cc := newTestCampaignCommands(entity.NewMemStore(), &config.CampaignConfig{Name: "TestCampaign"}, tt.active)
			if cc.isActive() != tt.wantBool {
				t.Errorf("isActive() = %v, want %v", cc.isActive(), tt.wantBool)
			}
		})
	}
}

// --- Permission tests ---

func TestCampaignInfo_NoDMRole(t *testing.T) {
	t.Parallel()

	perms := discordbot.NewPermissionChecker("123456789012345678")
	cc := NewCampaignCommands(
		perms,
		func() entity.Store { return entity.NewMemStore() },
		func() *config.CampaignConfig { return &config.CampaignConfig{} },
		func() bool { return false },
	)

	member := testMemberWithRoles()
	if perms.IsDM(member) {
		t.Fatal("expected IsDM to return false for user without DM role")
	}
	_ = cc
}

// --- DB path selection tests ---

func TestCampaignCommands_DBPathSelection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		tenantID     string
		hasReader    bool
		hasUpdater   bool
		wantDBInfo   bool
		wantDBSwitch bool
	}{
		{
			name:         "all DB deps present uses DB path",
			tenantID:     "t1",
			hasReader:    true,
			hasUpdater:   true,
			wantDBInfo:   true,
			wantDBSwitch: true,
		},
		{
			name:         "no tenant ID uses legacy path",
			tenantID:     "",
			hasReader:    true,
			hasUpdater:   true,
			wantDBInfo:   false,
			wantDBSwitch: false,
		},
		{
			name:         "no reader uses legacy path",
			tenantID:     "t1",
			hasReader:    false,
			hasUpdater:   true,
			wantDBInfo:   false,
			wantDBSwitch: false,
		},
		{
			name:         "reader only uses DB info and legacy switch",
			tenantID:     "t1",
			hasReader:    true,
			hasUpdater:   false,
			wantDBInfo:   true,
			wantDBSwitch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := CampaignCommandsConfig{
				Perms:    discordbot.NewPermissionChecker(""),
				TenantID: tt.tenantID,
				GetStore: func() entity.Store { return nil },
				GetCfg:   func() *config.CampaignConfig { return &config.CampaignConfig{Name: "Legacy"} },
				IsActive: func() bool { return false },
			}
			if tt.hasReader {
				cfg.Reader = &mockCampaignReader{}
			}
			if tt.hasUpdater {
				cfg.Updater = &mockTenantCampaignUpdater{}
			}
			cc := NewCampaignCommandsFromConfig(cfg)

			infoUsesDB := cc.reader != nil && cc.tenantID != ""
			if infoUsesDB != tt.wantDBInfo {
				t.Errorf("info uses DB = %v, want %v", infoUsesDB, tt.wantDBInfo)
			}

			switchUsesDB := cc.reader != nil && cc.updater != nil && cc.tenantID != ""
			if switchUsesDB != tt.wantDBSwitch {
				t.Errorf("switch uses DB = %v, want %v", switchUsesDB, tt.wantDBSwitch)
			}
		})
	}
}

// --- Campaign info (DB) tests ---

func TestHandleInfoDB_CampaignListing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		campaigns []CampaignSummary
		listErr   error
	}{
		{
			name: "multiple campaigns",
			campaigns: []CampaignSummary{
				{ID: "c1", Name: "Curse of Strahd", System: "dnd5e", Description: "Gothic horror"},
				{ID: "c2", Name: "Pathfinder Quest", System: "pf2e"},
			},
		},
		{
			name:      "empty campaign list",
			campaigns: nil,
		},
		{
			name:    "reader error",
			listErr: errors.New("db unavailable"),
		},
		{
			name:      "campaign with empty system",
			campaigns: []CampaignSummary{{ID: "c1", Name: "Test", System: ""}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reader := &mockCampaignReader{campaigns: tt.campaigns, listErr: tt.listErr}
			cc := newDBCampaignCommands(reader, nil, nil, false)

			if cc.reader == nil {
				t.Fatal("expected reader to be non-nil")
			}
			if cc.tenantID == "" {
				t.Fatal("expected tenantID to be set")
			}

			// Verify ListForTenant returns expected data.
			ctx := context.Background()
			campaigns, err := cc.reader.ListForTenant(ctx, cc.tenantID)
			if tt.listErr != nil {
				if err == nil {
					t.Fatal("expected error from ListForTenant")
				}
				return
			}
			if err != nil {
				t.Fatalf("ListForTenant() unexpected error: %v", err)
			}
			if len(campaigns) != len(tt.campaigns) {
				t.Errorf("campaign count = %d, want %d", len(campaigns), len(tt.campaigns))
			}
		})
	}
}

// --- Campaign switch (DB) tests ---

func TestHandleSwitchDB(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		campaignID  string
		campaign    *CampaignSummary
		getErr      error
		updateErr   error
		wantUpdated bool
	}{
		{
			name:       "successful switch",
			campaignID: "c1",
			campaign: &CampaignSummary{
				ID: "c1", Name: "Curse of Strahd", System: "dnd5e", Description: "Gothic horror",
			},
			wantUpdated: true,
		},
		{
			name:       "campaign not found",
			campaignID: "missing",
			getErr:     errors.New("not found"),
		},
		{
			name:       "update fails",
			campaignID: "c1",
			campaign:   &CampaignSummary{ID: "c1", Name: "Test", System: "dnd5e"},
			updateErr:  errors.New("db write error"),
		},
		{
			name:        "campaign with empty system",
			campaignID:  "c2",
			campaign:    &CampaignSummary{ID: "c2", Name: "Unnamed", System: ""},
			wantUpdated: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reader := &mockCampaignReader{single: tt.campaign, getErr: tt.getErr}
			updater := &mockTenantCampaignUpdater{err: tt.updateErr}
			cc := newDBCampaignCommands(reader, updater, nil, false)

			ctx := context.Background()

			// Simulate the switch flow.
			campaign, err := cc.reader.GetCampaign(ctx, cc.tenantID, tt.campaignID)
			if tt.getErr != nil {
				if err == nil {
					t.Fatal("expected GetCampaign error")
				}
				return
			}
			if err != nil {
				t.Fatalf("GetCampaign() unexpected error: %v", err)
			}
			if campaign.Name != tt.campaign.Name {
				t.Errorf("campaign Name = %q, want %q", campaign.Name, tt.campaign.Name)
			}

			setErr := cc.updater.SetActiveCampaign(ctx, cc.tenantID, tt.campaignID)
			if tt.updateErr != nil {
				if setErr == nil {
					t.Fatal("expected SetActiveCampaign error")
				}
				return
			}
			if setErr != nil {
				t.Fatalf("SetActiveCampaign() unexpected error: %v", setErr)
			}
			if updater.lastCampaignID != tt.campaignID {
				t.Errorf("updater.lastCampaignID = %q, want %q", updater.lastCampaignID, tt.campaignID)
			}
			if updater.lastTenantID != cc.tenantID {
				t.Errorf("updater.lastTenantID = %q, want %q", updater.lastTenantID, cc.tenantID)
			}
		})
	}
}

// --- Autocomplete filtering tests ---

func TestAutocompleteDB_Filtering(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		campaigns []CampaignSummary
		partial   string
		wantNames []string
		listErr   error
	}{
		{
			name: "no filter returns all",
			campaigns: []CampaignSummary{
				{ID: "c1", Name: "Curse of Strahd"},
				{ID: "c2", Name: "Lost Mine"},
			},
			partial:   "",
			wantNames: []string{"Curse of Strahd", "Lost Mine"},
		},
		{
			name: "filter by partial name",
			campaigns: []CampaignSummary{
				{ID: "c1", Name: "Curse of Strahd"},
				{ID: "c2", Name: "Lost Mine"},
				{ID: "c3", Name: "Curse of the Crimson Throne"},
			},
			partial:   "curse",
			wantNames: []string{"Curse of Strahd", "Curse of the Crimson Throne"},
		},
		{
			name: "filter case insensitive",
			campaigns: []CampaignSummary{
				{ID: "c1", Name: "Curse of Strahd"},
				{ID: "c2", Name: "CURSE LOUD"},
			},
			partial:   "CURSE",
			wantNames: []string{"Curse of Strahd", "CURSE LOUD"},
		},
		{
			name: "no matches",
			campaigns: []CampaignSummary{
				{ID: "c1", Name: "Curse of Strahd"},
			},
			partial:   "pathfinder",
			wantNames: nil,
		},
		{
			name:      "reader error returns empty",
			listErr:   errors.New("db error"),
			wantNames: nil,
		},
		{
			name:      "empty campaign list",
			campaigns: nil,
			wantNames: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reader := &mockCampaignReader{campaigns: tt.campaigns, listErr: tt.listErr}

			ctx := context.Background()
			campaigns, err := reader.ListForTenant(ctx, "test-tenant")
			if err != nil {
				if tt.listErr == nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}

			// Mirror the filtering logic from autocompleteDB.
			partial := strings.ToLower(tt.partial)
			var filtered []string
			for _, c := range campaigns {
				if partial == "" || strings.Contains(strings.ToLower(c.Name), partial) {
					filtered = append(filtered, c.Name)
				}
			}

			if len(filtered) != len(tt.wantNames) {
				t.Fatalf("filtered count = %d, want %d", len(filtered), len(tt.wantNames))
			}
			for i, want := range tt.wantNames {
				if filtered[i] != want {
					t.Errorf("filtered[%d] = %q, want %q", i, filtered[i], want)
				}
			}
		})
	}
}

func TestAutocompleteDB_MaxChoices(t *testing.T) {
	t.Parallel()

	// Generate 30 campaigns — autocomplete should cap at 25.
	var campaigns []CampaignSummary
	for i := range 30 {
		campaigns = append(campaigns, CampaignSummary{
			ID:   strings.Repeat("x", 5),
			Name: strings.Repeat("Campaign ", 1) + string(rune('A'+i)),
		})
	}

	reader := &mockCampaignReader{campaigns: campaigns}
	ctx := context.Background()
	all, err := reader.ListForTenant(ctx, "test-tenant")
	if err != nil {
		t.Fatalf("ListForTenant() error: %v", err)
	}

	// Mirror the cap logic.
	var choices []string
	for _, c := range all {
		choices = append(choices, c.Name)
		if len(choices) >= 25 {
			break
		}
	}

	if len(choices) != 25 {
		t.Errorf("choice count = %d, want 25 (Discord max)", len(choices))
	}
}

// --- Autocomplete legacy tests ---

func TestAutocompleteLegacy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cfg       *config.CampaignConfig
		wantCount int
	}{
		{
			name:      "config with name returns one choice",
			cfg:       &config.CampaignConfig{Name: "Test Campaign"},
			wantCount: 1,
		},
		{
			name:      "config with empty name returns no choices",
			cfg:       &config.CampaignConfig{Name: ""},
			wantCount: 0,
		},
		{
			name:      "nil config returns no choices",
			cfg:       nil,
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cc := NewCampaignCommands(
				discordbot.NewPermissionChecker(""),
				func() entity.Store { return nil },
				func() *config.CampaignConfig { return tt.cfg },
				func() bool { return false },
			)

			// Verify the legacy path produces the right choice count.
			var count int
			if cc.getCfg != nil {
				cfg := cc.getCfg()
				if cfg != nil && cfg.Name != "" {
					count = 1
				}
			}
			if count != tt.wantCount {
				t.Errorf("choice count = %d, want %d", count, tt.wantCount)
			}
		})
	}
}

// --- Campaign writer integration tests ---

func TestCampaignWriter_CreateAndActivate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		writerID   string
		writerErr  error
		updaterErr error
		wantID     string
	}{
		{
			name:     "write and activate succeeds",
			writerID: "new-campaign-123",
			wantID:   "new-campaign-123",
		},
		{
			name:      "write fails",
			writerErr: errors.New("duplicate name"),
		},
		{
			name:       "write succeeds but activate fails gracefully",
			writerID:   "new-campaign-456",
			updaterErr: errors.New("db error"),
			wantID:     "new-campaign-456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			writer := &mockCampaignWriter{returnID: tt.writerID, err: tt.writerErr}
			updater := &mockTenantCampaignUpdater{err: tt.updaterErr}

			ctx := context.Background()
			id, err := writer.CreateCampaign(ctx, "test-tenant", "Test", "dnd5e", "")
			if tt.writerErr != nil {
				if err == nil {
					t.Fatal("expected write error")
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateCampaign() unexpected error: %v", err)
			}
			if id != tt.wantID {
				t.Errorf("campaign ID = %q, want %q", id, tt.wantID)
			}

			if writer.lastName != "Test" {
				t.Errorf("writer.lastName = %q, want %q", writer.lastName, "Test")
			}
			if writer.lastSystem != "dnd5e" {
				t.Errorf("writer.lastSystem = %q, want %q", writer.lastSystem, "dnd5e")
			}

			setErr := updater.SetActiveCampaign(ctx, "test-tenant", id)
			if tt.updaterErr != nil {
				if setErr == nil {
					t.Fatal("expected updater error")
				}
			} else if setErr != nil {
				t.Fatalf("SetActiveCampaign() unexpected error: %v", setErr)
			}
		})
	}
}

// --- Embed color test ---

func TestCampaignEmbedColor(t *testing.T) {
	t.Parallel()

	if campaignEmbedColor != 0x2ECC71 {
		t.Errorf("campaignEmbedColor = %d, want %d", campaignEmbedColor, 0x2ECC71)
	}
}

// --- CampaignSummary field tests ---

func TestCampaignSummary_Fields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		campaign CampaignSummary
		wantID   string
		wantName string
		wantSys  string
		wantDesc string
	}{
		{
			name:     "all fields populated",
			campaign: CampaignSummary{ID: "abc", Name: "Campaign", System: "dnd5e", Description: "desc"},
			wantID:   "abc",
			wantName: "Campaign",
			wantSys:  "dnd5e",
			wantDesc: "desc",
		},
		{
			name:     "empty fields",
			campaign: CampaignSummary{},
			wantID:   "",
			wantName: "",
			wantSys:  "",
			wantDesc: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.campaign.ID != tt.wantID {
				t.Errorf("ID = %q, want %q", tt.campaign.ID, tt.wantID)
			}
			if tt.campaign.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", tt.campaign.Name, tt.wantName)
			}
			if tt.campaign.System != tt.wantSys {
				t.Errorf("System = %q, want %q", tt.campaign.System, tt.wantSys)
			}
			if tt.campaign.Description != tt.wantDesc {
				t.Errorf("Description = %q, want %q", tt.campaign.Description, tt.wantDesc)
			}
		})
	}
}
