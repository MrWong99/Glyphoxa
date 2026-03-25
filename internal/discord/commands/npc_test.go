package commands

import (
	"testing"

	"github.com/disgoorg/disgo/discord"

	"github.com/MrWong99/glyphoxa/internal/agent"
	"github.com/MrWong99/glyphoxa/internal/agent/mock"
	"github.com/MrWong99/glyphoxa/internal/agent/orchestrator"
	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
)

func newTestOrchestrator(agents ...agent.NPCAgent) *orchestrator.Orchestrator {
	return orchestrator.New(agents)
}

func TestNPCDefinition(t *testing.T) {
	t.Parallel()

	nc := NewNPCCommands(discordbot.NewPermissionChecker(""), nil)
	def := nc.Definition()

	if def.Name != "npc" {
		t.Errorf("Name = %q, want %q", def.Name, "npc")
	}

	expectedSubs := []string{"list", "mute", "unmute", "speak", "muteall", "unmuteall"}
	if len(def.Options) != len(expectedSubs) {
		t.Fatalf("Options count = %d, want %d", len(def.Options), len(expectedSubs))
	}

	for i, name := range expectedSubs {
		if def.Options[i].OptionName() != name {
			t.Errorf("subcommand[%d] = %q, want %q", i, def.Options[i].OptionName(), name)
		}
		if def.Options[i].Type() != discord.ApplicationCommandOptionTypeSubCommand {
			t.Errorf("subcommand[%d] type = %d, want SubCommand", i, def.Options[i].Type())
		}
	}

	// Verify mute has a "name" option with autocomplete.
	muteSub := def.Options[1].(discord.ApplicationCommandOptionSubCommand)
	if len(muteSub.Options) != 1 {
		t.Fatalf("mute options count = %d, want 1", len(muteSub.Options))
	}
	muteNameOpt := muteSub.Options[0].(discord.ApplicationCommandOptionString)
	if muteNameOpt.Name != "name" {
		t.Errorf("mute option name = %q, want %q", muteNameOpt.Name, "name")
	}
	if !muteNameOpt.Autocomplete {
		t.Error("mute name option should have autocomplete enabled")
	}

	// Verify speak has "name" and "text" options.
	speakSub := def.Options[3].(discord.ApplicationCommandOptionSubCommand)
	if len(speakSub.Options) != 2 {
		t.Fatalf("speak options count = %d, want 2", len(speakSub.Options))
	}
	if speakSub.Options[0].OptionName() != "name" {
		t.Errorf("speak option[0] = %q, want %q", speakSub.Options[0].OptionName(), "name")
	}
	if speakSub.Options[1].OptionName() != "text" {
		t.Errorf("speak option[1] = %q, want %q", speakSub.Options[1].OptionName(), "text")
	}
}

func TestNPCList_NoSession(t *testing.T) {
	t.Parallel()

	perms := discordbot.NewPermissionChecker("")
	nc := NewNPCCommands(perms, func() *orchestrator.Orchestrator { return nil })

	orch := nc.getOrch()
	if orch != nil {
		t.Fatal("expected nil orchestrator")
	}
}

func TestNPCList_WithAgents(t *testing.T) {
	t.Parallel()

	npc1 := &mock.NPCAgent{
		IDResult:   "greymantle",
		NameResult: "Greymantle the Sage",
		IdentityResult: agent.NPCIdentity{
			Name:        "Greymantle the Sage",
			Personality: "A wise old sage.",
		},
	}
	npc2 := &mock.NPCAgent{
		IDResult:   "thorn",
		NameResult: "Thorn",
		IdentityResult: agent.NPCIdentity{
			Name:        "Thorn",
			Personality: "A gruff blacksmith.",
		},
	}

	orch := newTestOrchestrator(npc1, npc2)

	perms := discordbot.NewPermissionChecker("")
	nc := NewNPCCommands(perms, func() *orchestrator.Orchestrator { return orch })

	agents := nc.getOrch().ActiveAgents()
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}

	for _, a := range agents {
		muted, err := orch.IsMuted(a.ID())
		if err != nil {
			t.Fatalf("IsMuted(%q) error: %v", a.ID(), err)
		}
		if muted {
			t.Errorf("agent %q should not be muted by default", a.ID())
		}
	}
}

