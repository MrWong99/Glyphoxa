package gateway

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/snowflake/v2"

	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
)

// Compile-time interface assertion.
var _ TenantBotConnector = (*DiscordBotConnector)(nil)

// CommandSetupFunc is called after a [GatewayBot] is created to register
// slash command handlers.
type CommandSetupFunc func(gwBot *GatewayBot, tenant Tenant)

// DiscordBotConnector implements [BotConnector] and [TenantBotConnector].
type DiscordBotConnector struct {
	mgr          *BotManager
	commandSetup CommandSetupFunc
}

// NewDiscordBotConnector creates a BotConnector backed by the given BotManager.
func NewDiscordBotConnector(mgr *BotManager) *DiscordBotConnector {
	return &DiscordBotConnector{mgr: mgr}
}

// SetCommandSetup configures the callback that wires slash command handlers.
func (c *DiscordBotConnector) SetCommandSetup(fn CommandSetupFunc) {
	c.commandSetup = fn
}

// ConnectBot implements [BotConnector].
func (c *DiscordBotConnector) ConnectBot(ctx context.Context, tenantID, botToken string, guildIDs []string) error {
	return c.ConnectBotForTenant(ctx, Tenant{
		ID:       tenantID,
		BotToken: botToken,
		GuildIDs: guildIDs,
	})
}

// ConnectBotForTenant creates a fully wired [GatewayBot] for the given tenant.
func (c *DiscordBotConnector) ConnectBotForTenant(ctx context.Context, tenant Tenant) error {
	_ = c.mgr.RemoveBot(tenant.ID)

	parsedGuildIDs := make([]snowflake.ID, 0, len(tenant.GuildIDs))
	for _, gid := range tenant.GuildIDs {
		id, err := snowflake.Parse(gid)
		if err != nil {
			slog.Warn("gateway: invalid guild ID, skipping",
				"tenant_id", tenant.ID, "guild_id", gid, "err", err)
			continue
		}
		parsedGuildIDs = append(parsedGuildIDs, id)
	}

	router := discordbot.NewCommandRouter()
	perms := discordbot.NewPermissionChecker(tenant.DMRoleID)

	opts := []bot.ConfigOpt{
		bot.WithDefaultGateway(),
		bot.WithCacheConfigOpts(
			cache.WithCaches(cache.FlagVoiceStates, cache.FlagGuilds, cache.FlagChannels, cache.FlagMembers),
		),
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(
				gateway.IntentGuildMessages,
				gateway.IntentGuildVoiceStates,
				gateway.IntentGuilds,
			),
			// Enable raw events so voice credential capture can use them as
			// a fallback when typed event listeners don't fire.
			gateway.WithEnableRawEvents(true),
		),
		bot.WithEventListenerFunc(func(e *events.ApplicationCommandInteractionCreate) {
			router.HandleCommand(e)
		}),
		bot.WithEventListenerFunc(func(e *events.AutocompleteInteractionCreate) {
			router.HandleAutocomplete(e)
		}),
		bot.WithEventListenerFunc(func(e *events.ComponentInteractionCreate) {
			router.HandleComponent(e)
		}),
		bot.WithEventListenerFunc(func(e *events.ModalSubmitInteractionCreate) {
			router.HandleModal(e)
		}),
	}

	client, err := disgo.New(tenant.BotToken, opts...)
	if err != nil {
		return fmt.Errorf("gateway: create discord client for tenant %q: %w", tenant.ID, err)
	}

	// The gateway bot captures voice credentials manually via event listeners
	// rather than establishing voice connections through the VoiceManager.
	// Disable the auto-created VoiceManager so disgo's voice_handlers skip
	// VoiceManager processing and dispatch events directly to our listeners.
	client.VoiceManager = nil

	if err := client.OpenGateway(ctx); err != nil {
		client.Close(ctx)
		return fmt.Errorf("gateway: open discord gateway for tenant %q: %w", tenant.ID, err)
	}

	gwBot := NewGatewayBot(client, router, perms, tenant.ID, parsedGuildIDs)

	if c.commandSetup != nil {
		c.commandSetup(gwBot, tenant)
		if err := gwBot.RegisterCommands(ctx); err != nil {
			slog.Error("gateway: failed to register slash commands",
				"tenant_id", tenant.ID, "err", err)
		}
	}

	if err := c.mgr.AddGatewayBot(tenant.ID, gwBot); err != nil {
		gwBot.Close(ctx)
		return fmt.Errorf("gateway: register bot for tenant %q: %w", tenant.ID, err)
	}

	slog.Info("admin: discord bot connected",
		"tenant_id", tenant.ID,
		"guild_ids", tenant.GuildIDs,
		"has_commands", c.commandSetup != nil,
	)
	return nil
}

// DisconnectBot unregisters commands and then removes and closes the bot for
// the given tenant. This should only be called when the tenant is being
// permanently deleted — reconnects use [BotManager.RemoveBot] directly which
// skips command unregistration.
func (c *DiscordBotConnector) DisconnectBot(tenantID string) {
	if gwBot, ok := c.mgr.GetBot(tenantID); ok {
		gwBot.UnregisterCommands()
	}
	if err := c.mgr.RemoveBot(tenantID); err != nil {
		slog.Debug("admin: disconnect bot (no-op)", "tenant_id", tenantID, "reason", err)
	} else {
		slog.Info("admin: discord bot disconnected", "tenant_id", tenantID)
	}
}
