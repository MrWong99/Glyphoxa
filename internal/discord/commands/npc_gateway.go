package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"

	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
	gw "github.com/MrWong99/glyphoxa/internal/gateway"
)

// GatewayNPCCommands handles /npc slash commands in gateway mode by proxying
// NPC management operations through gRPC to the worker.
type GatewayNPCCommands struct {
	ctrl    gw.SessionController
	npcCtrl gw.NPCController
	perms   *discordbot.PermissionChecker
}

// NewGatewayNPCCommands creates and registers gateway NPC commands.
func NewGatewayNPCCommands(
	gwBot *gw.GatewayBot,
	ctrl gw.SessionController,
	npcCtrl gw.NPCController,
	perms *discordbot.PermissionChecker,
) *GatewayNPCCommands {
	nc := &GatewayNPCCommands{ctrl: ctrl, npcCtrl: npcCtrl, perms: perms}
	nc.Register(gwBot.Router())
	return nc
}

// Register registers all /npc subcommands with the router.
func (nc *GatewayNPCCommands) Register(router *discordbot.CommandRouter) {
	def := nc.Definition()
	router.RegisterCommand("npc", def, func(e *events.ApplicationCommandInteractionCreate) {
		discordbot.RespondEphemeral(e, "Please use a subcommand: `/npc list`, `/npc mute`, `/npc unmute`, `/npc speak`, `/npc muteall`, `/npc unmuteall`.")
	})
	router.RegisterHandler("npc/list", nc.handleList)
	router.RegisterHandler("npc/mute", nc.handleMute)
	router.RegisterHandler("npc/unmute", nc.handleUnmute)
	router.RegisterHandler("npc/speak", nc.handleSpeak)
	router.RegisterHandler("npc/muteall", nc.handleMuteAll)
	router.RegisterHandler("npc/unmuteall", nc.handleUnmuteAll)
}

// Definition returns the /npc SlashCommandCreate for Discord registration.
func (nc *GatewayNPCCommands) Definition() discord.SlashCommandCreate {
	npcNameOption := discord.ApplicationCommandOptionString{
		Name:        "name",
		Description: "NPC name",
		Required:    true,
	}
	return discord.SlashCommandCreate{
		Name:        "npc",
		Description: "Manage NPC agents",
		Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionSubCommand{
				Name:        "list",
				Description: "List all NPCs with their status",
			},
			discord.ApplicationCommandOptionSubCommand{
				Name:        "mute",
				Description: "Mute an NPC",
				Options:     []discord.ApplicationCommandOption{npcNameOption},
			},
			discord.ApplicationCommandOptionSubCommand{
				Name:        "unmute",
				Description: "Unmute an NPC",
				Options:     []discord.ApplicationCommandOption{npcNameOption},
			},
			discord.ApplicationCommandOptionSubCommand{
				Name:        "speak",
				Description: "Make an NPC speak pre-written text",
				Options: []discord.ApplicationCommandOption{
					npcNameOption,
					discord.ApplicationCommandOptionString{
						Name:        "text",
						Description: "Text for the NPC to speak",
						Required:    true,
					},
				},
			},
			discord.ApplicationCommandOptionSubCommand{
				Name:        "muteall",
				Description: "Mute all NPCs",
			},
			discord.ApplicationCommandOptionSubCommand{
				Name:        "unmuteall",
				Description: "Unmute all NPCs",
			},
		},
	}
}

// sessionID returns the active session ID for the guild, or responds with
// an error and returns empty string if no session is active.
func (nc *GatewayNPCCommands) sessionID(e *events.ApplicationCommandInteractionCreate) string {
	guildStr := e.GuildID().String()
	if !nc.ctrl.IsActive(guildStr) {
		discordbot.RespondEphemeral(e, "No active session.")
		return ""
	}
	info, _ := nc.ctrl.Info(guildStr)
	return info.SessionID
}

