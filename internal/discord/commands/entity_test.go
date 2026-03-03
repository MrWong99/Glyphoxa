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