func TestNPCMuteUnmute(t *testing.T) {
	t.Parallel()

	npc := &mock.NPCAgent{
		IDResult:   "greymantle",
		NameResult: "Greymantle",
	}
	orch := newTestOrchestrator(npc)

	if err := orch.MuteAgent("greymantle"); err != nil {
		t.Fatalf("MuteAgent error: %v", err)
	}
	muted, err := orch.IsMuted("greymantle")
	if err != nil {
		t.Fatalf("IsMuted error: %v", err)
	}
	if !muted {
		t.Error("expected agent to be muted")
	}

	if err := orch.UnmuteAgent("greymantle"); err != nil {
		t.Fatalf("UnmuteAgent error: %v", err)
	}
	muted, err = orch.IsMuted("greymantle")
	if err != nil {
		t.Fatalf("IsMuted error: %v", err)
	}
	if muted {
		t.Error("expected agent to be unmuted")
	}
}

func TestNPCMuteAll_UnmuteAll(t *testing.T) {
	t.Parallel()

	npc1 := &mock.NPCAgent{IDResult: "a", NameResult: "Alpha"}
	npc2 := &mock.NPCAgent{IDResult: "b", NameResult: "Beta"}
	orch := newTestOrchestrator(npc1, npc2)

	count := orch.MuteAll()
	if count != 2 {
		t.Errorf("MuteAll = %d, want 2", count)
	}

	count = orch.MuteAll()
	if count != 0 {
		t.Errorf("second MuteAll = %d, want 0", count)
	}

	count = orch.UnmuteAll()
	if count != 2 {
		t.Errorf("UnmuteAll = %d, want 2", count)
	}
}

func TestNPCAgentByName(t *testing.T) {
	t.Parallel()

	npc := &mock.NPCAgent{IDResult: "grey", NameResult: "Greymantle"}
	orch := newTestOrchestrator(npc)

	found := orch.AgentByName("greymantle")
	if found == nil {
		t.Fatal("expected to find agent by case-insensitive name")
	}
	if found.ID() != "grey" {
		t.Errorf("ID = %q, want %q", found.ID(), "grey")
	}

	notFound := orch.AgentByName("nonexistent")
	if notFound != nil {
		t.Error("expected nil for nonexistent agent name")
	}
}

func TestNPCRegister(t *testing.T) {
	t.Parallel()

	perms := discordbot.NewPermissionChecker("")
	nc := NewNPCCommands(perms, func() *orchestrator.Orchestrator { return nil })
	router := discordbot.NewCommandRouter()

	nc.Register(router)

	cmds := router.ApplicationCommands()
	if len(cmds) != 1 {
		t.Fatalf("expected 1 application command, got %d", len(cmds))
	}
	if cmds[0].CommandName() != "npc" {
		t.Errorf("command name = %q, want %q", cmds[0].CommandName(), "npc")
	}
}

func TestNPCDefinition_UnmuteHasAutocomplete(t *testing.T) {
	t.Parallel()

	nc := NewNPCCommands(discordbot.NewPermissionChecker(""), nil)
	def := nc.Definition()

	unmuteSub := def.Options[2].(discord.ApplicationCommandOptionSubCommand)
	if len(unmuteSub.Options) != 1 {
		t.Fatalf("unmute options count = %d, want 1", len(unmuteSub.Options))
	}
	nameOpt := unmuteSub.Options[0].(discord.ApplicationCommandOptionString)
	if nameOpt.Name != "name" {
		t.Errorf("option name = %q, want %q", nameOpt.Name, "name")
	}
	if !nameOpt.Autocomplete {
		t.Error("unmute name option should have autocomplete enabled")
	}
}

func TestNPCDefinition_SpeakNameHasAutocomplete(t *testing.T) {
	t.Parallel()

	nc := NewNPCCommands(discordbot.NewPermissionChecker(""), nil)
	def := nc.Definition()

	speakSub := def.Options[3].(discord.ApplicationCommandOptionSubCommand)
	nameOpt := speakSub.Options[0].(discord.ApplicationCommandOptionString)
	if !nameOpt.Autocomplete {
		t.Error("speak name option should have autocomplete enabled")
	}
	if !nameOpt.Required {
		t.Error("speak name option should be required")
	}
}

