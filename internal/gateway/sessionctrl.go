package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"

	"github.com/MrWong99/glyphoxa/internal/agent/npcstore"
	"github.com/MrWong99/glyphoxa/internal/config"
	"github.com/MrWong99/glyphoxa/internal/gateway/audiobridge"
	"github.com/MrWong99/glyphoxa/internal/gateway/dispatch"
)

// Compile-time interface assertions.
var (
	_ SessionController = (*GatewaySessionController)(nil)
	_ NPCController     = (*GatewaySessionController)(nil)
)

// SessionOrchestrator is the subset of sessionorch.Orchestrator needed by
// GatewaySessionController. Defined here to avoid an import cycle between
// gateway and sessionorch.
type SessionOrchestrator interface {
	ValidateAndCreate(ctx context.Context, tenantID, campaignID, guildID, channelID string, tier config.LicenseTier) (string, error)
	Transition(ctx context.Context, sessionID string, state SessionState, errMsg string) error
	GetSessionInfo(ctx context.Context, sessionID string) (SessionInfo, error)
	ListActiveSessionIDs(ctx context.Context, tenantID string) ([]string, error)
}

// GatewaySessionController implements [SessionController] for gateway mode
// by composing a [SessionOrchestrator] with a K8s [dispatch.Dispatcher].
//
// All methods are safe for concurrent use.
type GatewaySessionController struct {
	orch       SessionOrchestrator
	dispatcher *dispatch.Dispatcher
	tenantID   string
	campaignID string
	tier       config.LicenseTier
	botToken   string
	npcConfigs []NPCConfigMsg
	npcStore   npcstore.Store
	dialer     WorkerDialer
	gwBot      *GatewayBot // for voice channel management
	bridgeSrv  *audiobridge.Server

	mu          sync.Mutex
	active      map[string]string // guildID -> sessionID
	workerAddrs map[string]string // sessionID -> worker gRPC address

	// voiceCleanups stores cleanup functions for voice bridges, keyed by sessionID.
	voiceCleanupsMu sync.Mutex
	voiceCleanups   map[string]func()
}

// NewGatewaySessionController creates a GatewaySessionController.
func NewGatewaySessionController(
	orch SessionOrchestrator,
	dispatcher *dispatch.Dispatcher,
	tenantID, campaignID string,
	tier config.LicenseTier,
	opts ...SessionControllerOption,
) *GatewaySessionController {
	gc := &GatewaySessionController{
		orch:          orch,
		dispatcher:    dispatcher,
		tenantID:      tenantID,
		campaignID:    campaignID,
		tier:          tier,
		active:        make(map[string]string),
		workerAddrs:   make(map[string]string),
		voiceCleanups: make(map[string]func()),
	}
	for _, opt := range opts {
		opt(gc)
	}

	// Register audio bridge detach callback so worker death triggers
	// immediate session cleanup instead of waiting for heartbeat timeout.
	if gc.bridgeSrv != nil {
		gc.bridgeSrv.SetOnStreamDetach(func(sessionID string, err error) {
			gc.mu.Lock()
			_, owns := gc.workerAddrs[sessionID]
			gc.mu.Unlock()
			if !owns {
				return
			}
			slog.Warn("gateway: audio stream lost, stopping session",
				"session_id", sessionID, "err", err)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if stopErr := gc.Stop(ctx, sessionID); stopErr != nil {
					slog.Error("gateway: failed to stop session after stream loss",
						"session_id", sessionID, "err", stopErr)
				}
			}()
		})
	}

	return gc
}

// SessionControllerOption configures a GatewaySessionController.
type SessionControllerOption func(*GatewaySessionController)

// WithBotToken sets the Discord bot token for worker voice connections.
func WithBotToken(token string) SessionControllerOption {
	return func(gc *GatewaySessionController) { gc.botToken = token }
}

