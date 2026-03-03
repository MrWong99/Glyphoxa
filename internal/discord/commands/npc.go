package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"

	"github.com/MrWong99/glyphoxa/internal/agent/orchestrator"
	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
)

// NPCCommands handles /npc slash command group.
type NPCCommands struct {
	perms *discordbot.PermissionChecker
	// getOrch returns the current session's orchestrator, or nil if no session is active.
	getOrch func() *orchestrator.Orchestrator
}

// NewNPCCommands creates an NPCCommands handler.
func NewNPCCommands(perms *discordbot.PermissionChecker, getOrch func() *orchestrator.Orchestrator) *NPCCommands {
	return &NPCCommands{
		perms:   perms,
		getOrch: getOrch,
	}
}

// Register registers all /npc subcommands with the router.
func (nc *NPCCommands) Register(router *discordbot.CommandRouter) {
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

	router.RegisterAutocomplete("npc/mute", nc.handleAutocomplete)
	router.RegisterAutocomplete("npc/unmute", nc.handleAutocomplete)
	router.RegisterAutocomplete("npc/speak", nc.handleAutocomplete)
}

// Definition returns the /npc SlashCommandCreate for Discord registration.
func (nc *NPCCommands) Definition() discord.SlashCommandCreate {
	npcNameOption := discord.ApplicationCommandOptionString{
		Name:         "name",
		Description:  "NPC name",
		Required:     true,
		Autocomplete: true,
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

// handleList handles /npc list.
func (nc *NPCCommands) handleList(e *events.ApplicationCommandInteractionCreate) {
	if !nc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to manage NPCs.")
		return
	}

	orch := nc.getOrch()
	if orch == nil {
		discordbot.RespondEphemeral(e, "No active session.")
		return
	}

	agents := orch.ActiveAgents()
	if len(agents) == 0 {
		discordbot.RespondEphemeral(e, "No NPCs in this session.")
		return
	}

	var lines []string
	for _, a := range agents {
		muted, _ := orch.IsMuted(a.ID())
		icon := "🔊"
		if muted {
			icon = "🔇"
		}
		lines = append(lines, fmt.Sprintf("%s **%s** (ID: `%s`)", icon, a.Name(), a.ID()))
	}

	discordbot.RespondEmbed(e, discord.Embed{
		Title:       "NPC Agents",
		Description: strings.Join(lines, "\n"),
		Color:       0x5865F2,
	})
}

// handleMute handles /npc mute <name>.
func (nc *NPCCommands) handleMute(e *events.ApplicationCommandInteractionCreate) {
	if !nc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to manage NPCs.")
		return
	}

	orch := nc.getOrch()
	if orch == nil {
		discordbot.RespondEphemeral(e, "No active session.")
		return
	}

	data := e.SlashCommandInteractionData()
	name := data.String("name")
	a := orch.AgentByName(name)
	if a == nil {
		discordbot.RespondEphemeral(e, fmt.Sprintf("NPC %q not found.", name))
		return
	}

	if err := orch.MuteAgent(a.ID()); err != nil {
		discordbot.RespondError(e, err)
		return
	}

	discordbot.RespondEphemeral(e, fmt.Sprintf("Muted **%s**.", a.Name()))
}

// handleUnmute handles /npc unmute <name>.
func (nc *NPCCommands) handleUnmute(e *events.ApplicationCommandInteractionCreate) {
	if !nc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to manage NPCs.")
		return
	}

	orch := nc.getOrch()
	if orch == nil {
		discordbot.RespondEphemeral(e, "No active session.")
		return
	}

	data := e.SlashCommandInteractionData()
	name := data.String("name")
	a := orch.AgentByName(name)
	if a == nil {
		discordbot.RespondEphemeral(e, fmt.Sprintf("NPC %q not found.", name))
		return
	}

	if err := orch.UnmuteAgent(a.ID()); err != nil {
		discordbot.RespondError(e, err)
		return
	}

	discordbot.RespondEphemeral(e, fmt.Sprintf("Unmuted **%s**.", a.Name()))
}

// handleSpeak handles /npc speak <name> <text>.
func (nc *NPCCommands) handleSpeak(e *events.ApplicationCommandInteractionCreate) {
	if !nc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to manage NPCs.")
		return
	}

	orch := nc.getOrch()
	if orch == nil {
		discordbot.RespondEphemeral(e, "No active session.")
		return
	}

	data := e.SlashCommandInteractionData()
	name := data.String("name")
	text := data.String("text")

	a := orch.AgentByName(name)
	if a == nil {
		discordbot.RespondEphemeral(e, fmt.Sprintf("NPC %q not found.", name))
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := a.SpeakText(ctx, text); err != nil {
		discordbot.RespondError(e, err)
		return
	}

	discordbot.RespondEphemeral(e, fmt.Sprintf("**%s** is speaking: %q", a.Name(), text))
}

// handleMuteAll handles /npc muteall.
func (nc *NPCCommands) handleMuteAll(e *events.ApplicationCommandInteractionCreate) {
	if !nc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to manage NPCs.")
		return
	}

	orch := nc.getOrch()
	if orch == nil {
		discordbot.RespondEphemeral(e, "No active session.")
		return
	}

	count := orch.MuteAll()
	discordbot.RespondEphemeral(e, fmt.Sprintf("Muted %d NPC(s).", count))
}

// handleUnmuteAll handles /npc unmuteall.
func (nc *NPCCommands) handleUnmuteAll(e *events.ApplicationCommandInteractionCreate) {
	if !nc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to manage NPCs.")
		return
	}

	orch := nc.getOrch()
	if orch == nil {
		discordbot.RespondEphemeral(e, "No active session.")
		return
	}

	count := orch.UnmuteAll()
	discordbot.RespondEphemeral(e, fmt.Sprintf("Unmuted %d NPC(s).", count))
}

// handleAutocomplete provides autocomplete for the "name" option across
// /npc mute, /npc unmute, and /npc speak.
func (nc *NPCCommands) handleAutocomplete(e *events.AutocompleteInteractionCreate) {
	orch := nc.getOrch()
	if orch == nil {
		_ = e.AutocompleteResult(nil)
		return
	}

	partial := strings.ToLower(e.Data.String("name"))

	agents := orch.ActiveAgents()
	var choices []discord.AutocompleteChoice
	for _, a := range agents {
		if partial == "" || strings.HasPrefix(strings.ToLower(a.Name()), partial) {
			choices = append(choices, discord.AutocompleteChoiceString{
				Name:  a.Name(),
				Value: a.Name(),
			})
		}
		if len(choices) >= 25 {
			break
		}
	}

	_ = e.AutocompleteResult(choices)
}
