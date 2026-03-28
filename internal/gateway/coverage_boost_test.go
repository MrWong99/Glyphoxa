package gateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	disgoGateway "github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"

	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
	"github.com/MrWong99/glyphoxa/internal/gateway/audiobridge"
)

// ── AddGatewayBot ───────────────────────────────────────────────────────────

func TestAddGatewayBot_Success(t *testing.T) {
	t.Parallel()

	bm := NewBotManager()
	guildIDs := []snowflake.ID{snowflake.ID(111), snowflake.ID(222)}
	gwBot := NewGatewayBot(nil, nil, nil, "tenant-gw", guildIDs)

	err := bm.AddGatewayBot("tenant-gw", gwBot)
	if err != nil {
		t.Fatalf("AddGatewayBot failed: %v", err)
	}

	// Verify the entry is retrievable via GetBot.
	got, ok := bm.GetBot("tenant-gw")
	if !ok {
		t.Fatal("expected GetBot to find the bot")
	}
	if got != gwBot {
		t.Error("GetBot returned a different GatewayBot")
	}

	// Guild IDs should be stored and deduped in the allowlist.
	connected, count := bm.IsBotConnected("tenant-gw")
	if !connected {
		t.Error("expected connected to be true")
	}
	if count != 2 {
		t.Errorf("got guild count %d, want 2", count)
	}
}

func TestAddGatewayBot_Duplicate(t *testing.T) {
	t.Parallel()

	bm := NewBotManager()
	gwBot := NewGatewayBot(nil, nil, nil, "tenant-dup", nil)

	if err := bm.AddGatewayBot("tenant-dup", gwBot); err != nil {
		t.Fatalf("first AddGatewayBot failed: %v", err)
	}

	err := bm.AddGatewayBot("tenant-dup", gwBot)
	if err == nil {
		t.Fatal("expected error for duplicate AddGatewayBot")
	}
	if !containsSubstr(err.Error(), "already registered") {
		t.Errorf("error %q does not contain 'already registered'", err)
	}
}

func TestAddGatewayBot_EmptyGuildIDs(t *testing.T) {
	t.Parallel()

	bm := NewBotManager()
	gwBot := NewGatewayBot(nil, nil, nil, "tenant-empty-guilds", nil)

	err := bm.AddGatewayBot("tenant-empty-guilds", gwBot)
	if err != nil {
		t.Fatalf("AddGatewayBot failed: %v", err)
	}

	// With empty guild IDs, RouteEventForGuild should allow all guilds.
	called := false
	bm.RouteEventForGuild("tenant-empty-guilds", "any-guild", func(_ *bot.Client) {
		called = true
	})
	if !called {
		t.Error("handler should have been called for empty guild allowlist")
	}
}

func TestAddGatewayBot_GuildFiltering(t *testing.T) {
	t.Parallel()

	bm := NewBotManager()
	guildIDs := []snowflake.ID{snowflake.ID(100), snowflake.ID(200)}
	gwBot := NewGatewayBot(nil, nil, nil, "tenant-filter", guildIDs)

	_ = bm.AddGatewayBot("tenant-filter", gwBot)

	t.Run("allowed guild", func(t *testing.T) {
		t.Parallel()
		called := false
		bm.RouteEventForGuild("tenant-filter", "100", func(_ *bot.Client) {
			called = true
		})
		if !called {
			t.Error("handler should have been called for allowed guild")
		}
	})

	t.Run("blocked guild", func(t *testing.T) {
		t.Parallel()
		called := false
		bm.RouteEventForGuild("tenant-filter", "999", func(_ *bot.Client) {
			called = true
		})
		if called {
			t.Error("handler should NOT have been called for blocked guild")
		}
	})
}

// ── DiscordBotConnector ─────────────────────────────────────────────────────