// WithNPCConfigs sets the NPC configurations sent to workers.
func WithNPCConfigs(configs []NPCConfigMsg) SessionControllerOption {
	return func(gc *GatewaySessionController) { gc.npcConfigs = configs }
}

// WorkerDialer creates a WorkerClient connected to the given address.
// It is injected from cmd/glyphoxa to avoid import cycles with grpctransport.
type WorkerDialer func(addr string) (WorkerClient, error)

// WithWorkerDialer sets the function used to create gRPC connections to workers.
func WithWorkerDialer(d WorkerDialer) SessionControllerOption {
	return func(gc *GatewaySessionController) { gc.dialer = d }
}

// WithGatewayBot sets the GatewayBot used for voice channel management.
func WithGatewayBot(gwBot *GatewayBot) SessionControllerOption {
	return func(gc *GatewaySessionController) { gc.gwBot = gwBot }
}

// WithAudioBridgeServer sets the audio bridge gRPC server for voice streaming.
func WithAudioBridgeServer(srv *audiobridge.Server) SessionControllerOption {
	return func(gc *GatewaySessionController) { gc.bridgeSrv = srv }
}

// WithNPCStore sets the NPC store used to load NPC definitions from the
// database at session start. When set, NPCs are loaded fresh from the DB for
// the session's campaign instead of using the static npcConfigs snapshot.
func WithNPCStore(store npcstore.Store) SessionControllerOption {
	return func(gc *GatewaySessionController) { gc.npcStore = store }
}

