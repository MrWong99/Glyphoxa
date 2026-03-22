package gateway

import (
	"context"
	"log/slog"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/snowflake/v2"

	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
)

// GatewayBot wraps a disgo client with a command router and permission checker.
// It represents a fully wired Discord bot for a single tenant in gateway mode,
// including slash command registration and event routing.
//
// All exported methods are safe for concurrent use.
type GatewayBot struct {
	client   *bot.Client
	router   *discordbot.CommandRouter
	perms    *discordbot.PermissionChecker
	tenantID string
	guildIDs []snowflake.ID
}

// NewGatewayBot creates a GatewayBot with the given dependencies.
func NewGatewayBot(
	client *bot.Client,
	router *discordbot.CommandRouter,
	perms *discordbot.PermissionChecker,
	tenantID string,
	guildIDs []snowflake.ID,
) *GatewayBot {
	return &GatewayBot{
		client:   client,
		router:   router,
		perms:    perms,
		tenantID: tenantID,
		guildIDs: guildIDs,
	}
}

// Client returns the underlying disgo bot client.
func (g *GatewayBot) Client() *bot.Client {
	return g.client
}

// Router returns the command router for registering slash commands.
func (g *GatewayBot) Router() *discordbot.CommandRouter {
	return g.router
}

// Permissions returns the permission checker for this tenant's bot.
func (g *GatewayBot) Permissions() *discordbot.PermissionChecker {
	return g.perms
}

// TenantID returns the tenant this bot belongs to.
func (g *GatewayBot) TenantID() string {
	return g.tenantID
}

// GuildIDs returns the guild IDs this bot is scoped to.
func (g *GatewayBot) GuildIDList() []snowflake.ID {
	return g.guildIDs
}

// RegisterCommands syncs slash commands with the Discord API for each guild.
func (g *GatewayBot) RegisterCommands(_ context.Context) error {
	cmds := g.router.ApplicationCommands()
	for _, guildID := range g.guildIDs {
		if _, err := g.client.Rest.SetGuildCommands(g.client.ApplicationID, guildID, cmds); err != nil {
			slog.Warn("gateway: failed to register commands for guild",
				"tenant_id", g.tenantID,
				"guild_id", guildID,
				"err", err,
			)
		}
	}
	return nil
}

// UnregisterCommands removes all slash commands from Discord for each guild.
// Call this only when the tenant is being permanently deleted, not on
// reconnect or restart — commands persist across bot restarts and the new
// connection will overwrite them via [RegisterCommands].
func (g *GatewayBot) UnregisterCommands() {
	for _, guildID := range g.guildIDs {
		if _, err := g.client.Rest.SetGuildCommands(g.client.ApplicationID, guildID, nil); err != nil {
			slog.Debug("gateway: failed to unregister commands",
				"tenant_id", g.tenantID,
				"guild_id", guildID,
				"err", err,
			)
		}
	}
}

// Close closes the Discord client without unregistering commands.
// Commands are left in place so they remain available across restarts.
func (g *GatewayBot) Close(ctx context.Context) {
	g.client.Close(ctx)
}