// handleList handles /npc list.
func (nc *GatewayNPCCommands) handleList(e *events.ApplicationCommandInteractionCreate) {
	if !nc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to manage NPCs.")
		return
	}

	sid := nc.sessionID(e)
	if sid == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	npcs, err := nc.npcCtrl.ListNPCs(ctx, sid)
	if err != nil {
		discordbot.RespondError(e, err)
		return
	}

	if len(npcs) == 0 {
		discordbot.RespondEphemeral(e, "No NPCs in this session.")
		return
	}

	var lines []string
	for _, n := range npcs {
		icon := "🔊"
		if n.Muted {
			icon = "🔇"
		}
		lines = append(lines, fmt.Sprintf("%s **%s** (ID: `%s`)", icon, n.Name, n.ID))
	}

	discordbot.RespondEmbed(e, discord.Embed{
		Title:       "NPC Agents",
		Description: strings.Join(lines, "\n"),
		Color:       0x5865F2,
	})
}

// handleMute handles /npc mute <name>.
func (nc *GatewayNPCCommands) handleMute(e *events.ApplicationCommandInteractionCreate) {
	if !nc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to manage NPCs.")
		return
	}

	sid := nc.sessionID(e)
	if sid == "" {
		return
	}

	name := e.SlashCommandInteractionData().String("name")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := nc.npcCtrl.MuteNPC(ctx, sid, name); err != nil {
		discordbot.RespondError(e, err)
		return
	}
	discordbot.RespondEphemeral(e, fmt.Sprintf("Muted **%s**.", name))
}

// handleUnmute handles /npc unmute <name>.
func (nc *GatewayNPCCommands) handleUnmute(e *events.ApplicationCommandInteractionCreate) {
	if !nc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to manage NPCs.")
		return
	}

	sid := nc.sessionID(e)
	if sid == "" {
		return
	}

	name := e.SlashCommandInteractionData().String("name")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := nc.npcCtrl.UnmuteNPC(ctx, sid, name); err != nil {
		discordbot.RespondError(e, err)
		return
	}
	discordbot.RespondEphemeral(e, fmt.Sprintf("Unmuted **%s**.", name))
}

// handleSpeak handles /npc speak <name> <text>.
func (nc *GatewayNPCCommands) handleSpeak(e *events.ApplicationCommandInteractionCreate) {
	if !nc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to manage NPCs.")
		return
	}

	sid := nc.sessionID(e)
	if sid == "" {
		return
	}

	data := e.SlashCommandInteractionData()
	name := data.String("name")
	text := data.String("text")

	discordbot.DeferReply(e)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := nc.npcCtrl.SpeakNPC(ctx, sid, name, text); err != nil {
			discordbot.FollowUp(e, fmt.Sprintf("Failed to make %s speak: %v", name, err))
			return
		}
		discordbot.FollowUp(e, fmt.Sprintf("**%s** is speaking: %q", name, text))
	}()
}

// handleMuteAll handles /npc muteall.
func (nc *GatewayNPCCommands) handleMuteAll(e *events.ApplicationCommandInteractionCreate) {
	if !nc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to manage NPCs.")
		return
	}

	sid := nc.sessionID(e)
	if sid == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	count, err := nc.npcCtrl.MuteAllNPCs(ctx, sid)
	if err != nil {
		discordbot.RespondError(e, err)
		return
	}
	discordbot.RespondEphemeral(e, fmt.Sprintf("Muted %d NPC(s).", count))
}

// handleUnmuteAll handles /npc unmuteall.
func (nc *GatewayNPCCommands) handleUnmuteAll(e *events.ApplicationCommandInteractionCreate) {
	if !nc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to manage NPCs.")
		return
	}

	sid := nc.sessionID(e)
	if sid == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	count, err := nc.npcCtrl.UnmuteAllNPCs(ctx, sid)
	if err != nil {
		discordbot.RespondError(e, err)
		return
	}
	discordbot.RespondEphemeral(e, fmt.Sprintf("Unmuted %d NPC(s).", count))
}