// Start begins a new voice session.
func (gc *GatewaySessionController) Start(ctx context.Context, req SessionStartRequest) error {
	gc.mu.Lock()
	if _, ok := gc.active[req.GuildID]; ok {
		gc.mu.Unlock()
		return fmt.Errorf("gateway: session already active for guild %s", req.GuildID)
	}
	gc.mu.Unlock()

	// Use the request's CampaignID (from web or Discord) with fallback to
	// the tenant's default campaign.
	campaignID := req.CampaignID
	if campaignID == "" {
		campaignID = gc.campaignID
	}

	sessionID, err := gc.orch.ValidateAndCreate(ctx, gc.tenantID, campaignID, req.GuildID, req.ChannelID, gc.tier)
	if err != nil {
		return fmt.Errorf("gateway: validate session: %w", err)
	}

	// Load NPC definitions fresh from the database when an npcStore is
	// configured. This ensures the session uses the latest NPCs for the
	// campaign, not a stale startup snapshot.
	npcConfigs := gc.npcConfigs
	if gc.npcStore != nil && campaignID != "" {
		defs, listErr := gc.npcStore.List(ctx, campaignID)
		if listErr != nil {
			_ = gc.orch.Transition(ctx, sessionID, SessionEnded, listErr.Error())
			return fmt.Errorf("gateway: load NPCs for campaign %q: %w", campaignID, listErr)
		}
		if len(defs) > 0 {
			npcConfigs = npcDefsToConfigs(defs)
		}
	}

	if gc.dispatcher != nil {
		startReq := StartSessionRequest{
			SessionID:   sessionID,
			TenantID:    gc.tenantID,
			CampaignID:  campaignID,
			GuildID:     req.GuildID,
			ChannelID:   req.ChannelID,
			LicenseTier: gc.tier.String(),
			BotToken:    gc.botToken,
			NPCConfigs:  npcConfigs,
		}

		// Join voice via the gateway bot's VoiceManager and set up the audio
		// bridge so opus frames flow between Discord and the worker over gRPC.
		if gc.gwBot != nil && gc.bridgeSrv != nil {
			bridge := gc.bridgeSrv.NewSessionBridge(sessionID)

			gID, parseErr := snowflake.Parse(req.GuildID)
			if parseErr != nil {
				gc.bridgeSrv.RemoveBridge(sessionID)
				_ = gc.orch.Transition(ctx, sessionID, SessionEnded, parseErr.Error())
				return fmt.Errorf("gateway: parse guild ID %q: %w", req.GuildID, parseErr)
			}
			chID, parseErr := snowflake.Parse(req.ChannelID)
			if parseErr != nil {
				gc.bridgeSrv.RemoveBridge(sessionID)
				_ = gc.orch.Transition(ctx, sessionID, SessionEnded, parseErr.Error())
				return fmt.Errorf("gateway: parse channel ID %q: %w", req.ChannelID, parseErr)
			}

			voiceMgr := gc.gwBot.Client().VoiceManager
			if voiceMgr == nil {
				gc.bridgeSrv.RemoveBridge(sessionID)
				_ = gc.orch.Transition(ctx, sessionID, SessionEnded, "VoiceManager not available")
				return fmt.Errorf("gateway: VoiceManager not available on gateway bot")
			}

			// Check bot has CONNECT + SPEAK permissions before attempting to join.
			// Discord silently ignores Op 4 without CONNECT, causing a confusing timeout.
			if permErr := checkVoicePermissions(ctx, gc.gwBot.Client().Rest, gID, chID, gc.gwBot.Client().ApplicationID); permErr != nil {
				gc.bridgeSrv.RemoveBridge(sessionID)
				_ = gc.orch.Transition(ctx, sessionID, SessionEnded, permErr.Error())
				return fmt.Errorf("gateway: voice permission check: %w", permErr)
			}

			voiceConn := voiceMgr.CreateConn(gID)
			if joinErr := voiceConn.Open(ctx, chID, false, false); joinErr != nil {
				closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
				voiceConn.Close(closeCtx)
				closeCancel()
				voiceMgr.RemoveConn(gID)
				gc.bridgeSrv.RemoveBridge(sessionID)
				_ = gc.orch.Transition(ctx, sessionID, SessionEnded, joinErr.Error())
				return fmt.Errorf("gateway: join voice channel: %w", joinErr)
			}

			slog.Info("gateway: joined voice channel",
				"session_id", sessionID,
				"guild_id", req.GuildID,
				"channel_id", req.ChannelID,
			)

			// Wire the audio bridge immediately. DAVE E2EE handshake
			// completes asynchronously inside disgo — the
			// OpusFrameReceiver callback receives already-decrypted
			// packets, so there is no need to block here waiting for a
			// DAVE event. This matches the full-mode code path which
			// also starts flowing audio right after Open().
			cleanup := setupVoiceBridge(voiceConn, bridge, sessionID)
			gc.voiceCleanupsMu.Lock()
			gc.voiceCleanups[sessionID] = func() {
				cleanup()
				voiceMgr.RemoveConn(gID)
			}
			gc.voiceCleanupsMu.Unlock()

			// Listen for external disconnection (admin kicks the bot).
			gc.registerDisconnectListener(sessionID, req.GuildID)
		}

		starter := func(callCtx context.Context, addr string) error {
			if gc.dialer == nil {
				return fmt.Errorf("gateway: no worker dialer configured")
			}
			client, err := gc.dialer(addr)
			if err != nil {
				return fmt.Errorf("dial worker gRPC at %s: %w", addr, err)
			}
			if err := client.StartSession(callCtx, startReq); err != nil {
				return fmt.Errorf("StartSession RPC: %w", err)
			}
			return nil
		}
		result, dispErr := gc.dispatcher.Dispatch(ctx, sessionID, gc.tenantID, starter)
		if dispErr != nil {
			gc.cleanupVoiceBridge(sessionID)
			if transErr := gc.orch.Transition(ctx, sessionID, SessionEnded, dispErr.Error()); transErr != nil {
				slog.Error("gateway: failed to transition session after dispatch failure",
					"session_id", sessionID, "err", transErr)
			}
			return fmt.Errorf("gateway: dispatch worker: %w", dispErr)
		}

		gc.mu.Lock()
		gc.workerAddrs[sessionID] = result.Address
		gc.mu.Unlock()

		slog.Info("gateway: worker dispatched",
			"session_id", sessionID,
			"tenant_id", gc.tenantID,
			"address", result.Address,
		)
	}

	gc.mu.Lock()
	gc.active[req.GuildID] = sessionID
	gc.mu.Unlock()
	return nil
}

