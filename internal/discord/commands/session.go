// Package commands implements Discord slash command handlers for Glyphoxa.
package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"

	"github.com/MrWong99/glyphoxa/internal/app"
	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
)

// SessionCommands holds the dependencies for /session slash commands.
type SessionCommands struct {
	sessionMgr *app.SessionManager
	perms      *discordbot.PermissionChecker
	bot        *discordbot.Bot
}

// NewSessionCommands creates a SessionCommands and registers its handlers
// with the bot's router.
func NewSessionCommands(bot *discordbot.Bot, sessionMgr *app.SessionManager, perms *discordbot.PermissionChecker) *SessionCommands {
	sc := &SessionCommands{
		sessionMgr: sessionMgr,
		perms:      perms,
		bot:        bot,
	}
	sc.Register(bot.Router())
	return sc
}

// Register registers the /session command group with the router.
func (sc *SessionCommands) Register(router *discordbot.CommandRouter) {
	def := sc.Definition()
	router.RegisterCommand("session", def, func(e *events.ApplicationCommandInteractionCreate) {
		discordbot.RespondEphemeral(e, "Please use a subcommand: `/session start`, `/session stop`, `/session recap`, or `/session voice-recap`.")
	})
	router.RegisterHandler("session/start", sc.handleStart)
	router.RegisterHandler("session/stop", sc.handleStop)
}

// Definition returns the SlashCommandCreate definition for Discord.
func (sc *SessionCommands) Definition() discord.SlashCommandCreate {
	return discord.SlashCommandCreate{
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
			discord.ApplicationCommandOptionSubCommand{
				Name:        "recap",
				Description: "Show a recap of the current or most recent session",
				Options: []discord.ApplicationCommandOption{
					discord.ApplicationCommandOptionString{
						Name:        "session_id",
						Description: "Session ID (defaults to current or most recent)",
						Required:    false,
					},
				},
			},
			discord.ApplicationCommandOptionSubCommand{
				Name:        "voice-recap",
				Description: "Generate and play a dramatic voiced recap of a session",
				Options: []discord.ApplicationCommandOption{
					discord.ApplicationCommandOptionString{
						Name:        "session_id",
						Description: "Session ID (defaults to most recent for this campaign)",
						Required:    false,
					},
				},
			},
		},
	}
}

// handleStart handles /session start.
func (sc *SessionCommands) handleStart(e *events.ApplicationCommandInteractionCreate) {
	if !sc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to start a session.")
		return
	}

	// Check that the user is in a voice channel.
	guildID := sc.bot.GuildID()
	userID := e.User().ID
	vs, ok := e.Client().Caches.VoiceState(guildID, userID)
	if !ok || vs.ChannelID == nil {
		discordbot.RespondEphemeral(e, "You must be in a voice channel to start a session.")
		return
	}

	if sc.sessionMgr.IsActive() {
		info := sc.sessionMgr.Info()
		discordbot.RespondEphemeral(e, fmt.Sprintf("A session is already active (ID: `%s`).", info.SessionID))
		return
	}

	discordbot.DeferReply(e)

	// Run Start in a goroutine so the gateway event handler returns
	// immediately. conn.Open() waits for VoiceStateUpdate and
	// VoiceServerUpdate events, which are delivered on the same gateway
	// event loop — blocking here would deadlock.
	channelID := vs.ChannelID.String()
	dmUserID := userID.String()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := sc.sessionMgr.Start(ctx, channelID, dmUserID); err != nil {
			discordbot.FollowUp(e, fmt.Sprintf("Failed to start session: %v", err))
			return
		}

		info := sc.sessionMgr.Info()
		discordbot.FollowUp(e, fmt.Sprintf(
			"Session started!\n**Session ID:** `%s`\n**Campaign:** %s\n**Channel:** <#%s>",
			info.SessionID,
			info.CampaignName,
			info.ChannelID,
		))
	}()
}

// handleStop handles /session stop.
func (sc *SessionCommands) handleStop(e *events.ApplicationCommandInteractionCreate) {
	if !sc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to stop a session.")
		return
	}

	if !sc.sessionMgr.IsActive() {
		discordbot.RespondEphemeral(e, "No active session to stop.")
		return
	}

	info := sc.sessionMgr.Info()
	duration := time.Since(info.StartedAt).Truncate(time.Second)

	discordbot.DeferReply(e)

	// Run Stop in a goroutine so the gateway event handler returns
	// immediately. conn.Close needs the gateway to process
	// VoiceStateUpdate — blocking here would deadlock.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := sc.sessionMgr.Stop(ctx); err != nil {
			discordbot.FollowUp(e, fmt.Sprintf("Failed to stop session: %v", err))
			return
		}

		discordbot.FollowUp(e, fmt.Sprintf(
			"Session `%s` stopped.\n**Duration:** %s",
			info.SessionID,
			duration.String(),
		))
	}()
}
