package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"

	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
	gw "github.com/MrWong99/glyphoxa/internal/gateway"
)

// GatewaySessionCommands holds the dependencies for /session slash commands
// in gateway mode.
type GatewaySessionCommands struct {
	ctrl  gw.SessionController
	perms *discordbot.PermissionChecker
}

// NewGatewaySessionCommands creates and registers gateway session commands.
func NewGatewaySessionCommands(gwBot *gw.GatewayBot, ctrl gw.SessionController, perms *discordbot.PermissionChecker) *GatewaySessionCommands {
	sc := &GatewaySessionCommands{ctrl: ctrl, perms: perms}
	sc.Register(gwBot.Router())
	return sc
}

// Register registers the /session command group with the router.
func (sc *GatewaySessionCommands) Register(router *discordbot.CommandRouter) {
	def := discord.SlashCommandCreate{
		Name:        "session",
		Description: "Manage voice sessions",
		Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionSubCommand{
				Name:        "start",
				Description: "Start a voice session in your current voice channel",
			},
			discord.ApplicationCommandOptionSubCommand{
				Name:        "stop",
				Description: "Stop the active voice session",
			},
		},
	}
	router.RegisterCommand("session", def, func(e *events.ApplicationCommandInteractionCreate) {
		discordbot.RespondEphemeral(e, "Please use a subcommand: /session start, /session stop.")
	})
	router.RegisterHandler("session/start", sc.handleStart)
	router.RegisterHandler("session/stop", sc.handleStop)
}

func (sc *GatewaySessionCommands) handleStart(e *events.ApplicationCommandInteractionCreate) {
	if !sc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to start a session.")
		return
	}
	userID := e.User().ID
	guildID := *e.GuildID()
	vs, ok := e.Client().Caches.VoiceState(guildID, userID)
	if !ok || vs.ChannelID == nil {
		discordbot.RespondEphemeral(e, "You must be in a voice channel to start a session.")
		return
	}
	guildStr := guildID.String()
	if sc.ctrl.IsActive(guildStr) {
		info, _ := sc.ctrl.Info(guildStr)
		discordbot.RespondEphemeral(e, fmt.Sprintf("A session is already active (ID: %s).", info.SessionID))
		return
	}
	discordbot.DeferReply(e)
	channelID := vs.ChannelID.String()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		err := sc.ctrl.Start(ctx, gw.SessionStartRequest{
			GuildID:   guildStr,
			ChannelID: channelID,
			UserID:    userID.String(),
		})
		if err != nil {
			discordbot.FollowUp(e, fmt.Sprintf("Failed to start session: %v", err))
			return
		}
		info, _ := sc.ctrl.Info(guildStr)
		discordbot.FollowUp(e, fmt.Sprintf("Session started! **Session ID:** %s **Channel:** <#%s>", info.SessionID, info.ChannelID))
	}()
}

func (sc *GatewaySessionCommands) handleStop(e *events.ApplicationCommandInteractionCreate) {
	if !sc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to stop a session.")
		return
	}
	guildStr := e.GuildID().String()
	if !sc.ctrl.IsActive(guildStr) {
		discordbot.RespondEphemeral(e, "No active session to stop.")
		return
	}
	info, _ := sc.ctrl.Info(guildStr)
	duration := time.Since(info.StartedAt).Truncate(time.Second)
	discordbot.DeferReply(e)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := sc.ctrl.Stop(ctx, info.SessionID); err != nil {
			discordbot.FollowUp(e, fmt.Sprintf("Failed to stop session: %v", err))
			return
		}
		discordbot.FollowUp(e, fmt.Sprintf("Session %s stopped. **Duration:** %s", info.SessionID, duration.String()))
	}()
}