// Stop gracefully ends the session. It first sends a StopSession RPC to the
// worker so it can flush transcripts and cleanly shut down before the K8s Job
// is deleted.
func (gc *GatewaySessionController) Stop(ctx context.Context, sessionID string) error {
	// 1. Tell the worker to stop gracefully (flush transcripts, etc.).
	gc.mu.Lock()
	addr := gc.workerAddrs[sessionID]
	gc.mu.Unlock()
	if addr != "" && gc.dialer != nil {
		rpcCtx, rpcCancel := context.WithTimeout(ctx, 5*time.Second)
		client, dialErr := gc.dialer(addr)
		if dialErr == nil {
			if stopErr := client.StopSession(rpcCtx, sessionID); stopErr != nil {
				slog.Warn("gateway: worker StopSession RPC failed (may already be dead)",
					"session_id", sessionID, "err", stopErr)
			}
			if c, ok := client.(interface{ Close() error }); ok {
				if closeErr := c.Close(); closeErr != nil {
					slog.Debug("gateway: close worker client error", "session_id", sessionID, "err", closeErr)
				}
			}
		}
		rpcCancel()
	}

	// 2. Delete the K8s Job.
	if gc.dispatcher != nil {
		if err := gc.dispatcher.Stop(ctx, sessionID); err != nil {
			slog.Warn("gateway: dispatcher stop error",
				"session_id", sessionID, "err", err)
		}
	}

	// 3. Transition session state in the DB.
	if err := gc.orch.Transition(ctx, sessionID, SessionEnded, ""); err != nil {
		return fmt.Errorf("gateway: transition session to ended: %w", err)
	}

	// 4. Clean up in-memory state and voice bridge.
	gc.mu.Lock()
	delete(gc.workerAddrs, sessionID)
	for guildID, sid := range gc.active {
		if sid == sessionID {
			delete(gc.active, guildID)
			break
		}
	}
	gc.mu.Unlock()

	gc.cleanupVoiceBridge(sessionID)
	return nil
}

// IsActive reports whether a session is running for the guild.
func (gc *GatewaySessionController) IsActive(guildID string) bool {
	gc.mu.Lock()
	_, ok := gc.active[guildID]
	gc.mu.Unlock()
	return ok
}

// Info returns metadata about the active session for the guild.
func (gc *GatewaySessionController) Info(guildID string) (SessionInfo, bool) {
	gc.mu.Lock()
	sessionID, ok := gc.active[guildID]
	gc.mu.Unlock()

	if !ok {
		return SessionInfo{}, false
	}

	info, err := gc.orch.GetSessionInfo(context.Background(), sessionID)
	if err != nil {
		slog.Warn("gateway: failed to get session info",
			"session_id", sessionID, "err", err)
		return SessionInfo{
			SessionID: sessionID,
			GuildID:   guildID,
			StartedAt: time.Time{},
		}, true
	}
	return info, true
}

// StopAll gracefully stops all active sessions. Used during gateway shutdown
// to drain sessions before the process exits.
func (gc *GatewaySessionController) StopAll(ctx context.Context) {
	gc.mu.Lock()
	// Snapshot session IDs so we don't hold the lock while stopping.
	sessionIDs := make([]string, 0, len(gc.active))
	for _, sid := range gc.active {
		sessionIDs = append(sessionIDs, sid)
	}
	gc.mu.Unlock()

	for _, sid := range sessionIDs {
		if err := gc.Stop(ctx, sid); err != nil {
			slog.Warn("gateway: failed to stop session during shutdown",
				"session_id", sid, "err", err)
		}
	}
}