func TestNewDiscordBotConnector(t *testing.T) {
	t.Parallel()

	mgr := NewBotManager()
	conn := NewDiscordBotConnector(mgr)
	if conn == nil {
		t.Fatal("expected non-nil connector")
	}
	if conn.mgr != mgr {
		t.Error("expected connector to reference the provided BotManager")
	}
}

func TestDiscordBotConnector_SetCommandSetup(t *testing.T) {
	t.Parallel()

	mgr := NewBotManager()
	conn := NewDiscordBotConnector(mgr)

	if conn.commandSetup != nil {
		t.Error("commandSetup should be nil initially")
	}

	called := false
	conn.SetCommandSetup(func(_ *GatewayBot, _ Tenant) {
		called = true
	})

	if conn.commandSetup == nil {
		t.Error("expected commandSetup to be set")
	}

	// Invoke it to verify it was stored correctly.
	conn.commandSetup(nil, Tenant{})
	if !called {
		t.Error("expected commandSetup function to be callable")
	}
}

func TestDiscordBotConnector_DisconnectBot_NoBotRegistered(t *testing.T) {
	t.Parallel()

	mgr := NewBotManager()
	conn := NewDiscordBotConnector(mgr)

	// DisconnectBot for a tenant that has no bot should not panic.
	conn.DisconnectBot("nonexistent-tenant")
}

func TestDiscordBotConnector_DisconnectBot_WithBot(t *testing.T) {
	t.Parallel()

	mgr := NewBotManager()
	conn := NewDiscordBotConnector(mgr)

	// Register a bot with nil client (safe to close).
	_ = mgr.AddBot("tenant-disc", nil, nil)

	conn.DisconnectBot("tenant-disc")

	// Bot should be removed after disconnect.
	if _, ok := mgr.Get("tenant-disc"); ok {
		t.Error("bot should be removed after DisconnectBot")
	}
}

func TestDiscordBotConnector_DisconnectBot_WithPlainBot(t *testing.T) {
	t.Parallel()

	mgr := NewBotManager()
	conn := NewDiscordBotConnector(mgr)

	// Register as plain bot (nil client, safe to close via RemoveBot).
	_ = mgr.AddBot("tenant-disc-plain", nil, []string{"guild-1"})

	conn.DisconnectBot("tenant-disc-plain")

	// Should be removed after disconnect.
	if _, ok := mgr.Get("tenant-disc-plain"); ok {
		t.Error("bot should be removed after DisconnectBot")
	}
}

// ── computeChannelPerms ─────────────────────────────────────────────────────
// computeChannelPerms requires a discord.GuildChannel, which has unexported
// methods (guildChannel(), channel()). We must use a real disgo type created
// via JSON unmarshal.

// newTestVoiceChannel creates a GuildVoiceChannel via JSON unmarshal with the
// given permission overwrites. The overwrites JSON should be a JSON array of
// overwrite objects.
func newTestVoiceChannel(t *testing.T, channelID, guildID snowflake.ID, overwritesJSON string) discord.GuildVoiceChannel {
	t.Helper()
	if overwritesJSON == "" {
		overwritesJSON = "[]"
	}
	raw := `{
		"id": "` + channelID.String() + `",
		"type": 2,
		"guild_id": "` + guildID.String() + `",
		"name": "test-voice",
		"position": 0,
		"permission_overwrites": ` + overwritesJSON + `
	}`
	var ch discord.GuildVoiceChannel
	if err := json.Unmarshal([]byte(raw), &ch); err != nil {
		t.Fatalf("unmarshal voice channel: %v", err)
	}
	return ch
}

func TestComputeChannelPerms_OwnerGetsAll(t *testing.T) {
	t.Parallel()

	guildID := snowflake.ID(1)
	ownerID := snowflake.ID(42)

	guild := &discord.RestGuild{
		Guild: discord.Guild{
			ID:      guildID,
			OwnerID: ownerID,
		},
		Roles: []discord.Role{
			{ID: guildID, Permissions: discord.PermissionsNone},
		},
	}
	member := &discord.Member{
		User: discord.User{ID: ownerID},
	}
	ch := newTestVoiceChannel(t, snowflake.ID(100), guildID, "")

	perms := computeChannelPerms(guild, member, ch)
	if perms != discord.PermissionsAll {
		t.Errorf("owner should have PermissionsAll, got %d", perms)
	}
}

