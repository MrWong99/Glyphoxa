package commands

import (
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
	if len(def.Options) != 3 {
		t.Fatalf("Options count = %d, want 3", len(def.Options))
	}

	expectedSubs := []string{"start", "stop", "recap"}
	for i, want := range expectedSubs {
		if def.Options[i].OptionName() != want {
			t.Errorf("subcommand[%d] = %q, want %q", i, def.Options[i].OptionName(), want)
		}
	}
}
