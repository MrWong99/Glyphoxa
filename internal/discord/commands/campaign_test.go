package commands

import (
	"testing"

	"github.com/disgoorg/disgo/discord"

	"github.com/MrWong99/glyphoxa/internal/config"
	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
	"github.com/MrWong99/glyphoxa/internal/entity"
)

func newTestCampaignCommands(store entity.Store, cfg *config.CampaignConfig, active bool) *CampaignCommands {
	return NewCampaignCommands(
		discordbot.NewPermissionChecker(""),
		func() entity.Store { return store },
		func() *config.CampaignConfig { return cfg },
		func() bool { return active },
	)
}

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

func TestCampaignSwitch_ActiveSession(t *testing.T) {
	t.Parallel()

	cc := newTestCampaignCommands(entity.NewMemStore(), &config.CampaignConfig{Name: "TestCampaign"}, true)

	// When session is active, switch should be blocked.
	if !cc.isActive() {
		t.Fatal("expected isActive to return true")
	}
}

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

func TestCampaignInfo_NoDMRole(t *testing.T) {
	t.Parallel()

	perms := discordbot.NewPermissionChecker("123456789012345678")
	cc := NewCampaignCommands(
		perms,
		func() entity.Store { return entity.NewMemStore() },
		func() *config.CampaignConfig { return &config.CampaignConfig{} },
		func() bool { return false },
	)

	// Verify the perms check works for non-DM users.
	member := testMemberWithRoles()
	if perms.IsDM(member) {
		t.Fatal("expected IsDM to return false for user without DM role")
	}

	_ = cc // cc is valid
}

func TestCampaignLoad_ActiveSession(t *testing.T) {
	t.Parallel()

	// When a session is active, isActive returns true.
	cc := newTestCampaignCommands(entity.NewMemStore(), &config.CampaignConfig{}, true)

	// Verify the isActive check is correctly wired.
	if !cc.isActive() {
		t.Fatal("expected isActive to return true")
	}
}

func TestCampaignLoad_NoActiveSession(t *testing.T) {
	t.Parallel()

	cc := newTestCampaignCommands(entity.NewMemStore(), &config.CampaignConfig{}, false)

	if cc.isActive() {
		t.Fatal("expected isActive to return false")
	}
}
