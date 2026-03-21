package gateway

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/disgoorg/disgo"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/cache"
	"github.com/disgoorg/disgo/gateway"
)

// DiscordBotConnector implements BotConnector using the BotManager and disgo.
type DiscordBotConnector struct {
	mgr *BotManager
}

// NewDiscordBotConnector creates a BotConnector backed by the given BotManager.
func NewDiscordBotConnector(mgr *BotManager) *DiscordBotConnector {
	return &DiscordBotConnector{mgr: mgr}
}

// ConnectBot creates a disgo client, opens the Discord gateway, and registers
// it with the BotManager. If a bot is already connected for this tenant, the
// old one is replaced.
func (c *DiscordBotConnector) ConnectBot(ctx context.Context, tenantID, botToken string, guildIDs []string) error {
	// Remove any existing bot for this tenant (replace semantics).
	_ = c.mgr.RemoveBot(tenantID)

	client, err := disgo.New(botToken,
		bot.WithDefaultGateway(),
		bot.WithCacheConfigOpts(
			cache.WithCaches(cache.FlagVoiceStates, cache.FlagGuilds, cache.FlagChannels),
		),
		bot.WithGatewayConfigOpts(
			gateway.WithIntents(
				gateway.IntentGuildMessages,
				gateway.IntentGuildVoiceStates,
				gateway.IntentGuilds,
			),
		),
	)
	if err != nil {
		return fmt.Errorf("gateway: create discord client for tenant %q: %w", tenantID, err)
	}

	if err := client.OpenGateway(ctx); err != nil {
		client.Close(ctx)
		return fmt.Errorf("gateway: open discord gateway for tenant %q: %w", tenantID, err)
	}

	if err := c.mgr.AddBot(tenantID, client, guildIDs); err != nil {
		client.Close(ctx)
		return fmt.Errorf("gateway: register bot for tenant %q: %w", tenantID, err)
	}

	slog.Info("admin: discord bot connected",
		"tenant_id", tenantID,
		"guild_ids", guildIDs,
	)
	return nil
}

// DisconnectBot removes and closes the bot for the given tenant.
func (c *DiscordBotConnector) DisconnectBot(tenantID string) {
	if err := c.mgr.RemoveBot(tenantID); err != nil {
		slog.Debug("admin: disconnect bot (no-op)", "tenant_id", tenantID, "reason", err)
	} else {
		slog.Info("admin: discord bot disconnected", "tenant_id", tenantID)
	}
}
