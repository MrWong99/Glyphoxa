package discord

import (
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
)

// testInteractionMember satisfies the interactionMember interface for tests.
type testInteractionMember struct {
	member *discord.ResolvedMember
	user   discord.User
}

func (t *testInteractionMember) Member() *discord.ResolvedMember { return t.member }
func (t *testInteractionMember) User() discord.User              { return t.user }

func TestPermissionChecker_IsDM(t *testing.T) {
	t.Parallel()

	dmRole := snowflake.ID(123)
	otherRole := snowflake.ID(456)
	thirdRole := snowflake.ID(789)

	tests := []struct {
		name     string
		dmRoleID string
		member   *testInteractionMember
		want     bool
	}{
		{
			name:     "user with DM role",
			dmRoleID: "123",
			member: &testInteractionMember{
				member: &discord.ResolvedMember{
					Member: discord.Member{RoleIDs: []snowflake.ID{otherRole, dmRole, thirdRole}},
				},
			},
			want: true,
		},
		{
			name:     "user without DM role",
			dmRoleID: "123",
			member: &testInteractionMember{
				member: &discord.ResolvedMember{
					Member: discord.Member{RoleIDs: []snowflake.ID{otherRole, thirdRole}},
				},
			},
			want: false,
		},
		{
			name:     "empty DMRoleID allows all",
			dmRoleID: "",
			member: &testInteractionMember{
				member: &discord.ResolvedMember{
					Member: discord.Member{RoleIDs: []snowflake.ID{otherRole}},
				},
			},
			want: true,
		},
		{
			name:     "nil Member returns false",
			dmRoleID: "123",
			member: &testInteractionMember{
				member: nil,
			},
			want: false,
		},
		{
			name:     "user with empty roles",
			dmRoleID: "123",
			member: &testInteractionMember{
				member: &discord.ResolvedMember{
					Member: discord.Member{RoleIDs: []snowflake.ID{}},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pc := NewPermissionChecker(tt.dmRoleID)
			got := pc.IsDM(tt.member)
			if got != tt.want {
				t.Errorf("IsDM() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewCommandRouter(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()
	if r == nil {
		t.Fatal("NewCommandRouter() returned nil")
		return // unreachable; silences staticcheck SA5011
	}
	if len(r.commands) != 0 {
		t.Errorf("expected empty commands map, got %d entries", len(r.commands))
	}
	if len(r.autocomplete) != 0 {
		t.Errorf("expected empty autocomplete map, got %d entries", len(r.autocomplete))
	}
	if len(r.components) != 0 {
		t.Errorf("expected empty components map, got %d entries", len(r.components))
	}
	if len(r.modals) != 0 {
		t.Errorf("expected empty modals map, got %d entries", len(r.modals))
	}
}

func TestCommandRouter_ApplicationCommands(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()

	cmd := discord.SlashCommandCreate{Name: "test", Description: "test cmd"}
	r.RegisterCommand("test", cmd, func(e *events.ApplicationCommandInteractionCreate) {})

	cmds := r.ApplicationCommands()
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0].CommandName() != "test" {
		t.Errorf("expected command name 'test', got %q", cmds[0].CommandName())
	}
}

func TestCommandRouter_ApplicationCommands_Dedup(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()

	cmd := discord.SlashCommandCreate{Name: "npc", Description: "npc commands"}
	r.RegisterCommand("npc/mute", cmd, func(e *events.ApplicationCommandInteractionCreate) {})
	r.RegisterCommand("npc/unmute", cmd, func(e *events.ApplicationCommandInteractionCreate) {})

	cmds := r.ApplicationCommands()
	if len(cmds) != 1 {
		t.Fatalf("expected 1 deduplicated command, got %d", len(cmds))
	}
}

func TestCommandRouter_RegisterHandler(t *testing.T) {
	t.Parallel()

	r := NewCommandRouter()
	called := false
	r.RegisterHandler("test", func(e *events.ApplicationCommandInteractionCreate) {
		called = true
	})

	// Handler without command definition should not appear in ApplicationCommands.
	cmds := r.ApplicationCommands()
	if len(cmds) != 0 {
		t.Errorf("expected 0 commands, got %d", len(cmds))
	}

	// But the handler should still be accessible.
	entry, ok := r.commands["test"]
	if !ok {
		t.Fatal("expected handler to be registered")
	}
	entry.handler(nil)
	if !called {
		t.Error("handler was not called")
	}
}

func TestNewPermissionChecker_InvalidRoleID(t *testing.T) {
	t.Parallel()

	// An invalid (non-numeric) DM role ID should fall back to "all users are DMs".
	pc := NewPermissionChecker("not-a-number")
	if pc.hasRole {
		t.Error("expected hasRole=false for invalid role ID")
	}

	// Since hasRole is false, IsDM should return true for any member.
	member := &testInteractionMember{
		member: &discord.ResolvedMember{
			Member: discord.Member{RoleIDs: []snowflake.ID{}},
		},
	}
	if !pc.IsDM(member) {
		t.Error("IsDM should return true when hasRole=false (invalid role ID)")
	}
}

func TestNewPermissionChecker_ValidRoleID(t *testing.T) {
	t.Parallel()

	pc := NewPermissionChecker("999")
	if !pc.hasRole {
		t.Error("expected hasRole=true for valid role ID")
	}
	if pc.dmRoleID != snowflake.ID(999) {
		t.Errorf("dmRoleID = %v, want 999", pc.dmRoleID)
	}
}

func TestBot_Accessors(t *testing.T) {
	t.Parallel()

	router := NewCommandRouter()
	perms := NewPermissionChecker("42")

	b := &Bot{
		router:  router,
		perms:   perms,
		guildID: snowflake.ID(12345),
	}

	t.Run("GuildID", func(t *testing.T) {
		t.Parallel()
		if got := b.GuildID(); got != snowflake.ID(12345) {
			t.Errorf("GuildID() = %v, want 12345", got)
		}
	})

	t.Run("Router", func(t *testing.T) {
		t.Parallel()
		if got := b.Router(); got != router {
			t.Errorf("Router() returned unexpected value")
		}
	})

	t.Run("Permissions", func(t *testing.T) {
		t.Parallel()
		if got := b.Permissions(); got != perms {
			t.Errorf("Permissions() returned unexpected value")
		}
	})

	t.Run("Client_nil", func(t *testing.T) {
		t.Parallel()
		// Client should be nil when no real connection has been established.
		if got := b.Client(); got != nil {
			t.Errorf("Client() = %v, want nil", got)
		}
	})

	t.Run("Platform_zero", func(t *testing.T) {
		t.Parallel()
		// Platform returns a typed nil *discordaudio.Platform wrapped in an
		// audio.Platform interface, so we just verify it doesn't panic.
		_ = b.Platform()
	})
}