func TestComputeChannelPerms_AdminBypassesOverwrites(t *testing.T) {
	t.Parallel()

	guildID := snowflake.ID(1)
	adminRoleID := snowflake.ID(10)
	botID := snowflake.ID(42)

	guild := &discord.RestGuild{
		Guild: discord.Guild{
			ID:      guildID,
			OwnerID: snowflake.ID(999),
		},
		Roles: []discord.Role{
			{ID: guildID, Permissions: discord.PermissionsNone},
			{ID: adminRoleID, Permissions: discord.PermissionAdministrator},
		},
	}
	member := &discord.Member{
		User:    discord.User{ID: botID},
		RoleIDs: []snowflake.ID{adminRoleID},
	}
	// Channel denies CONNECT for that role, but admin should bypass.
	ch := newTestVoiceChannel(t, snowflake.ID(100), guildID,
		`[{"id": "10", "type": 0, "allow": "0", "deny": "1048576"}]`)

	perms := computeChannelPerms(guild, member, ch)
	if perms != discord.PermissionsAll {
		t.Errorf("administrator should have PermissionsAll, got %d", perms)
	}
}

func TestComputeChannelPerms_BasicRolePerms(t *testing.T) {
	t.Parallel()

	guildID := snowflake.ID(1)
	roleID := snowflake.ID(10)
	botID := snowflake.ID(42)

	guild := &discord.RestGuild{
		Guild: discord.Guild{
			ID:      guildID,
			OwnerID: snowflake.ID(999),
		},
		Roles: []discord.Role{
			{ID: guildID, Permissions: discord.PermissionViewChannel},
			{ID: roleID, Permissions: discord.PermissionConnect | discord.PermissionSpeak},
		},
	}
	member := &discord.Member{
		User:    discord.User{ID: botID},
		RoleIDs: []snowflake.ID{roleID},
	}
	ch := newTestVoiceChannel(t, snowflake.ID(100), guildID, "")

	perms := computeChannelPerms(guild, member, ch)
	if !perms.Has(discord.PermissionConnect) {
		t.Error("expected PermissionConnect")
	}
	if !perms.Has(discord.PermissionSpeak) {
		t.Error("expected PermissionSpeak")
	}
	if !perms.Has(discord.PermissionViewChannel) {
		t.Error("expected PermissionViewChannel from @everyone role")
	}
}

func TestComputeChannelPerms_EveryoneOverwriteDeny(t *testing.T) {
	t.Parallel()

	guildID := snowflake.ID(1)
	botID := snowflake.ID(42)

	guild := &discord.RestGuild{
		Guild: discord.Guild{
			ID:      guildID,
			OwnerID: snowflake.ID(999),
		},
		Roles: []discord.Role{
			{ID: guildID, Permissions: discord.PermissionConnect | discord.PermissionSpeak | discord.PermissionViewChannel},
		},
	}
	member := &discord.Member{
		User: discord.User{ID: botID},
	}
	// @everyone overwrite (id matches guildID) denies CONNECT.
	// PermissionConnect = 1 << 20 = 1048576
	ch := newTestVoiceChannel(t, snowflake.ID(100), guildID,
		`[{"id": "1", "type": 0, "allow": "0", "deny": "1048576"}]`)

	perms := computeChannelPerms(guild, member, ch)
	if perms.Has(discord.PermissionConnect) {
		t.Error("expected CONNECT to be denied by @everyone overwrite")
	}
	if !perms.Has(discord.PermissionSpeak) {
		t.Error("expected SPEAK to be kept")
	}
}

