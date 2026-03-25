package commands

import (
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/MrWong99/glyphoxa/internal/app"
	"github.com/MrWong99/glyphoxa/internal/config"
	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
	audiomock "github.com/MrWong99/glyphoxa/pkg/audio/mock"
	memorymock "github.com/MrWong99/glyphoxa/pkg/memory/mock"
)

// mockInteractionMember satisfies the interactionMember interface used by
// PermissionChecker.IsDM, allowing unit tests without real disgo events.
type mockInteractionMember struct {
	member *discord.ResolvedMember
	user   discord.User
}

func (m *mockInteractionMember) Member() *discord.ResolvedMember { return m.member }
func (m *mockInteractionMember) User() discord.User              { return m.user }

// testMemberWithRoles creates a mockInteractionMember with the given role IDs.
func testMemberWithRoles(roleIDs ...snowflake.ID) *mockInteractionMember {
	return &mockInteractionMember{
		member: &discord.ResolvedMember{
			Member: discord.Member{
				RoleIDs: roleIDs,
			},
		},
		user: discord.User{ID: snowflake.ID(1)},
	}
}

// newTestSessionMgr creates a SessionManager with mock dependencies.
func newTestSessionMgr() *app.SessionManager {
	conn := &audiomock.Connection{}
	platform := &audiomock.Platform{ConnectResult: conn}
	cfg := &config.Config{
		Campaign: config.CampaignConfig{Name: "TestCampaign"},
	}
	return app.NewSessionManager(app.SessionManagerConfig{
		Platform:     platform,
		Config:       cfg,
		Providers:    &app.Providers{},
		SessionStore: &memorymock.SessionStore{},
	})
}

func TestSessionStart_NoDMRole(t *testing.T) {
	t.Parallel()

	perms := discordbot.NewPermissionChecker("123456789012345678")
	sm := newTestSessionMgr()
	sc := &SessionCommands{
		sessionMgr: sm,
		perms:      perms,
	}

	// Interaction without the DM role.
	member := testMemberWithRoles(snowflake.ID(999))

	if sc.perms.IsDM(member) {
		t.Fatal("expected IsDM to return false for user without DM role")
	}
}

func TestSessionStart_NotInVoice(t *testing.T) {
	t.Parallel()

	// With empty DM role, all users are DMs.
	perms := discordbot.NewPermissionChecker("")
	sm := newTestSessionMgr()
	sc := &SessionCommands{
		sessionMgr: sm,
		perms:      perms,
	}

	member := testMemberWithRoles()

	if !sc.perms.IsDM(member) {
		t.Fatal("expected IsDM to return true when DMRoleID is empty")
	}

	if sm.IsActive() {
		t.Fatal("session should not be active without voice channel")
	}
}

func TestSessionStart_Success(t *testing.T) {
	t.Parallel()

	sm := newTestSessionMgr()

	if err := sm.Start(t.Context(), "voice-ch-1", "dm-user-1"); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	if !sm.IsActive() {
		t.Fatal("expected session to be active after Start")
	}

	info := sm.Info()
	if info.ChannelID != "voice-ch-1" {
		t.Errorf("ChannelID = %q, want %q", info.ChannelID, "voice-ch-1")
	}
	if info.StartedBy != "dm-user-1" {
		t.Errorf("StartedBy = %q, want %q", info.StartedBy, "dm-user-1")
	}
}

func TestDefinition(t *testing.T) {
	t.Parallel()

	sc := &SessionCommands{}
	def := sc.Definition()

	if def.Name != "session" {
		t.Errorf("Name = %q, want %q", def.Name, "session")
	}
	if len(def.Options) != 4 {
		t.Fatalf("Options count = %d, want 4", len(def.Options))
	}

	expectedSubs := []string{"start", "stop", "recap", "voice-recap"}
	for i, want := range expectedSubs {
		if def.Options[i].OptionName() != want {
			t.Errorf("subcommand[%d] = %q, want %q", i, def.Options[i].OptionName(), want)
		}
	}
}

func TestDefinition_SubcommandTypes(t *testing.T) {
	t.Parallel()

	sc := &SessionCommands{}
	def := sc.Definition()

	for i, opt := range def.Options {
		if opt.Type() != discord.ApplicationCommandOptionTypeSubCommand {
			t.Errorf("subcommand[%d] type = %d, want SubCommand", i, opt.Type())
		}
	}
}