func TestNPCDefinition_SpeakTextIsRequired(t *testing.T) {
	t.Parallel()

	nc := NewNPCCommands(discordbot.NewPermissionChecker(""), nil)
	def := nc.Definition()

	speakSub := def.Options[3].(discord.ApplicationCommandOptionSubCommand)
	textOpt := speakSub.Options[1].(discord.ApplicationCommandOptionString)
	if textOpt.Name != "text" {
		t.Errorf("option name = %q, want %q", textOpt.Name, "text")
	}
	if !textOpt.Required {
		t.Error("speak text option should be required")
	}
}

func TestNPCDefinition_MuteAllAndUnmuteAllHaveNoOptions(t *testing.T) {
	t.Parallel()

	nc := NewNPCCommands(discordbot.NewPermissionChecker(""), nil)
	def := nc.Definition()

	muteAllSub := def.Options[4].(discord.ApplicationCommandOptionSubCommand)
	if len(muteAllSub.Options) != 0 {
		t.Errorf("muteall options count = %d, want 0", len(muteAllSub.Options))
	}

	unmuteAllSub := def.Options[5].(discord.ApplicationCommandOptionSubCommand)
	if len(unmuteAllSub.Options) != 0 {
		t.Errorf("unmuteall options count = %d, want 0", len(unmuteAllSub.Options))
	}
}

func TestNPCDefinition_ListHasNoOptions(t *testing.T) {
	t.Parallel()

	nc := NewNPCCommands(discordbot.NewPermissionChecker(""), nil)
	def := nc.Definition()

	listSub := def.Options[0].(discord.ApplicationCommandOptionSubCommand)
	if len(listSub.Options) != 0 {
		t.Errorf("list options count = %d, want 0", len(listSub.Options))
	}
}

func TestNewNPCCommands_Fields(t *testing.T) {
	t.Parallel()

	perms := discordbot.NewPermissionChecker("test-role")
	orch := newTestOrchestrator()
	getOrch := func() *orchestrator.Orchestrator { return orch }

	nc := NewNPCCommands(perms, getOrch)

	if nc.perms != perms {
		t.Error("perms not set correctly")
	}
	if nc.getOrch() != orch {
		t.Error("getOrch not wired correctly")
	}
}

func TestGatewayNPCDefinition(t *testing.T) {
	t.Parallel()

	nc := &GatewayNPCCommands{
		perms: discordbot.NewPermissionChecker(""),
	}
	def := nc.Definition()

	if def.Name != "npc" {
		t.Errorf("Name = %q, want %q", def.Name, "npc")
	}

	expectedSubs := []string{"list", "mute", "unmute", "speak", "muteall", "unmuteall"}
	if len(def.Options) != len(expectedSubs) {
		t.Fatalf("Options count = %d, want %d", len(def.Options), len(expectedSubs))
	}
	for i, name := range expectedSubs {
		if def.Options[i].OptionName() != name {
			t.Errorf("subcommand[%d] = %q, want %q", i, def.Options[i].OptionName(), name)
		}
	}
}

func TestGatewayNPCDefinition_SpeakHasNameAndText(t *testing.T) {
	t.Parallel()

	nc := &GatewayNPCCommands{
		perms: discordbot.NewPermissionChecker(""),
	}
	def := nc.Definition()

	speakSub := def.Options[3].(discord.ApplicationCommandOptionSubCommand)
	if len(speakSub.Options) != 2 {
		t.Fatalf("speak options count = %d, want 2", len(speakSub.Options))
	}
	nameOpt := speakSub.Options[0].(discord.ApplicationCommandOptionString)
	if !nameOpt.Autocomplete {
		t.Error("speak name should have autocomplete")
	}
	textOpt := speakSub.Options[1].(discord.ApplicationCommandOptionString)
	if textOpt.Name != "text" {
		t.Errorf("speak option[1] = %q, want %q", textOpt.Name, "text")
	}
	if !textOpt.Required {
		t.Error("speak text should be required")
	}
}

func TestNPCMuteNonexistent(t *testing.T) {
	t.Parallel()

	orch := newTestOrchestrator()

	err := orch.MuteAgent("nonexistent")
	if err == nil {
		t.Fatal("expected error when muting nonexistent agent")
	}
}

func TestNPCUnmuteNonexistent(t *testing.T) {
	t.Parallel()

	orch := newTestOrchestrator()

	err := orch.UnmuteAgent("nonexistent")
	if err == nil {
		t.Fatal("expected error when unmuting nonexistent agent")
	}
}

