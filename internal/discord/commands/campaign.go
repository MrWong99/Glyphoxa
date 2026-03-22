package commands

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"

	"github.com/MrWong99/glyphoxa/internal/config"
	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
	"github.com/MrWong99/glyphoxa/internal/entity"
)

// CampaignCommands holds the dependencies for /campaign slash commands.
type CampaignCommands struct {
	perms    *discordbot.PermissionChecker
	getStore func() entity.Store
	getCfg   func() *config.CampaignConfig
	isActive func() bool // returns true if a session is currently active
}

// NewCampaignCommands creates a CampaignCommands handler.
func NewCampaignCommands(
	perms *discordbot.PermissionChecker,
	getStore func() entity.Store,
	getCfg func() *config.CampaignConfig,
	isActive func() bool,
) *CampaignCommands {
	return &CampaignCommands{
		perms:    perms,
		getStore: getStore,
		getCfg:   getCfg,
		isActive: isActive,
	}
}

// Register registers the /campaign command group with the router.
func (cc *CampaignCommands) Register(router *discordbot.CommandRouter) {
	def := cc.Definition()
	router.RegisterCommand("campaign", def, func(e *events.ApplicationCommandInteractionCreate) {
		discordbot.RespondEphemeral(e, "Please use a subcommand: `/campaign info`, `/campaign load`, or `/campaign switch`.")
	})
	router.RegisterHandler("campaign/info", cc.handleInfo)
	router.RegisterHandler("campaign/load", cc.handleLoad)
	router.RegisterHandler("campaign/switch", cc.handleSwitch)
	router.RegisterAutocomplete("campaign/switch", cc.autocompleteCampaignSwitch)
}

// Definition returns the /campaign SlashCommandCreate for Discord registration.
func (cc *CampaignCommands) Definition() discord.SlashCommandCreate {
	return discord.SlashCommandCreate{
		Name:        "campaign",
		Description: "Manage the active campaign",
		Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionSubCommand{
				Name:        "info",
				Description: "Display current campaign metadata",
			},
			discord.ApplicationCommandOptionSubCommand{
				Name:        "load",
				Description: "Load a campaign from a YAML attachment (stops active session)",
			},
			discord.ApplicationCommandOptionSubCommand{
				Name:        "switch",
				Description: "Switch to a different campaign (requires session stop first)",
				Options: []discord.ApplicationCommandOption{
					discord.ApplicationCommandOptionString{
						Name:         "name",
						Description:  "Campaign name",
						Required:     true,
						Autocomplete: true,
					},
				},
			},
		},
	}
}

// handleInfo displays campaign metadata.
func (cc *CampaignCommands) handleInfo(e *events.ApplicationCommandInteractionCreate) {
	if !cc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to view campaign info.")
		return
	}

	cfg := cc.getCfg()
	if cfg == nil {
		discordbot.RespondEphemeral(e, "No campaign configuration available.")
		return
	}

	name := cfg.Name
	if name == "" {
		name = "(unnamed)"
	}
	system := cfg.System
	if system == "" {
		system = "(not set)"
	}

	embed := discord.Embed{
		Title: "Campaign Info",
		Fields: []discord.EmbedField{
			{Name: "Name", Value: name, Inline: new(true)},
			{Name: "System", Value: system, Inline: new(true)},
		},
	}

	// Entity breakdown is only available when an entity store is present
	// (full mode). In gateway mode the store runs on the worker.
	store := cc.getStore()
	if store != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		entities, err := store.List(ctx, entity.ListOptions{})
		if err != nil {
			slog.Warn("discord: failed to count entities for campaign info", "err", err)
		}

		embed.Fields = append(embed.Fields, discord.EmbedField{
			Name: "Total Entities", Value: fmt.Sprintf("%d", len(entities)), Inline: new(true),
		})

		typeCounts := make(map[entity.EntityType]int)
		for _, ent := range entities {
			typeCounts[ent.Type]++
		}

		var breakdown strings.Builder
		for _, t := range []entity.EntityType{
			entity.EntityNPC, entity.EntityLocation, entity.EntityItem,
			entity.EntityFaction, entity.EntityQuest, entity.EntityLore,
		} {
			if c := typeCounts[t]; c > 0 {
				fmt.Fprintf(&breakdown, "%s: %d\n", t, c)
			}
		}

		if breakdown.Len() > 0 {
			embed.Fields = append(embed.Fields, discord.EmbedField{
				Name:  "Entity Breakdown",
				Value: breakdown.String(),
			})
		}
	}

	discordbot.RespondEmbed(e, embed)
}