// cleanupVoiceBridge tears down the voice bridge and audio bridge for a session.
func (gc *GatewaySessionController) cleanupVoiceBridge(sessionID string) {
	gc.voiceCleanupsMu.Lock()
	cleanup, ok := gc.voiceCleanups[sessionID]
	if ok {
		delete(gc.voiceCleanups, sessionID)
	}
	gc.voiceCleanupsMu.Unlock()

	if ok {
		cleanup()
	}

	if gc.bridgeSrv != nil {
		gc.bridgeSrv.RemoveBridge(sessionID)
	}
}

// registerDisconnectListener watches for the bot being kicked from the voice
// channel and stops the session if that happens.
func (gc *GatewaySessionController) registerDisconnectListener(sessionID, guildID string) {
	gID, err := snowflake.Parse(guildID)
	if err != nil {
		slog.Warn("gateway: invalid guild ID for disconnect listener", "guild_id", guildID, "err", err)
		return
	}

	listener := bot.NewListenerFunc(func(e *events.GuildVoiceStateUpdate) {
		if e.VoiceState.GuildID != gID || e.VoiceState.UserID != gc.gwBot.Client().ApplicationID {
			return
		}
		if e.VoiceState.ChannelID == nil {
			slog.Info("gateway: bot disconnected from voice externally",
				"session_id", sessionID, "guild_id", guildID)
			go func() {
				if err := gc.Stop(context.Background(), sessionID); err != nil {
					slog.Error("gateway: failed to stop session after voice disconnect",
						"session_id", sessionID, "err", err)
				}
			}()
		}
	})

	gc.gwBot.Client().AddEventListeners(listener)

	// Store removal function alongside the voice cleanup.
	gc.voiceCleanupsMu.Lock()
	prev := gc.voiceCleanups[sessionID]
	gc.voiceCleanups[sessionID] = func() {
		gc.gwBot.Client().RemoveEventListeners(listener)
		if prev != nil {
			prev()
		}
	}
	gc.voiceCleanupsMu.Unlock()
}

// ── NPCController implementation ────────────────────────────────────────────
// GatewaySessionController proxies NPC operations to the worker that owns the
// session. It dials the worker on each call; connections are lightweight and
// NPC commands are infrequent (slash command interactions).

// dialNPCController dials the worker for the given session and returns an
// NPCController plus a cleanup function that closes the underlying connection.
// The caller must call the returned cleanup function when done.
func (gc *GatewaySessionController) dialNPCController(sessionID string) (NPCController, func(), error) {
	gc.mu.Lock()
	addr, ok := gc.workerAddrs[sessionID]
	gc.mu.Unlock()
	if !ok {
		return nil, nil, fmt.Errorf("gateway: no worker address for session %s", sessionID)
	}
	if gc.dialer == nil {
		return nil, nil, fmt.Errorf("gateway: no worker dialer configured")
	}
	client, err := gc.dialer(addr)
	if err != nil {
		return nil, nil, fmt.Errorf("gateway: dial worker at %s: %w", addr, err)
	}
	npcCtrl, ok := client.(NPCController)
	if !ok {
		if c, ok := client.(interface{ Close() error }); ok {
			if closeErr := c.Close(); closeErr != nil {
				slog.Debug("gateway: close worker client error", "err", closeErr)
			}
		}
		return nil, nil, fmt.Errorf("gateway: worker client does not implement NPCController")
	}
	cleanup := func() {
		if c, ok := client.(interface{ Close() error }); ok {
			if closeErr := c.Close(); closeErr != nil {
				slog.Debug("gateway: close worker client error", "err", closeErr)
			}
		}
	}
	return npcCtrl, cleanup, nil
}

// ListNPCs implements [NPCController].
func (gc *GatewaySessionController) ListNPCs(ctx context.Context, sessionID string) ([]NPCStatus, error) {
	ctrl, cleanup, err := gc.dialNPCController(sessionID)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return ctrl.ListNPCs(ctx, sessionID)
}