func TestComputeChannelPerms_RoleOverwriteAllowOverridesEveryoneDeny(t *testing.T) {
	t.Parallel()

	guildID := snowflake.ID(1)
	roleID := snowflake.ID(10)
	botID := snowflake.ID(42)

	guild := &discord.RestGuild{
		Guild: discord.Guild{
			ID:      guildID,
			OwnerID: snowflake.ID(999),
		},
		Roles: []discord.Role{
			{ID: guildID, Permissions: discord.PermissionViewChannel | discord.PermissionConnect | discord.PermissionSpeak},
			{ID: roleID, Permissions: discord.PermissionsNone},
		},
	}
	member := &discord.Member{
		User:    discord.User{ID: botID},
		RoleIDs: []snowflake.ID{roleID},
	}
	// @everyone overwrite denies CONNECT, but specific role re-allows it.
	ch := newTestVoiceChannel(t, snowflake.ID(100), guildID,
		`[{"id": "1", "type": 0, "allow": "0", "deny": "1048576"},
		  {"id": "10", "type": 0, "allow": "1048576", "deny": "0"}]`)

	perms := computeChannelPerms(guild, member, ch)
	if !perms.Has(discord.PermissionConnect) {
		t.Error("expected CONNECT to be re-allowed by role overwrite")
	}
}

func TestComputeChannelPerms_MemberOverwriteDeny(t *testing.T) {
	t.Parallel()

	guildID := snowflake.ID(1)
	botID := snowflake.ID(42)

	guild := &discord.RestGuild{
		Guild: discord.Guild{
			ID:      guildID,
			OwnerID: snowflake.ID(999),
		},
		Roles: []discord.Role{
			{ID: guildID, Permissions: discord.PermissionViewChannel | discord.PermissionConnect | discord.PermissionSpeak},
		},
	}
	member := &discord.Member{
		User: discord.User{ID: botID},
	}
	// Member-level overwrite (type 1) denies SPEAK specifically for this bot.
	// PermissionSpeak = 1 << 21 = 2097152
	ch := newTestVoiceChannel(t, snowflake.ID(100), guildID,
		`[{"id": "42", "type": 1, "allow": "0", "deny": "2097152"}]`)

	perms := computeChannelPerms(guild, member, ch)
	if perms.Has(discord.PermissionSpeak) {
		t.Error("expected SPEAK to be denied by member overwrite")
	}
	if !perms.Has(discord.PermissionConnect) {
		t.Error("expected CONNECT to be kept")
	}
}

func TestComputeChannelPerms_MemberOverwriteAllow(t *testing.T) {
	t.Parallel()

	guildID := snowflake.ID(1)
	botID := snowflake.ID(42)

	guild := &discord.RestGuild{
		Guild: discord.Guild{
			ID:      guildID,
			OwnerID: snowflake.ID(999),
		},
		Roles: []discord.Role{
			{ID: guildID, Permissions: discord.PermissionViewChannel},
		},
	}
	member := &discord.Member{
		User: discord.User{ID: botID},
	}
	// @everyone denies CONNECT, member overwrite re-allows CONNECT and SPEAK.
	// PermissionConnect | PermissionSpeak = 1048576 + 2097152 = 3145728
	ch := newTestVoiceChannel(t, snowflake.ID(100), guildID,
		`[{"id": "1", "type": 0, "allow": "0", "deny": "1048576"},
		  {"id": "42", "type": 1, "allow": "3145728", "deny": "0"}]`)

	perms := computeChannelPerms(guild, member, ch)
	if !perms.Has(discord.PermissionConnect) {
		t.Error("expected CONNECT to be allowed by member overwrite")
	}
	if !perms.Has(discord.PermissionSpeak) {
		t.Error("expected SPEAK to be allowed by member overwrite")
	}
}

