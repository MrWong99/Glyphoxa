// Package discord provides the Discord bot layer for Glyphoxa. It owns
// the disgo bot.Client lifecycle, routes slash command interactions to
// registered handlers, and checks DM role permissions.
package discord

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"

	"github.com/MrWong99/glyphoxa/pkg/audio"
	discordaudio "github.com/MrWong99/glyphoxa/pkg/audio/discord"
)

// Config holds Discord bot configuration.
type Config struct {
	// Token is the Discord bot token (e.g., "Bot MTIz...").
	Token string `yaml:"token"`

	// GuildID is the target guild (single-guild for alpha).
	GuildID string `yaml:"guild_id"`

	// DMRoleID is the Discord role ID that identifies Dungeon Masters.
	DMRoleID string `yaml:"dm_role_id"`

	// VoiceOpts are additional voice.ManagerConfigOpt applied at bot creation.
	// In production this includes golibdave.NewSession for DAVE encryption.
	VoiceOpts []voice.ManagerConfigOpt `yaml:"-"`
}

// Bot owns the Discord gateway connection and routes interactions
// to registered command handlers.
type Bot struct {
	mu        sync.RWMutex
	client    *bot.Client
	platform  *discordaudio.Platform
	router    *CommandRouter
	perms     *PermissionChecker
	guildID   snowflake.ID
	commands  []discord.ApplicationCommand
	done      chan struct{}
	closeOnce sync.Once
}

// New creates a Bot, connects to Discord, and registers the interaction handler.
func New(ctx context.Context, cfg Config) (*Bot, error) {
	guildID, err := snowflake.Parse(cfg.GuildID)
	if err != nil {
		return nil, fmt.Errorf("discord: parse guild ID %q: %w", cfg.GuildID, err)
	}

	router := NewCommandRouter()
	perms := NewPermissionChecker(cfg.DMRoleID)

	b := &Bot{
		router:  router,
		perms:   perms,
		guildID: guildID,
		done:    make(chan struct{}),
	}

	opts := []bot.ConfigOpt{
		bot.WithDefaultGateway(),
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(
				gateway.IntentGuildMessages,
				gateway.IntentGuildVoiceStates,
				gateway.IntentGuilds,
			),
		),
		bot.WithEventListenerFunc(func(e *events.ApplicationCommandInteractionCreate) {
			b.router.HandleCommand(e)
		}),
		bot.WithEventListenerFunc(func(e *events.AutocompleteInteractionCreate) {
			b.router.HandleAutocomplete(e)
		}),
		bot.WithEventListenerFunc(func(e *events.ComponentInteractionCreate) {
			b.router.HandleComponent(e)
		}),
		bot.WithEventListenerFunc(func(e *events.ModalSubmitInteractionCreate) {
			b.router.HandleModal(e)
		}),
	}

	if len(cfg.VoiceOpts) > 0 {
		opts = append(opts, bot.WithVoiceManagerConfigOpts(cfg.VoiceOpts...))
	}

	client, err := disgo.New(cfg.Token, opts...)
	if err != nil {
		return nil, fmt.Errorf("discord: create client: %w", err)
	}

	if err := client.OpenGateway(ctx); err != nil {
		return nil, fmt.Errorf("discord: open gateway: %w", err)
	}

	b.mu.Lock()
	b.client = client
	b.platform = discordaudio.New(client.VoiceManager, guildID)
	b.mu.Unlock()

	return b, nil
}

// Platform returns the audio.Platform for voice channel connections.
func (b *Bot) Platform() audio.Platform {
	return b.platform
}

// GuildID returns the target guild ID.
func (b *Bot) GuildID() snowflake.ID {
	return b.guildID
}

// Client returns the underlying disgo bot client. Used by subsystems
// that need direct Discord API access (e.g., dashboard embed updates).
func (b *Bot) Client() *bot.Client {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.client
}

// Router returns the command router for registering handlers.
func (b *Bot) Router() *CommandRouter {
	return b.router
}

// Permissions returns the permission checker.
func (b *Bot) Permissions() *PermissionChecker {
	return b.perms
}

// Run registers slash commands with the Discord API and blocks until
// ctx is cancelled.
func (b *Bot) Run(ctx context.Context) error {
	b.mu.RLock()
	client := b.client
	b.mu.RUnlock()

	cmds := b.router.ApplicationCommands()
	if len(cmds) > 0 {
		registered, err := client.Rest.SetGuildCommands(client.ApplicationID, b.guildID, cmds)
		if err != nil {
			return fmt.Errorf("discord: register commands: %w", err)
		}
		b.mu.Lock()
		b.commands = registered
		b.mu.Unlock()
		slog.Info("discord commands registered", "count", len(registered))
	}

	<-ctx.Done()
	return ctx.Err()
}

// Close disconnects from Discord and unregisters commands.
func (b *Bot) Close() error {
	var closeErr error
	b.closeOnce.Do(func() {
		b.mu.Lock()
		defer b.mu.Unlock()

		// Unregister commands.
		if b.client != nil && len(b.commands) > 0 {
			for _, cmd := range b.commands {
				if err := b.client.Rest.DeleteGuildCommand(b.client.ApplicationID, b.guildID, cmd.ID()); err != nil {
					slog.Warn("discord: failed to delete command", "name", cmd.Name(), "err", err)
				}
			}
		}

		// Close client.
		if b.client != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*1e9) // 10s
			defer cancel()
			b.client.Close(ctx)
			slog.Info("discord bot closed")
		}
	})
	return closeErr
}
