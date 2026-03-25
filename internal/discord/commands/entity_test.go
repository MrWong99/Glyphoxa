package commands

import (
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
	"github.com/MrWong99/glyphoxa/internal/entity"
)

func newTestEntityCommands(store entity.Store, dmRoleID string) *EntityCommands {
	return NewEntityCommands(
		discordbot.NewPermissionChecker(dmRoleID),
		func() entity.Store { return store },
	)
}

func TestEntityDefinition(t *testing.T) {
	t.Parallel()

	ec := newTestEntityCommands(entity.NewMemStore(), "")
	def := ec.Definition()

	if def.Name != "entity" {
		t.Errorf("Name = %q, want %q", def.Name, "entity")
	}

	wantSubs := []string{"add", "list", "remove", "import"}
	if len(def.Options) != len(wantSubs) {
		t.Fatalf("subcommand count = %d, want %d", len(def.Options), len(wantSubs))
	}
	for i, want := range wantSubs {
		if def.Options[i].OptionName() != want {
			t.Errorf("subcommand[%d] = %q, want %q", i, def.Options[i].OptionName(), want)
		}
	}
}

func TestEntityDefinition_ListHasTypeFilter(t *testing.T) {
	t.Parallel()

	ec := newTestEntityCommands(entity.NewMemStore(), "")
	def := ec.Definition()

	// Find the list subcommand.
	var listSub discord.ApplicationCommandOptionSubCommand
	var found bool
	for _, opt := range def.Options {
		if opt.OptionName() == "list" {
			listSub, found = opt.(discord.ApplicationCommandOptionSubCommand)
			break
		}
	}
	if !found {
		t.Fatal("list subcommand not found")
	}
	if len(listSub.Options) == 0 {
		t.Fatal("list subcommand has no options")
	}
	typeOpt := listSub.Options[0].(discord.ApplicationCommandOptionString)
	if typeOpt.Name != "type" {
		t.Errorf("first option = %q, want %q", typeOpt.Name, "type")
	}
	if len(typeOpt.Choices) != 6 {
		t.Errorf("type choices = %d, want 6", len(typeOpt.Choices))
	}
}

func TestEntityDefinition_RemoveHasAutocomplete(t *testing.T) {
	t.Parallel()

	ec := newTestEntityCommands(entity.NewMemStore(), "")
	def := ec.Definition()

	var removeSub discord.ApplicationCommandOptionSubCommand
	var found bool
	for _, opt := range def.Options {
		if opt.OptionName() == "remove" {
			removeSub, found = opt.(discord.ApplicationCommandOptionSubCommand)
			break
		}
	}
	if !found {
		t.Fatal("remove subcommand not found")
	}
	if len(removeSub.Options) == 0 {
		t.Fatal("remove subcommand has no options")
	}
	nameOpt := removeSub.Options[0].(discord.ApplicationCommandOptionString)
	if nameOpt.Name != "name" {
		t.Errorf("option name = %q, want %q", nameOpt.Name, "name")
	}
	if !nameOpt.Autocomplete {
		t.Error("name option should have Autocomplete = true")
	}
}

func TestEntityRegister(t *testing.T) {
	t.Parallel()

	store := entity.NewMemStore()
	ec := newTestEntityCommands(store, "")
	router := discordbot.NewCommandRouter()
	ec.Register(router)

	cmds := router.ApplicationCommands()
	found := false
	for _, cmd := range cmds {
		if cmd.CommandName() == "entity" {
			found = true
			break
		}
	}
	if !found {
		t.Error("entity command not registered with router")
	}
}

func TestEntityAdd_NoDMRole(t *testing.T) {
	t.Parallel()

	perms := discordbot.NewPermissionChecker("123456789012345678")
	ec := NewEntityCommands(perms, func() entity.Store { return entity.NewMemStore() })

	member := testMemberWithRoles(snowflake.ID(999))

	if perms.IsDM(member) {
		t.Fatal("expected IsDM to return false for user without DM role")
	}

	if ec.perms != perms {
		t.Error("perms not set correctly")
	}
}

func TestEntityModalFields(t *testing.T) {
	t.Parallel()

	ec := newTestEntityCommands(entity.NewMemStore(), "")
	def := ec.Definition()

	// Verify the add subcommand exists (modal is opened from it).
	var addSub discord.ApplicationCommandOptionSubCommand
	var found bool
	for _, opt := range def.Options {
		if opt.OptionName() == "add" {
			addSub, found = opt.(discord.ApplicationCommandOptionSubCommand)
			break
		}
	}
	if !found {
		t.Fatal("add subcommand not found")
	}
	// The add subcommand has no inline options — it opens a modal.
	if len(addSub.Options) != 0 {
		t.Errorf("add subcommand options = %d, want 0 (modal-based)", len(addSub.Options))
	}
}