func TestComputeChannelPerms_NoRoles(t *testing.T) {
	t.Parallel()

	guildID := snowflake.ID(1)
	botID := snowflake.ID(42)

	guild := &discord.RestGuild{
		Guild: discord.Guild{
			ID:      guildID,
			OwnerID: snowflake.ID(999),
		},
		Roles: []discord.Role{
			{ID: guildID, Permissions: discord.PermissionConnect | discord.PermissionSpeak},
		},
	}
	member := &discord.Member{
		User: discord.User{ID: botID},
	}
	ch := newTestVoiceChannel(t, snowflake.ID(100), guildID, "")

	perms := computeChannelPerms(guild, member, ch)
	if !perms.Has(discord.PermissionConnect) {
		t.Error("expected CONNECT from @everyone")
	}
	if !perms.Has(discord.PermissionSpeak) {
		t.Error("expected SPEAK from @everyone")
	}
}

func TestComputeChannelPerms_MultipleRoles(t *testing.T) {
	t.Parallel()

	guildID := snowflake.ID(1)
	role1 := snowflake.ID(10)
	role2 := snowflake.ID(20)
	botID := snowflake.ID(42)

	guild := &discord.RestGuild{
		Guild: discord.Guild{
			ID:      guildID,
			OwnerID: snowflake.ID(999),
		},
		Roles: []discord.Role{
			{ID: guildID, Permissions: discord.PermissionsNone},
			{ID: role1, Permissions: discord.PermissionConnect},
			{ID: role2, Permissions: discord.PermissionSpeak},
		},
	}
	member := &discord.Member{
		User:    discord.User{ID: botID},
		RoleIDs: []snowflake.ID{role1, role2},
	}
	ch := newTestVoiceChannel(t, snowflake.ID(100), guildID, "")

	perms := computeChannelPerms(guild, member, ch)
	if !perms.Has(discord.PermissionConnect) {
		t.Error("expected CONNECT from role1")
	}
	if !perms.Has(discord.PermissionSpeak) {
		t.Error("expected SPEAK from role2")
	}
}

// ── setupVoiceBridge ────────────────────────────────────────────────────────

// mockVoiceConn implements voice.Conn for testing setupVoiceBridge.
type mockVoiceConn struct {
	receiver voice.OpusFrameReceiver
	provider voice.OpusFrameProvider
	guildID  snowflake.ID
	closed   bool
}

func (m *mockVoiceConn) Gateway() voice.Gateway                                     { return nil }
func (m *mockVoiceConn) UDP() voice.UDPConn                                         { return nil }
func (m *mockVoiceConn) ChannelID() *snowflake.ID                                   { return nil }
func (m *mockVoiceConn) GuildID() snowflake.ID                                      { return m.guildID }
func (m *mockVoiceConn) UserIDBySSRC(_ uint32) snowflake.ID                         { return 0 }
func (m *mockVoiceConn) SetSpeaking(_ context.Context, _ voice.SpeakingFlags) error { return nil }
func (m *mockVoiceConn) SetOpusFrameProvider(p voice.OpusFrameProvider)             { m.provider = p }
func (m *mockVoiceConn) SetOpusFrameReceiver(r voice.OpusFrameReceiver)             { m.receiver = r }
func (m *mockVoiceConn) SetEventHandlerFunc(_ voice.EventHandlerFunc)               {}
func (m *mockVoiceConn) Open(_ context.Context, _ snowflake.ID, _, _ bool) error    { return nil }
func (m *mockVoiceConn) Close(_ context.Context)                                    { m.closed = true }
func (m *mockVoiceConn) HandleVoiceStateUpdate(_ disgoGateway.EventVoiceStateUpdate) {
}
func (m *mockVoiceConn) HandleVoiceServerUpdate(_ disgoGateway.EventVoiceServerUpdate) {
}