// MuteNPC implements [NPCController].
func (gc *GatewaySessionController) MuteNPC(ctx context.Context, sessionID, npcName string) error {
	ctrl, cleanup, err := gc.dialNPCController(sessionID)
	if err != nil {
		return fmt.Errorf("gateway: mute NPC %q: %w", npcName, err)
	}
	defer cleanup()
	if err := ctrl.MuteNPC(ctx, sessionID, npcName); err != nil {
		return fmt.Errorf("gateway: mute NPC %q: %w", npcName, err)
	}
	return nil
}

// UnmuteNPC implements [NPCController].
func (gc *GatewaySessionController) UnmuteNPC(ctx context.Context, sessionID, npcName string) error {
	ctrl, cleanup, err := gc.dialNPCController(sessionID)
	if err != nil {
		return fmt.Errorf("gateway: unmute NPC %q: %w", npcName, err)
	}
	defer cleanup()
	if err := ctrl.UnmuteNPC(ctx, sessionID, npcName); err != nil {
		return fmt.Errorf("gateway: unmute NPC %q: %w", npcName, err)
	}
	return nil
}

// MuteAllNPCs implements [NPCController].
func (gc *GatewaySessionController) MuteAllNPCs(ctx context.Context, sessionID string) (int, error) {
	ctrl, cleanup, err := gc.dialNPCController(sessionID)
	if err != nil {
		return 0, err
	}
	defer cleanup()
	return ctrl.MuteAllNPCs(ctx, sessionID)
}

// UnmuteAllNPCs implements [NPCController].
func (gc *GatewaySessionController) UnmuteAllNPCs(ctx context.Context, sessionID string) (int, error) {
	ctrl, cleanup, err := gc.dialNPCController(sessionID)
	if err != nil {
		return 0, err
	}
	defer cleanup()
	return ctrl.UnmuteAllNPCs(ctx, sessionID)
}

// SpeakNPC implements [NPCController].
func (gc *GatewaySessionController) SpeakNPC(ctx context.Context, sessionID, npcName, text string) error {
	ctrl, cleanup, err := gc.dialNPCController(sessionID)
	if err != nil {
		return fmt.Errorf("gateway: speak NPC %q: %w", npcName, err)
	}
	defer cleanup()
	if err := ctrl.SpeakNPC(ctx, sessionID, npcName, text); err != nil {
		return fmt.Errorf("gateway: speak NPC %q: %w", npcName, err)
	}
	return nil
}

// npcDefsToConfigs converts npcstore definitions to the gateway's NPCConfigMsg
// format for transmission to workers.
func npcDefsToConfigs(defs []npcstore.NPCDefinition) []NPCConfigMsg {
	configs := make([]NPCConfigMsg, len(defs))
	for i, d := range defs {
		configs[i] = NPCConfigMsg{
			Name:           d.Name,
			Personality:    d.Personality,
			Engine:         d.Engine,
			VoiceID:        d.Voice.VoiceID,
			KnowledgeScope: d.KnowledgeScope,
			BudgetTier:     d.BudgetTier,
			GMHelper:       d.GMHelper,
			AddressOnly:    d.AddressOnly,
		}
	}
	return configs
}

// ── Voice permission check ──────────────────────────────────────────────────

// requiredVoicePerms are the Discord permissions the bot needs to join and
// speak in a voice channel.
var requiredVoicePerms = discord.PermissionConnect | discord.PermissionSpeak