func TestDefinition_RecapHasSessionIDOption(t *testing.T) {
	t.Parallel()

	sc := &SessionCommands{}
	def := sc.Definition()

	var recapSub discord.ApplicationCommandOptionSubCommand
	var found bool
	for _, opt := range def.Options {
		if opt.OptionName() == "recap" {
			recapSub, found = opt.(discord.ApplicationCommandOptionSubCommand)
			break
		}
	}
	if !found {
		t.Fatal("recap subcommand not found")
	}
	if len(recapSub.Options) != 1 {
		t.Fatalf("recap options count = %d, want 1", len(recapSub.Options))
	}
	sessionIDOpt := recapSub.Options[0].(discord.ApplicationCommandOptionString)
	if sessionIDOpt.Name != "session_id" {
		t.Errorf("option name = %q, want %q", sessionIDOpt.Name, "session_id")
	}
	if sessionIDOpt.Required {
		t.Error("session_id should not be required")
	}
}

func TestDefinition_VoiceRecapHasSessionIDOption(t *testing.T) {
	t.Parallel()

	sc := &SessionCommands{}
	def := sc.Definition()

	var vrSub discord.ApplicationCommandOptionSubCommand
	var found bool
	for _, opt := range def.Options {
		if opt.OptionName() == "voice-recap" {
			vrSub, found = opt.(discord.ApplicationCommandOptionSubCommand)
			break
		}
	}
	if !found {
		t.Fatal("voice-recap subcommand not found")
	}
	if len(vrSub.Options) != 1 {
		t.Fatalf("voice-recap options count = %d, want 1", len(vrSub.Options))
	}
	sessionIDOpt := vrSub.Options[0].(discord.ApplicationCommandOptionString)
	if sessionIDOpt.Name != "session_id" {
		t.Errorf("option name = %q, want %q", sessionIDOpt.Name, "session_id")
	}
	if sessionIDOpt.Required {
		t.Error("session_id should not be required")
	}
}

func TestSessionRegister(t *testing.T) {
	t.Parallel()

	sc := &SessionCommands{}
	router := discordbot.NewCommandRouter()
	sc.Register(router)

	cmds := router.ApplicationCommands()
	found := false
	for _, cmd := range cmds {
		if cmd.CommandName() == "session" {
			found = true
			break
		}
	}
	if !found {
		t.Error("session command not registered with router")
	}
}

func TestSessionCommands_FieldAssignment(t *testing.T) {
	t.Parallel()

	perms := discordbot.NewPermissionChecker("some-role")
	sm := newTestSessionMgr()
	sc := &SessionCommands{
		sessionMgr: sm,
		perms:      perms,
	}

	if sc.sessionMgr != sm {
		t.Error("sessionMgr not set correctly")
	}
	if sc.perms != perms {
		t.Error("perms not set correctly")
	}
}

func TestSessionIsDM_WithDMRole(t *testing.T) {
	t.Parallel()

	dmRoleID := snowflake.ID(123456789012345678)
	perms := discordbot.NewPermissionChecker(dmRoleID.String())

	member := testMemberWithRoles(dmRoleID)
	if !perms.IsDM(member) {
		t.Fatal("expected IsDM to return true for user with DM role")
	}
}

func TestSessionIsDM_EmptyRoleAllowsAll(t *testing.T) {
	t.Parallel()

	perms := discordbot.NewPermissionChecker("")

	member := testMemberWithRoles()
	if !perms.IsDM(member) {
		t.Fatal("expected IsDM to return true when DMRoleID is empty")
	}
}

func TestSessionStopBefore_NotActive(t *testing.T) {
	t.Parallel()

	sm := newTestSessionMgr()
	if sm.IsActive() {
		t.Fatal("expected session to not be active initially")
	}
}

func TestGatewaySessionDefinition(t *testing.T) {
	t.Parallel()

	sc := &GatewaySessionCommands{
		perms: discordbot.NewPermissionChecker(""),
	}
	router := discordbot.NewCommandRouter()
	sc.Register(router)

	cmds := router.ApplicationCommands()
	found := false
	for _, cmd := range cmds {
		if cmd.CommandName() == "session" {
			found = true
			break
		}
	}
	if !found {
		t.Error("session command not registered in gateway mode")
	}
}