func TestSetupVoiceBridge_WiresReceiverAndProvider(t *testing.T) {
	t.Parallel()

	srv := audiobridge.NewServer()
	bridge := srv.NewSessionBridge("sess-setup")
	defer srv.RemoveBridge("sess-setup")

	conn := &mockVoiceConn{guildID: snowflake.ID(123)}

	cleanup := setupVoiceBridge(conn, bridge, "sess-setup", 0)

	// Receiver and provider should be set on the mock conn.
	if conn.receiver == nil {
		t.Error("expected receiver to be set")
	}
	if conn.provider == nil {
		t.Error("expected provider to be set")
	}

	// Cleanup should close the conn.
	cleanup()
	if !conn.closed {
		t.Error("expected voice conn to be closed after cleanup")
	}
}

func TestSetupVoiceBridge_CleanupIdempotent(t *testing.T) {
	t.Parallel()

	srv := audiobridge.NewServer()
	bridge := srv.NewSessionBridge("sess-cleanup-idem")
	defer srv.RemoveBridge("sess-cleanup-idem")

	conn := &mockVoiceConn{guildID: snowflake.ID(456)}

	cleanup := setupVoiceBridge(conn, bridge, "sess-cleanup-idem", 0)

	// Should not panic when called multiple times.
	cleanup()
	cleanup()

	if !conn.closed {
		t.Error("expected conn to be closed")
	}
}

// ── voiceBridgeReceiver self-hearing guard ──────────────────────────────────

// TestVoiceBridgeReceiver_SelfHearingGuard verifies that frames from the bot's
// own user ID are silently dropped at the gateway layer.
func TestVoiceBridgeReceiver_SelfHearingGuard(t *testing.T) {
	t.Parallel()

	srv := audiobridge.NewServer()
	bridge := srv.NewSessionBridge("sess-guard")
	defer srv.RemoveBridge("sess-guard")

	botID := snowflake.ID(42)

	receiver := &voiceBridgeReceiver{
		bridge:    bridge,
		sessionID: "sess-guard",
		botUserID: botID,
		done:      make(chan struct{}),
	}

	opus := []byte{0xF8, 0xFF, 0xFE}

	// Frame from the bot's own user ID should be dropped.
	if err := receiver.ReceiveOpusFrame(botID, &voice.Packet{SSRC: 1, Opus: opus}); err != nil {
		t.Fatalf("ReceiveOpusFrame(botID): %v", err)
	}

	// Frame from a player should be forwarded.
	playerID := snowflake.ID(123)
	if err := receiver.ReceiveOpusFrame(playerID, &voice.Packet{SSRC: 2, Opus: opus}); err != nil {
		t.Fatalf("ReceiveOpusFrame(playerID): %v", err)
	}

	// Only the player frame should arrive on the toWorker channel.
	select {
	case frame := <-bridge.ReceiveFromWorker():
		// This reads from fromWorker, but we sent to toWorker. Let's read toWorker directly.
		_ = frame
	default:
	}

	// Verify bridge is still alive (not closed unexpectedly).
	select {
	case <-bridge.Done():
		t.Fatal("bridge closed unexpectedly")
	default:
	}

	// The receiver's frame counter should be 1 (only the player frame counted).
	if got := receiver.frameCount.Load(); got != 1 {
		t.Errorf("frameCount: got %d, want 1 (only player frame should be counted)", got)
	}
}

// TestVoiceBridgeReceiver_SelfHearingGuardInactive verifies that when no bot
// user ID is set (zero value), all frames pass through.
func TestVoiceBridgeReceiver_SelfHearingGuardInactive(t *testing.T) {
	t.Parallel()

	srv := audiobridge.NewServer()
	bridge := srv.NewSessionBridge("sess-guard-off")
	defer srv.RemoveBridge("sess-guard-off")

	receiver := &voiceBridgeReceiver{
		bridge:    bridge,
		sessionID: "sess-guard-off",
		// botUserID is zero — guard should be inactive.
		done: make(chan struct{}),
	}

	opus := []byte{0xF8, 0xFF, 0xFE}
	userID := snowflake.ID(42)

	if err := receiver.ReceiveOpusFrame(userID, &voice.Packet{SSRC: 1, Opus: opus}); err != nil {
		t.Fatalf("ReceiveOpusFrame: %v", err)
	}

	if got := receiver.frameCount.Load(); got != 1 {
		t.Errorf("frameCount: got %d, want 1", got)
	}
}

