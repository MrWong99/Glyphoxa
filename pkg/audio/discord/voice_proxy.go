package discord

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/MrWong99/glyphoxa/pkg/audio"
	discodiscord "github.com/disgoorg/disgo/discord"
	botgateway "github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// VoiceProxyPlatform connects to a Discord voice server using pre-captured
// credentials (session_id, token, endpoint) from the gateway pod. The worker
// does NOT need its own Discord gateway connection.
//
// All exported methods are safe for concurrent use.
type VoiceProxyPlatform struct {
	conn      voice.Conn
	guildID   snowflake.ID
	botUserID snowflake.ID
	readyCh   chan struct{}
	closeOnce sync.Once
}

// NewVoiceProxyPlatform creates a voice platform that connects using
// pre-captured credentials rather than its own Discord gateway.
func NewVoiceProxyPlatform(
	guildIDStr, botUserIDStr string,
	opts ...voice.ConnConfigOpt,
) (*VoiceProxyPlatform, error) {
	guildID, err := snowflake.Parse(guildIDStr)
	if err != nil {
		return nil, fmt.Errorf("discord: parse guild ID %q: %w", guildIDStr, err)
	}
	botUserID, err := snowflake.Parse(botUserIDStr)
	if err != nil {
		return nil, fmt.Errorf("discord: parse bot user ID %q: %w", botUserIDStr, err)
	}

	vp := &VoiceProxyPlatform{
		guildID:   guildID,
		botUserID: botUserID,
		readyCh:   make(chan struct{}, 1),
	}

	// No-op: the gateway pod handles Opcode 4 (join/leave voice channel).
	noopStateUpdate := func(_ context.Context, _ snowflake.ID,
		_ *snowflake.ID, _, _ bool) error {
		return nil
	}

	allOpts := append([]voice.ConnConfigOpt{
		voice.WithConnEventHandlerFunc(func(_ voice.Gateway, _ voice.Opcode,
			_ int, data voice.GatewayMessageData) {
			if _, ok := data.(voice.GatewayMessageDataSessionDescription); ok {
				select {
				case vp.readyCh <- struct{}{}:
				default:
				}
			}
		}),
	}, opts...)

	vp.conn = voice.NewConn(guildID, botUserID, noopStateUpdate, func() {}, allOpts...)
	return vp, nil
}

// Connect feeds pre-captured voice credentials into the connection, triggering
// the voice WebSocket + UDP handshake. The ctx governs the setup phase only.
func (vp *VoiceProxyPlatform) Connect(
	ctx context.Context,
	channelIDStr, voiceSessionID, voiceToken, voiceEndpoint string,
) (audio.Connection, error) {
	channelID, err := snowflake.Parse(channelIDStr)
	if err != nil {
		return nil, fmt.Errorf("discord: parse channel ID: %w", err)
	}

	slog.Info("discord: voice proxy connecting",
		"guild_id", vp.guildID,
		"channel_id", channelID,
		"endpoint", voiceEndpoint,
	)

	// Feed the credentials that the gateway captured.
	// Order matters: HandleVoiceStateUpdate sets SessionID,
	// HandleVoiceServerUpdate triggers gateway.Open() which needs SessionID.
	vp.conn.HandleVoiceStateUpdate(botgateway.EventVoiceStateUpdate{
		VoiceState: discodiscord.VoiceState{
			GuildID:   vp.guildID,
			ChannelID: &channelID,
			UserID:    vp.botUserID,
			SessionID: voiceSessionID,
		},
	})
	vp.conn.HandleVoiceServerUpdate(botgateway.EventVoiceServerUpdate{
		Token:    voiceToken,
		GuildID:  vp.guildID,
		Endpoint: &voiceEndpoint,
	})

	// Wait for the voice WebSocket handshake to complete.
	select {
	case <-vp.readyCh:
		slog.Info("discord: voice proxy connected", "guild_id", vp.guildID)
		return newConnection(vp.conn, vp.guildID), nil
	case <-ctx.Done():
		vp.conn.Close(ctx)
		return nil, fmt.Errorf("discord: voice proxy connect: %w", ctx.Err())
	}
}

// UpdateVoiceServer handles mid-session voice server changes. Discord sends
// a new VOICE_SERVER_UPDATE when migrating voice servers. The gateway
// forwards this to the worker, which calls this method.
func (vp *VoiceProxyPlatform) UpdateVoiceServer(token, endpoint string) {
	vp.conn.HandleVoiceServerUpdate(botgateway.EventVoiceServerUpdate{
		Token:    token,
		GuildID:  vp.guildID,
		Endpoint: &endpoint,
	})
}

// Close tears down the voice connection. It is safe to call more than once.
func (vp *VoiceProxyPlatform) Close() error {
	vp.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		vp.conn.Close(ctx)
		slog.Info("discord: voice proxy closed", "guild_id", vp.guildID)
	})
	return nil
}