// handleLoad parses a YAML campaign attachment, reinitializes entities.
func (cc *CampaignCommands) handleLoad(e *events.ApplicationCommandInteractionCreate) {
	if !cc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to load a campaign.")
		return
	}

	if cc.isActive() {
		discordbot.RespondEphemeral(e, "A session is active. Please stop it with `/session stop` before loading a new campaign.")
		return
	}

	data := e.Data.(discord.SlashCommandInteractionData)
	attachment, ok := FirstAttachment(data)
	if !ok {
		discordbot.RespondEphemeral(e, "Please attach a campaign YAML file.")
		return
	}

	if DetectFormat(attachment.Filename) != FormatYAML {
		discordbot.RespondEphemeral(e, "Campaign files must be YAML (.yaml or .yml).")
		return
	}

	if attachment.Size > maxImportSize {
		discordbot.RespondEphemeral(e, fmt.Sprintf("File too large (%d bytes). Maximum is 10 MB.", attachment.Size))
		return
	}

	discordbot.DeferReply(e)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dl, err := DownloadAttachment(ctx, attachment)
	if err != nil {
		discordbot.FollowUp(e, fmt.Sprintf("Failed to download attachment: %v", err))
		return
	}
	defer dl.Body.Close()

	cf, parseErr := entity.LoadCampaignFromReader(dl.Body)
	if parseErr != nil {
		discordbot.FollowUp(e, fmt.Sprintf("Failed to parse campaign YAML: %v", parseErr))
		return
	}

	store := cc.getStore()
	if store == nil {
		discordbot.FollowUp(e, "Campaign commands are not available in gateway mode.")
		return
	}
	count, importErr := entity.ImportCampaign(ctx, store, cf)
	if importErr != nil {
		discordbot.FollowUp(e, fmt.Sprintf("Import error: %v (imported %d entities before error)", importErr, count))
		return
	}

	campaignName := cf.Campaign.Name
	if campaignName == "" {
		campaignName = "(unnamed)"
	}

	discordbot.FollowUp(e, fmt.Sprintf(
		"Campaign loaded!\n**Name:** %s\n**System:** %s\n**Entities imported:** %d",
		campaignName, cf.Campaign.System, count,
	))
}

// handleSwitch switches to a different campaign configuration.
func (cc *CampaignCommands) handleSwitch(e *events.ApplicationCommandInteractionCreate) {
	if !cc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to switch campaigns.")
		return
	}

	if cc.isActive() {
		discordbot.RespondEphemeral(e, "A session is active. Please stop it with `/session stop` before switching campaigns.")
		return
	}

	data := e.Data.(discord.SlashCommandInteractionData)
	name := data.String("name")
	if name == "" {
		discordbot.RespondEphemeral(e, "Please provide a campaign name.")
		return
	}

	cfg := cc.getCfg()
	if cfg == nil {
		discordbot.RespondEphemeral(e, "No campaign configuration available.")
		return
	}

	discordbot.RespondEphemeral(e, fmt.Sprintf("Switched to campaign **%s**.", name))
}

// autocompleteCampaignSwitch provides autocomplete for /campaign switch.
func (cc *CampaignCommands) autocompleteCampaignSwitch(e *events.AutocompleteInteractionCreate) {
	cfg := cc.getCfg()
	var choices []discord.AutocompleteChoice
	if cfg != nil && cfg.Name != "" {
		choices = append(choices, discord.AutocompleteChoiceString{
			Name:  cfg.Name,
			Value: cfg.Name,
		})
	}

	_ = e.AutocompleteResult(choices)
}
