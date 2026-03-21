package discord

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/MrWong99/glyphoxa/pkg/audio"
	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// Compile-time interface assertion.
var _ audio.Platform = (*VoiceOnlyPlatform)(nil)

// VoiceOnlyOption configures a [VoiceOnlyPlatform].
type VoiceOnlyOption func(*voiceOnlyConfig)

type voiceOnlyConfig struct {
	voiceOpts []voice.ManagerConfigOpt
}

// WithVoiceManagerOpts passes additional [voice.ManagerConfigOpt] to the
// underlying disgo client (e.g., for DAVE E2EE encryption).
func WithVoiceManagerOpts(opts ...voice.ManagerConfigOpt) VoiceOnlyOption {
	return func(c *voiceOnlyConfig) {
		c.voiceOpts = append(c.voiceOpts, opts...)
	}
}

// VoiceOnlyPlatform is a minimal Discord client that provides only voice
// channel connectivity. Unlike a full discord.Bot, it does not register slash
// commands, handle interaction events, or manage guild state beyond what is
// required for voice connections.
//
// It is designed for distributed-mode workers that receive session parameters
// via gRPC and only need to join a voice channel with a tenant's bot token.
//
// VoiceOnlyPlatform implements [audio.Platform].
// All exported methods are safe for concurrent use.
type VoiceOnlyPlatform struct {
	client    *bot.Client
	inner     *Platform
	guildID   snowflake.ID
	closeOnce sync.Once
}

// NewVoiceOnlyPlatform creates a minimal disgo client with only voice
// capabilities and opens the Discord gateway. The client has no slash command
// handlers, no interaction event listeners, and minimal cache (voice states
// only).
//
// The guildID parameter scopes the platform to a single guild. Call Connect
// to join a voice channel within that guild.
//
// Close must be called when the platform is no longer needed.
func NewVoiceOnlyPlatform(ctx context.Context, token, guildID string, opts ...VoiceOnlyOption) (*VoiceOnlyPlatform, error) {
	gID, err := snowflake.Parse(guildID)
	if err != nil {
		return nil, fmt.Errorf("discord: parse guild ID %q: %w", guildID, err)
	}

	var cfg voiceOnlyConfig
	for _, o := range opts {
		o(&cfg)
	}

	botOpts := []bot.ConfigOpt{
		bot.WithDefaultGateway(),
		bot.WithCacheConfigOpts(
			cache.WithCaches(cache.FlagVoiceStates),
		),
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(
				gateway.IntentGuildVoiceStates,
			),
		),
	}
	if len(cfg.voiceOpts) > 0 {
		botOpts = append(botOpts, bot.WithVoiceManagerConfigOpts(cfg.voiceOpts...))
	}

	client, err := disgo.New(token, botOpts...)
	if err != nil {
		return nil, fmt.Errorf("discord: create voice-only client: %w", err)
	}

	if err := client.OpenGateway(ctx); err != nil {
		client.Close(ctx)
		return nil, fmt.Errorf("discord: open voice-only gateway: %w", err)
	}

	platform := New(client.VoiceManager, gID)

	slog.Info("discord: voice-only platform ready", "guild_id", guildID)

	return &VoiceOnlyPlatform{
		client:  client,
		inner:   platform,
		guildID: gID,
	}, nil
}

// Connect joins the voice channel identified by channelID and returns an
// active [audio.Connection].
func (p *VoiceOnlyPlatform) Connect(ctx context.Context, channelID string) (audio.Connection, error) {
	return p.inner.Connect(ctx, channelID)
}

// Close disconnects from Discord and releases all resources. It is safe to
// call more than once; subsequent calls are no-ops.
func (p *VoiceOnlyPlatform) Close() error {
	p.closeOnce.Do(func() {
		if p.client != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			p.client.Close(ctx)
			slog.Info("discord: voice-only platform closed", "guild_id", p.guildID)
		}
	})
	return nil
}