func TestGatewayNPCRegister(t *testing.T) {
	t.Parallel()

	nc := &GatewayNPCCommands{
		perms: discordbot.NewPermissionChecker(""),
	}
	router := discordbot.NewCommandRouter()
	nc.Register(router)

	cmds := router.ApplicationCommands()
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0].CommandName() != "npc" {
		t.Errorf("command name = %q, want %q", cmds[0].CommandName(), "npc")
	}
}

func TestGatewayNPCDefinition_MuteAllAndUnmuteAllHaveNoOptions(t *testing.T) {
	t.Parallel()

	nc := &GatewayNPCCommands{
		perms: discordbot.NewPermissionChecker(""),
	}
	def := nc.Definition()

	muteAllSub := def.Options[4].(discord.ApplicationCommandOptionSubCommand)
	if len(muteAllSub.Options) != 0 {
		t.Errorf("muteall options count = %d, want 0", len(muteAllSub.Options))
	}

	unmuteAllSub := def.Options[5].(discord.ApplicationCommandOptionSubCommand)
	if len(unmuteAllSub.Options) != 0 {
		t.Errorf("unmuteall options count = %d, want 0", len(unmuteAllSub.Options))
	}
}

func TestGatewayNPCDefinition_ListHasNoOptions(t *testing.T) {
	t.Parallel()

	nc := &GatewayNPCCommands{
		perms: discordbot.NewPermissionChecker(""),
	}
	def := nc.Definition()

	listSub := def.Options[0].(discord.ApplicationCommandOptionSubCommand)
	if len(listSub.Options) != 0 {
		t.Errorf("list options count = %d, want 0", len(listSub.Options))
	}
}

func TestGatewayNPCDefinition_MuteHasAutocomplete(t *testing.T) {
	t.Parallel()

	nc := &GatewayNPCCommands{
		perms: discordbot.NewPermissionChecker(""),
	}
	def := nc.Definition()

	muteSub := def.Options[1].(discord.ApplicationCommandOptionSubCommand)
	if len(muteSub.Options) != 1 {
		t.Fatalf("mute options count = %d, want 1", len(muteSub.Options))
	}
	nameOpt := muteSub.Options[0].(discord.ApplicationCommandOptionString)
	if !nameOpt.Autocomplete {
		t.Error("mute name option should have autocomplete enabled")
	}
	if !nameOpt.Required {
		t.Error("mute name option should be required")
	}
}

func TestGatewayNPCDefinition_UnmuteHasAutocomplete(t *testing.T) {
	t.Parallel()

	nc := &GatewayNPCCommands{
		perms: discordbot.NewPermissionChecker(""),
	}
	def := nc.Definition()

	unmuteSub := def.Options[2].(discord.ApplicationCommandOptionSubCommand)
	if len(unmuteSub.Options) != 1 {
		t.Fatalf("unmute options count = %d, want 1", len(unmuteSub.Options))
	}
	nameOpt := unmuteSub.Options[0].(discord.ApplicationCommandOptionString)
	if !nameOpt.Autocomplete {
		t.Error("unmute name option should have autocomplete enabled")
	}
}

func TestGatewayNPCDefinition_AllSubcommandsAreSubCommandType(t *testing.T) {
	t.Parallel()

	nc := &GatewayNPCCommands{
		perms: discordbot.NewPermissionChecker(""),
	}
	def := nc.Definition()

	for i, opt := range def.Options {
		if opt.Type() != discord.ApplicationCommandOptionTypeSubCommand {
			t.Errorf("option[%d] %q type = %d, want SubCommand", i, opt.OptionName(), opt.Type())
		}
	}
}

func TestGatewayNPCCommands_FieldAssignment(t *testing.T) {
	t.Parallel()

	perms := discordbot.NewPermissionChecker("test-role")
	nc := &GatewayNPCCommands{
		perms: perms,
	}

	if nc.perms != perms {
		t.Error("perms not set correctly")
	}
}

func TestNPCIsMutedNonexistent(t *testing.T) {
	t.Parallel()

	orch := newTestOrchestrator()

	_, err := orch.IsMuted("nonexistent")
	if err == nil {
		t.Fatal("expected error for IsMuted on nonexistent agent")
	}
}
