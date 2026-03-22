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
	"github.com/MrWong99/glyphoxa/internal/gateway/dispatch"
)

// Compile-time interface assertion.
var _ SessionController = (*GatewaySessionController)(nil)

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
	gwBot      *GatewayBot // for voice credential capture and voice channel management

	mu          sync.Mutex
	active      map[string]string // guildID -> sessionID
	workerAddrs map[string]string // sessionID -> worker gRPC address

	voiceForwardersMu sync.Mutex
	voiceForwarders   map[string]func() // sessionID -> unregister func
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
		orch:            orch,
		dispatcher:      dispatcher,
		tenantID:        tenantID,
		campaignID:      campaignID,
		tier:            tier,
		active:          make(map[string]string),
		workerAddrs:     make(map[string]string),
		voiceForwarders: make(map[string]func()),
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

// WithGatewayBot sets the GatewayBot used for voice credential capture
// and voice channel management.
func WithGatewayBot(gwBot *GatewayBot) SessionControllerOption {
	return func(gc *GatewaySessionController) { gc.gwBot = gwBot }
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

		// Capture voice credentials from the gateway bot if available.
		// The gateway bot joins voice and stays connected for slash commands.
		if gc.gwBot != nil {
			voiceCtx, voiceCancel := context.WithTimeout(ctx, 10*time.Second)
			defer voiceCancel()

			vsID, vToken, vEndpoint, botUserID, captureErr := gc.captureVoiceCredentials(
				voiceCtx, req.GuildID, req.ChannelID)
			if captureErr != nil {
				_ = gc.orch.Transition(ctx, sessionID, SessionEnded, captureErr.Error())
				return fmt.Errorf("gateway: capture voice credentials: %w", captureErr)
			}

			startReq.VoiceSessionID = vsID
			startReq.VoiceToken = vToken
			startReq.VoiceEndpoint = vEndpoint
			startReq.BotUserID = botUserID
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
			// Leave voice on dispatch failure.
			if gc.gwBot != nil {
				gID, _ := snowflake.Parse(req.GuildID)
				_ = gc.gwBot.Client().UpdateVoiceState(ctx, gID, nil, false, false)
			}
			if transErr := gc.orch.Transition(ctx, sessionID, SessionEnded, dispErr.Error()); transErr != nil {
				slog.Error("gateway: failed to transition session after dispatch failure",
					"session_id", sessionID, "err", transErr)
			}
			return fmt.Errorf("gateway: dispatch worker: %w", dispErr)
		}

		// Register listener for mid-session voice server changes.
		if gc.gwBot != nil {
			gc.mu.Lock()
			gc.workerAddrs[sessionID] = result.Address
			gc.mu.Unlock()
			gc.registerVoiceServerForwarder(sessionID, req.GuildID)
		}

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

	// Leave the voice channel and clean up forwarders.
	gc.mu.Lock()
	delete(gc.workerAddrs, sessionID)
	for guildID, sid := range gc.active {
		if sid == sessionID {
			delete(gc.active, guildID)
			if gc.gwBot != nil {
				gID, _ := snowflake.Parse(guildID)
				_ = gc.gwBot.Client().UpdateVoiceState(ctx, gID, nil, false, false)
			}
			break
		}
	}
	gc.mu.Unlock()

	gc.unregisterVoiceServerForwarder(sessionID)
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

// captureVoiceCredentials joins the voice channel via the gateway bot and
// captures the voice server credentials (session_id, token, endpoint) from
// the resulting VOICE_STATE_UPDATE and VOICE_SERVER_UPDATE dispatch events.
func (gc *GatewaySessionController) captureVoiceCredentials(
	ctx context.Context, guildID, channelID string,
) (sessionID, token, endpoint, botUserID string, err error) {
	gID, _ := snowflake.Parse(guildID)
	chID, _ := snowflake.Parse(channelID)

	type creds struct {
		sessionID string
		token     string
		endpoint  string
	}
	credsCh := make(chan creds, 1)

	var (
		mu        sync.Mutex
		c         creds
		gotState  bool
		gotServer bool
	)

	// Temporary event listeners — removed after capture.
	stateListener := bot.NewListenerFunc(func(e *events.GuildVoiceStateUpdate) {
		if e.VoiceState.GuildID != gID || e.VoiceState.UserID != gc.gwBot.Client().ApplicationID {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		c.sessionID = e.VoiceState.SessionID
		gotState = true
		if gotServer {
			select {
			case credsCh <- c:
			default:
			}
		}
	})
	serverListener := bot.NewListenerFunc(func(e *events.VoiceServerUpdate) {
		if e.GuildID != gID || e.Endpoint == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		c.token = e.Token
		c.endpoint = *e.Endpoint
		gotServer = true
		if gotState {
			select {
			case credsCh <- c:
			default:
			}
		}
	})

	gc.gwBot.Client().AddEventListeners(stateListener, serverListener)
	defer gc.gwBot.Client().RemoveEventListeners(stateListener, serverListener)

	// Send Opcode 4 to join voice channel.
	if err := gc.gwBot.Client().UpdateVoiceState(ctx, gID, &chID, false, false); err != nil {
		return "", "", "", "", fmt.Errorf("send voice state update: %w", err)
	}

	select {
	case vc := <-credsCh:
		return vc.sessionID, vc.token, vc.endpoint,
			gc.gwBot.Client().ApplicationID.String(), nil
	case <-ctx.Done():
		return "", "", "", "", fmt.Errorf("capture voice credentials: %w", ctx.Err())
	}
}

// registerVoiceServerForwarder listens for mid-session VOICE_SERVER_UPDATE
// events and forwards them to the worker via gRPC.
func (gc *GatewaySessionController) registerVoiceServerForwarder(sessionID, guildID string) {
	gID, _ := snowflake.Parse(guildID)

	listener := bot.NewListenerFunc(func(e *events.VoiceServerUpdate) {
		if e.GuildID != gID || e.Endpoint == nil {
			return
		}
		gc.mu.Lock()
		addr := gc.workerAddrs[sessionID]
		gc.mu.Unlock()

		if addr == "" || gc.dialer == nil {
			return
		}
		client, err := gc.dialer(addr)
		if err != nil {
			slog.Error("gateway: failed to dial worker for voice server forward",
				"session_id", sessionID, "err", err)
			return
		}
		fwdCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.UpdateVoiceServer(fwdCtx, sessionID, e.Token, *e.Endpoint); err != nil {
			slog.Error("gateway: failed to forward voice server update",
				"session_id", sessionID, "err", err)
		}
	})

	// Also listen for external disconnections (admin kicks the bot).
	disconnectListener := bot.NewListenerFunc(func(e *events.GuildVoiceStateUpdate) {
		if e.VoiceState.GuildID != gID || e.VoiceState.UserID != gc.gwBot.Client().ApplicationID {
			return
		}
		if e.VoiceState.ChannelID == nil {
			slog.Info("gateway: bot disconnected from voice externally",
				"session_id", sessionID, "guild_id", guildID)
			go gc.Stop(context.Background(), sessionID)
		}
	})

	gc.gwBot.Client().AddEventListeners(listener, disconnectListener)

	gc.voiceForwardersMu.Lock()
	gc.voiceForwarders[sessionID] = func() {
		gc.gwBot.Client().RemoveEventListeners(listener, disconnectListener)
	}
	gc.voiceForwardersMu.Unlock()
}

// unregisterVoiceServerForwarder removes the VOICE_SERVER_UPDATE listener
// for the given session.
func (gc *GatewaySessionController) unregisterVoiceServerForwarder(sessionID string) {
	gc.voiceForwardersMu.Lock()
	unregister, ok := gc.voiceForwarders[sessionID]
	if ok {
		delete(gc.voiceForwarders, sessionID)
	}
	gc.voiceForwardersMu.Unlock()

	if ok {
		unregister()
	}
}