// TestSetupVoiceBridge_BotUserIDPassedToReceiver verifies that
// setupVoiceBridge wires the botUserID into the receiver.
func TestSetupVoiceBridge_BotUserIDPassedToReceiver(t *testing.T) {
	t.Parallel()

	srv := audiobridge.NewServer()
	bridge := srv.NewSessionBridge("sess-guard-wire")
	defer srv.RemoveBridge("sess-guard-wire")

	conn := &mockVoiceConn{guildID: snowflake.ID(789)}
	botID := snowflake.ID(42)

	cleanup := setupVoiceBridge(conn, bridge, "sess-guard-wire", botID)
	defer cleanup()

	// The receiver should have botUserID set. Verify by sending a frame from
	// the bot's user ID and checking it doesn't increment the frame counter.
	receiver, ok := conn.receiver.(*voiceBridgeReceiver)
	if !ok {
		t.Fatal("receiver is not a *voiceBridgeReceiver")
	}

	opus := []byte{0xF8, 0xFF, 0xFE}
	_ = receiver.ReceiveOpusFrame(botID, &voice.Packet{SSRC: 1, Opus: opus})

	if got := receiver.frameCount.Load(); got != 0 {
		t.Errorf("frameCount after bot frame: got %d, want 0", got)
	}

	// A non-bot frame should be counted.
	_ = receiver.ReceiveOpusFrame(snowflake.ID(999), &voice.Packet{SSRC: 2, Opus: opus})
	if got := receiver.frameCount.Load(); got != 1 {
		t.Errorf("frameCount after player frame: got %d, want 1", got)
	}
}

// ── BotManager.Close with mixed entries ─────────────────────────────────────

func TestBotManager_Close_WithMixedEntries(t *testing.T) {
	t.Parallel()

	bm := NewBotManager()
	// Only use AddBot with nil client (safe to close).
	// GatewayBot.Close requires a non-nil client so we skip that combination here.
	_ = bm.AddBot("tenant-close-1", nil, nil)
	_ = bm.AddBot("tenant-close-2", nil, []string{"guild-1"})

	bm.Close()

	if _, ok := bm.Get("tenant-close-1"); ok {
		t.Error("tenant-close-1 should be removed after Close")
	}
	if _, ok := bm.Get("tenant-close-2"); ok {
		t.Error("tenant-close-2 should be removed after Close")
	}
}

// ── BotManager.AddGatewayBot then Get ───────────────────────────────────────

func TestAddGatewayBot_GetViaBothMethods(t *testing.T) {
	t.Parallel()

	bm := NewBotManager()
	gwBot := NewGatewayBot(nil, nil, nil, "tenant-both", nil)
	_ = bm.AddGatewayBot("tenant-both", gwBot)

	// Get should return nil client (since GatewayBot was created with nil client).
	client, ok := bm.Get("tenant-both")
	if !ok {
		t.Fatal("expected Get to find the bot")
	}
	if client != nil {
		t.Error("expected nil client from Get")
	}

	// GetBot should return the GatewayBot.
	gotBot, ok := bm.GetBot("tenant-both")
	if !ok {
		t.Fatal("expected GetBot to find the bot")
	}
	if gotBot != gwBot {
		t.Error("GetBot returned a different GatewayBot")
	}
}

// ── GatewayBot.RegisterCommands / UnregisterCommands with no guilds ─────────

func TestGatewayBot_RegisterCommands_NoGuilds(t *testing.T) {
	t.Parallel()

	// Need a real router since RegisterCommands calls router.ApplicationCommands().
	router := discordbot.NewCommandRouter()
	gwBot := NewGatewayBot(nil, router, nil, "tenant-no-guilds", nil)

	// With no guild IDs, the loop body is never entered — should be a no-op.
	err := gwBot.RegisterCommands(context.Background())
	if err != nil {
		t.Errorf("RegisterCommands with no guilds returned error: %v", err)
	}
}