func TestRecapConstants(t *testing.T) {
	t.Parallel()

	t.Run("recapColor", func(t *testing.T) {
		t.Parallel()
		if recapColor != 0x3498DB {
			t.Errorf("recapColor = %d, want %d", recapColor, 0x3498DB)
		}
	})

	t.Run("maxEmbedDescriptionLen", func(t *testing.T) {
		t.Parallel()
		if maxEmbedDescriptionLen != 4096 {
			t.Errorf("maxEmbedDescriptionLen = %d, want 4096", maxEmbedDescriptionLen)
		}
	})

	t.Run("voiceRecapColor", func(t *testing.T) {
		t.Parallel()
		if voiceRecapColor != 0x9B59B6 {
			t.Errorf("voiceRecapColor = %d, want %d", voiceRecapColor, 0x9B59B6)
		}
	})

	t.Run("feedbackModalID", func(t *testing.T) {
		t.Parallel()
		if feedbackModalID != "feedback_modal" {
			t.Errorf("feedbackModalID = %q, want %q", feedbackModalID, "feedback_modal")
		}
	})
}

func TestSessionStart_CampaignName(t *testing.T) {
	t.Parallel()

	sm := newTestSessionMgr()

	if err := sm.Start(t.Context(), "voice-ch-2", "dm-user-2"); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	info := sm.Info()
	if !strings.Contains(info.CampaignName, "TestCampaign") {
		t.Errorf("CampaignName = %q, want it to contain %q", info.CampaignName, "TestCampaign")
	}
}

func TestGatewaySessionDefinition_SubcommandNames(t *testing.T) {
	t.Parallel()

	sc := &GatewaySessionCommands{
		perms: discordbot.NewPermissionChecker(""),
	}
	router := discordbot.NewCommandRouter()
	sc.Register(router)

	cmds := router.ApplicationCommands()
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}

	cmd := cmds[0].(discord.SlashCommandCreate)
	if cmd.Name != "session" {
		t.Errorf("Name = %q, want %q", cmd.Name, "session")
	}

	expectedSubs := []string{"start", "stop", "recap"}
	if len(cmd.Options) != len(expectedSubs) {
		t.Fatalf("subcommand count = %d, want %d", len(cmd.Options), len(expectedSubs))
	}
	for i, want := range expectedSubs {
		if cmd.Options[i].OptionName() != want {
			t.Errorf("subcommand[%d] = %q, want %q", i, cmd.Options[i].OptionName(), want)
		}
	}
}

func TestGatewaySessionDefinition_RecapHasSessionID(t *testing.T) {
	t.Parallel()

	sc := &GatewaySessionCommands{
		perms: discordbot.NewPermissionChecker(""),
	}
	router := discordbot.NewCommandRouter()
	sc.Register(router)

	cmds := router.ApplicationCommands()
	cmd := cmds[0].(discord.SlashCommandCreate)

	var recapSub discord.ApplicationCommandOptionSubCommand
	for _, opt := range cmd.Options {
		if opt.OptionName() == "recap" {
			recapSub = opt.(discord.ApplicationCommandOptionSubCommand)
			break
		}
	}

	if len(recapSub.Options) != 1 {
		t.Fatalf("recap options count = %d, want 1", len(recapSub.Options))
	}
	sessionIDOpt := recapSub.Options[0].(discord.ApplicationCommandOptionString)
	if sessionIDOpt.Name != "session_id" {
		t.Errorf("option name = %q, want %q", sessionIDOpt.Name, "session_id")
	}
	if sessionIDOpt.Required {
		t.Error("session_id should not be required")
	}
}

func TestGatewaySessionDefinition_AllSubcommandsAreSubCommandType(t *testing.T) {
	t.Parallel()

	sc := &GatewaySessionCommands{
		perms: discordbot.NewPermissionChecker(""),
	}
	router := discordbot.NewCommandRouter()
	sc.Register(router)

	cmds := router.ApplicationCommands()
	cmd := cmds[0].(discord.SlashCommandCreate)

	for i, opt := range cmd.Options {
		if opt.Type() != discord.ApplicationCommandOptionTypeSubCommand {
			t.Errorf("subcommand[%d] type = %d, want SubCommand", i, opt.Type())
		}
	}
}

func TestGatewaySessionCommands_FieldAssignment(t *testing.T) {
	t.Parallel()

	perms := discordbot.NewPermissionChecker("test-role")
	sc := &GatewaySessionCommands{
		perms: perms,
	}

	if sc.perms != perms {
		t.Error("perms not set correctly")
	}
}

func TestSessionStart_SessionIDNotEmpty(t *testing.T) {
	t.Parallel()

	sm := newTestSessionMgr()

	if err := sm.Start(t.Context(), "voice-ch-3", "dm-user-3"); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	info := sm.Info()
	if info.SessionID == "" {
		t.Error("SessionID should not be empty after start")
	}
}