// checkVoicePermissions verifies via the Discord REST API that the bot has
// CONNECT and SPEAK permissions on the target voice channel. Discord silently
// ignores the voice connect opcode when CONNECT is missing, so failing fast
// here prevents a confusing timeout.
func checkVoicePermissions(ctx context.Context, restClient rest.Rest, guildID, channelID, botID snowflake.ID) error {
	// Fetch the channel to get permission overwrites.
	ch, err := restClient.GetChannel(channelID, rest.WithCtx(ctx))
	if err != nil {
		return fmt.Errorf("fetch channel %s: %w", channelID, err)
	}
	guildCh, ok := ch.(discord.GuildChannel)
	if !ok {
		return fmt.Errorf("channel %s is not a guild channel", channelID)
	}

	// Fetch guild to get roles (needed for base permission computation).
	guild, err := restClient.GetGuild(guildID, false, rest.WithCtx(ctx))
	if err != nil {
		return fmt.Errorf("fetch guild %s: %w", guildID, err)
	}

	// Fetch the bot's member to get its role list.
	member, err := restClient.GetMember(guildID, botID, rest.WithCtx(ctx))
	if err != nil {
		return fmt.Errorf("fetch bot member in guild %s: %w", guildID, err)
	}

	perms := computeChannelPerms(guild, member, guildCh)

	if perms.Has(discord.PermissionAdministrator) {
		return nil
	}
	if perms.Missing(requiredVoicePerms) {
		missing := requiredVoicePerms.Remove(perms)
		slog.Error("gateway: bot missing voice channel permissions",
			"guild_id", guildID,
			"channel_id", channelID,
			"missing", missing,
			"required", requiredVoicePerms,
		)
		return fmt.Errorf("bot missing permissions on channel %s: need CONNECT+SPEAK, missing %s", channelID, missing)
	}
	return nil
}

// computeChannelPerms computes the effective permissions for a member in a
// guild channel following Discord's permission algorithm:
//  1. Start with @everyone role permissions.
//  2. OR in permissions from all member roles.
//  3. Apply channel-level role permission overwrites.
//  4. Apply channel-level member permission overwrite.
func computeChannelPerms(guild *discord.RestGuild, member *discord.Member, ch discord.GuildChannel) discord.Permissions {
	// Owner has all permissions.
	if guild.OwnerID == member.User.ID {
		return discord.PermissionsAll
	}

	// 1. Base: @everyone role (same ID as guild).
	var base discord.Permissions
	for _, role := range guild.Roles {
		if role.ID == guild.ID {
			base = role.Permissions
			break
		}
	}

	// 2. Accumulate role permissions.
	memberRoles := make(map[snowflake.ID]struct{}, len(member.RoleIDs))
	for _, rid := range member.RoleIDs {
		memberRoles[rid] = struct{}{}
		for _, role := range guild.Roles {
			if role.ID == rid {
				base = base.Add(role.Permissions)
				break
			}
		}
	}

	// Administrator bypasses channel overwrites.
	if base.Has(discord.PermissionAdministrator) {
		return discord.PermissionsAll
	}

	// 3. Apply channel role overwrites.
	var allow, deny discord.Permissions
	overwrites := ch.PermissionOverwrites()
	for _, ow := range overwrites {
		roleOW, ok := ow.(discord.RolePermissionOverwrite)
		if !ok {
			continue
		}
		if roleOW.RoleID == guild.ID {
			// @everyone overwrite.
			deny = deny.Add(roleOW.Deny)
			allow = allow.Add(roleOW.Allow)
		}
	}
	base = base.Remove(deny).Add(allow)

	// Other role overwrites.
	deny = 0
	allow = 0
	for _, ow := range overwrites {
		roleOW, ok := ow.(discord.RolePermissionOverwrite)
		if !ok {
			continue
		}
		if roleOW.RoleID == guild.ID {
			continue // already handled above
		}
		if _, isMember := memberRoles[roleOW.RoleID]; isMember {
			deny = deny.Add(roleOW.Deny)
			allow = allow.Add(roleOW.Allow)
		}
	}
	base = base.Remove(deny).Add(allow)

	// 4. Apply member-specific overwrite.
	if memberOW, ok := overwrites.Member(member.User.ID); ok {
		base = base.Remove(memberOW.Deny).Add(memberOW.Allow)
	}

	return base
}
