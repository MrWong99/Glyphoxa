package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"

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

// Start begins a new voice session.
func (gc *GatewaySessionController) Start(ctx context.Context, req SessionStartRequest) error {
	gc.mu.Lock()
	if _, ok := gc.active[req.GuildID]; ok {
		gc.mu.Unlock()
		return fmt.Errorf("gateway: session already active for guild %s", req.GuildID)
	}
	gc.mu.Unlock()

	sessionID, err := gc.orch.ValidateAndCreate(ctx, gc.tenantID, gc.campaignID, req.GuildID, req.ChannelID, gc.tier)
	if err != nil {
		return fmt.Errorf("gateway: validate session: %w", err)
	}

	if gc.dispatcher != nil {
		startReq := StartSessionRequest{
			SessionID:   sessionID,
			TenantID:    gc.tenantID,
			CampaignID:  gc.campaignID,
			GuildID:     req.GuildID,
			ChannelID:   req.ChannelID,
			LicenseTier: gc.tier.String(),
			BotToken:    gc.botToken,
			NPCConfigs:  gc.npcConfigs,
		}

		// Join voice via the gateway bot's VoiceManager and set up the audio
		// bridge so opus frames flow between Discord and the worker over gRPC.
		if gc.gwBot != nil && gc.bridgeSrv != nil {
			bridge := gc.bridgeSrv.NewSessionBridge(sessionID)

			gID, _ := snowflake.Parse(req.GuildID)
			chID, _ := snowflake.Parse(req.ChannelID)

			voiceMgr := gc.gwBot.Client().VoiceManager
			if voiceMgr == nil {
				gc.bridgeSrv.RemoveBridge(sessionID)
				_ = gc.orch.Transition(ctx, sessionID, SessionEnded, "VoiceManager not available")
				return fmt.Errorf("gateway: VoiceManager not available on gateway bot")
			}

			voiceConn := voiceMgr.CreateConn(gID)
			if joinErr := voiceConn.Open(ctx, chID, false, false); joinErr != nil {
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

// Stop gracefully ends the session.
func (gc *GatewaySessionController) Stop(ctx context.Context, sessionID string) error {
	if gc.dispatcher != nil {
		if err := gc.dispatcher.Stop(ctx, sessionID); err != nil {
			slog.Warn("gateway: dispatcher stop error",
				"session_id", sessionID, "err", err)
		}
	}
	if err := gc.orch.Transition(ctx, sessionID, SessionEnded, ""); err != nil {
		return fmt.Errorf("gateway: transition session to ended: %w", err)
	}

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
	gID, _ := snowflake.Parse(guildID)

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
// NPCController. The caller should not cache the returned controller.
func (gc *GatewaySessionController) dialNPCController(sessionID string) (NPCController, error) {
	gc.mu.Lock()
	addr, ok := gc.workerAddrs[sessionID]
	gc.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("gateway: no worker address for session %s", sessionID)
	}
	if gc.dialer == nil {
		return nil, fmt.Errorf("gateway: no worker dialer configured")
	}
	client, err := gc.dialer(addr)
	if err != nil {
		return nil, fmt.Errorf("gateway: dial worker at %s: %w", addr, err)
	}
	npcCtrl, ok := client.(NPCController)
	if !ok {
		return nil, fmt.Errorf("gateway: worker client does not implement NPCController")
	}
	return npcCtrl, nil
}

// ListNPCs implements [NPCController].
func (gc *GatewaySessionController) ListNPCs(ctx context.Context, sessionID string) ([]NPCStatus, error) {
	ctrl, err := gc.dialNPCController(sessionID)
	if err != nil {
		return nil, err
	}
	return ctrl.ListNPCs(ctx, sessionID)
}

// MuteNPC implements [NPCController].
func (gc *GatewaySessionController) MuteNPC(ctx context.Context, sessionID, npcName string) error {
	ctrl, err := gc.dialNPCController(sessionID)
	if err != nil {
		return err
	}
	return ctrl.MuteNPC(ctx, sessionID, npcName)
}

// UnmuteNPC implements [NPCController].
func (gc *GatewaySessionController) UnmuteNPC(ctx context.Context, sessionID, npcName string) error {
	ctrl, err := gc.dialNPCController(sessionID)
	if err != nil {
		return err
	}
	return ctrl.UnmuteNPC(ctx, sessionID, npcName)
}

// MuteAllNPCs implements [NPCController].
func (gc *GatewaySessionController) MuteAllNPCs(ctx context.Context, sessionID string) (int, error) {
	ctrl, err := gc.dialNPCController(sessionID)
	if err != nil {
		return 0, err
	}
	return ctrl.MuteAllNPCs(ctx, sessionID)
}

// UnmuteAllNPCs implements [NPCController].
func (gc *GatewaySessionController) UnmuteAllNPCs(ctx context.Context, sessionID string) (int, error) {
	ctrl, err := gc.dialNPCController(sessionID)
	if err != nil {
		return 0, err
	}
	return ctrl.UnmuteAllNPCs(ctx, sessionID)
}

// SpeakNPC implements [NPCController].
func (gc *GatewaySessionController) SpeakNPC(ctx context.Context, sessionID, npcName, text string) error {
	ctrl, err := gc.dialNPCController(sessionID)
	if err != nil {
		return err
	}
	return ctrl.SpeakNPC(ctx, sessionID, npcName, text)
}