func TestGatewayBot_UnregisterCommands_NoGuilds(t *testing.T) {
	t.Parallel()

	gwBot := NewGatewayBot(nil, nil, nil, "tenant-no-guilds-unreg", nil)
	// With no guild IDs, the loop body is never entered — should be a no-op.
	gwBot.UnregisterCommands()
}

// ── DiscordBotConnector.ConnectBot delegation ───────────────────────────────

func TestDiscordBotConnector_ConnectBot_EmptyToken(t *testing.T) {
	t.Parallel()

	mgr := NewBotManager()
	conn := NewDiscordBotConnector(mgr)

	err := conn.ConnectBot(context.Background(), "t1", "", nil)
	// With an empty token, disgo.New should fail.
	if err == nil {
		t.Error("expected error with empty bot token")
	}
}

func TestDiscordBotConnector_ConnectBot_InvalidToken(t *testing.T) {
	t.Parallel()

	mgr := NewBotManager()
	conn := NewDiscordBotConnector(mgr)

	// disgo.New with a bogus token may succeed (it's just a string),
	// but OpenGateway will fail. Either way we exercise the code path.
	err := conn.ConnectBot(context.Background(), "t1", "invalid-token", []string{"123"})
	// ConnectBotForTenant calls disgo.New then OpenGateway — one of them should fail.
	if err == nil {
		// If it somehow didn't fail (unlikely), clean up.
		_ = mgr.RemoveBot("t1")
	}
}

func TestDiscordBotConnector_ConnectBot_ReplacesExistingBot(t *testing.T) {
	t.Parallel()

	mgr := NewBotManager()
	conn := NewDiscordBotConnector(mgr)

	// Pre-register a bot with nil client.
	_ = mgr.AddBot("t1", nil, nil)

	// ConnectBotForTenant calls mgr.RemoveBot first, which removes the old entry.
	// Then it tries to create a new client and open gateway, which will fail.
	err := conn.ConnectBot(context.Background(), "t1", "", nil)
	if err == nil {
		t.Error("expected error with empty token")
	}

	// The old bot should have been removed.
	if _, ok := mgr.Get("t1"); ok {
		t.Error("old bot should have been removed during ConnectBot")
	}
}

// ── computeChannelPerms with real GuildVoiceChannel ─────────────────────────

func TestComputeChannelPerms_WithUnmarshaledChannel(t *testing.T) {
	t.Parallel()

	guildID := snowflake.ID(1)
	channelID := snowflake.ID(100)
	roleID := snowflake.ID(10)
	botID := snowflake.ID(42)

	ch := newTestVoiceChannel(t, channelID, guildID,
		`[{"id": "1", "type": 0, "allow": "0", "deny": "1048576"},
		  {"id": "10", "type": 0, "allow": "1048576", "deny": "0"}]`)

	if ch.ID() != channelID {
		t.Fatalf("channel ID = %v, want %v", ch.ID(), channelID)
	}

	guild := &discord.RestGuild{
		Guild: discord.Guild{
			ID:      guildID,
			OwnerID: snowflake.ID(999),
		},
		Roles: []discord.Role{
			{ID: guildID, Permissions: discord.PermissionViewChannel | discord.PermissionConnect | discord.PermissionSpeak},
			{ID: roleID, Permissions: discord.PermissionsNone},
		},
	}
	member := &discord.Member{
		User:    discord.User{ID: botID},
		RoleIDs: []snowflake.ID{roleID},
	}

	perms := computeChannelPerms(guild, member, ch)

	// @everyone denies CONNECT (1<<20 = 1048576), role 10 re-allows it.
	if !perms.Has(discord.PermissionConnect) {
		t.Error("expected CONNECT to be re-allowed by role overwrite")
	}
}