func TestEntityDefinition_ListTypeChoiceValues(t *testing.T) {
	t.Parallel()

	ec := newTestEntityCommands(entity.NewMemStore(), "")
	def := ec.Definition()

	var listSub discord.ApplicationCommandOptionSubCommand
	for _, opt := range def.Options {
		if opt.OptionName() == "list" {
			listSub = opt.(discord.ApplicationCommandOptionSubCommand)
			break
		}
	}

	typeOpt := listSub.Options[0].(discord.ApplicationCommandOptionString)

	expectedChoices := map[string]string{
		"NPC":      string(entity.EntityNPC),
		"Location": string(entity.EntityLocation),
		"Item":     string(entity.EntityItem),
		"Faction":  string(entity.EntityFaction),
		"Quest":    string(entity.EntityQuest),
		"Lore":     string(entity.EntityLore),
	}

	for _, choice := range typeOpt.Choices {
		expected, ok := expectedChoices[choice.Name]
		if !ok {
			t.Errorf("unexpected choice name %q", choice.Name)
			continue
		}
		if choice.Value != expected {
			t.Errorf("choice %q value = %q, want %q", choice.Name, choice.Value, expected)
		}
	}
}

func TestEntityDefinition_ImportHasNoOptions(t *testing.T) {
	t.Parallel()

	ec := newTestEntityCommands(entity.NewMemStore(), "")
	def := ec.Definition()

	var importSub discord.ApplicationCommandOptionSubCommand
	var found bool
	for _, opt := range def.Options {
		if opt.OptionName() == "import" {
			importSub, found = opt.(discord.ApplicationCommandOptionSubCommand)
			break
		}
	}
	if !found {
		t.Fatal("import subcommand not found")
	}
	if len(importSub.Options) != 0 {
		t.Errorf("import subcommand options = %d, want 0 (attachment-based)", len(importSub.Options))
	}
}

func TestEntityDefinition_RemoveNameIsRequired(t *testing.T) {
	t.Parallel()

	ec := newTestEntityCommands(entity.NewMemStore(), "")
	def := ec.Definition()

	var removeSub discord.ApplicationCommandOptionSubCommand
	for _, opt := range def.Options {
		if opt.OptionName() == "remove" {
			removeSub = opt.(discord.ApplicationCommandOptionSubCommand)
			break
		}
	}

	nameOpt := removeSub.Options[0].(discord.ApplicationCommandOptionString)
	if !nameOpt.Required {
		t.Error("remove name option should be required")
	}
}

func TestEntityDefinition_AllSubcommandsAreSubCommandType(t *testing.T) {
	t.Parallel()

	ec := newTestEntityCommands(entity.NewMemStore(), "")
	def := ec.Definition()

	for i, opt := range def.Options {
		if opt.Type() != discord.ApplicationCommandOptionTypeSubCommand {
			t.Errorf("option[%d] %q type = %d, want SubCommand", i, opt.OptionName(), opt.Type())
		}
	}
}

func TestEntityConstants(t *testing.T) {
	t.Parallel()

	t.Run("entityAddModalID", func(t *testing.T) {
		t.Parallel()
		if entityAddModalID != "entity_add_modal" {
			t.Errorf("entityAddModalID = %q, want %q", entityAddModalID, "entity_add_modal")
		}
	})

	t.Run("entityRemoveCancelID", func(t *testing.T) {
		t.Parallel()
		if entityRemoveCancelID != "entity_remove_cancel" {
			t.Errorf("entityRemoveCancelID = %q, want %q", entityRemoveCancelID, "entity_remove_cancel")
		}
	})

	t.Run("entityRemoveConfirmPrefix", func(t *testing.T) {
		t.Parallel()
		if entityRemoveConfirmPrefix != "entity_remove_confirm:" {
			t.Errorf("entityRemoveConfirmPrefix = %q, want %q", entityRemoveConfirmPrefix, "entity_remove_confirm:")
		}
	})

	t.Run("maxImportSize", func(t *testing.T) {
		t.Parallel()
		want := 10 << 20 // 10 MB
		if maxImportSize != want {
			t.Errorf("maxImportSize = %d, want %d", maxImportSize, want)
		}
	})
}

func TestNewEntityCommands_Fields(t *testing.T) {
	t.Parallel()

	perms := discordbot.NewPermissionChecker("test-role")
	store := entity.NewMemStore()
	ec := NewEntityCommands(perms, func() entity.Store { return store })

	if ec.perms != perms {
		t.Error("perms not set correctly")
	}
	if ec.getStore() != store {
		t.Error("store not wired correctly")
	}
}

func TestEntityDefinition_Description(t *testing.T) {
	t.Parallel()

	ec := newTestEntityCommands(entity.NewMemStore(), "")
	def := ec.Definition()

	if def.Description == "" {
		t.Error("expected non-empty description")
	}
}

func TestNewEntityCommands_NilStore(t *testing.T) {
	t.Parallel()

	ec := NewEntityCommands(
		discordbot.NewPermissionChecker(""),
		func() entity.Store { return nil },
	)

	if ec == nil {
		t.Fatal("NewEntityCommands returned nil")
	}
	if ec.getStore() != nil {
		t.Error("expected nil store from getStore()")
	}
}
