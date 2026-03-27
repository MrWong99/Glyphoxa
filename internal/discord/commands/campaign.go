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

// campaignEmbedColor is the embed accent color for campaign responses.
const campaignEmbedColor = 0x2ECC71

// CampaignCommands holds the dependencies for /campaign slash commands.
type CampaignCommands struct {
	perms    *discordbot.PermissionChecker
	tenantID string
	reader   CampaignReader
	updater  TenantCampaignUpdater
	writer   CampaignWriter
	getStore func() entity.Store
	getCfg   func() *config.CampaignConfig
	isActive func() bool // returns true if a session is currently active
}

// CampaignCommandsConfig configures a CampaignCommands handler.
type CampaignCommandsConfig struct {
	Perms    *discordbot.PermissionChecker
	TenantID string
	Reader   CampaignReader
	Updater  TenantCampaignUpdater
	Writer   CampaignWriter
	GetStore func() entity.Store
	GetCfg   func() *config.CampaignConfig
	IsActive func() bool
}

// NewCampaignCommands creates a CampaignCommands handler.
//
// Deprecated: Use NewCampaignCommandsFromConfig for DB-backed campaign commands.
// This constructor is retained for backward compatibility with full-mode wiring.
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

// NewCampaignCommandsFromConfig creates a CampaignCommands handler with
// DB-backed campaign reader, updater, and writer.
func NewCampaignCommandsFromConfig(cfg CampaignCommandsConfig) *CampaignCommands {
	return &CampaignCommands{
		perms:    cfg.Perms,
		tenantID: cfg.TenantID,
		reader:   cfg.Reader,
		updater:  cfg.Updater,
		writer:   cfg.Writer,
		getStore: cfg.GetStore,
		getCfg:   cfg.GetCfg,
		isActive: cfg.IsActive,
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

// handleInfo displays campaign metadata from the database.
// Falls back to YAML config when no CampaignReader is configured.
func (cc *CampaignCommands) handleInfo(e *events.ApplicationCommandInteractionCreate) {
	if !cc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to view campaign info.")
		return
	}

	// DB-backed path: query the management database.
	if cc.reader != nil && cc.tenantID != "" {
		cc.handleInfoDB(e)
		return
	}

	// Legacy fallback: read from YAML config.
	cc.handleInfoLegacy(e)
}

// handleInfoDB reads campaign info from the mgmt.campaigns database.
func (cc *CampaignCommands) handleInfoDB(e *events.ApplicationCommandInteractionCreate) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	campaigns, err := cc.reader.ListForTenant(ctx, cc.tenantID)
	if err != nil {
		slog.Warn("discord: failed to list campaigns", "tenant_id", cc.tenantID, "err", err)
		discordbot.RespondEphemeral(e, "Failed to retrieve campaign information.")
		return
	}

	if len(campaigns) == 0 {
		discordbot.RespondEphemeral(e, "No campaigns found. Create one in the web dashboard or use `/campaign load`.")
		return
	}

	// Show all campaigns as a list with the first one highlighted.
	var desc strings.Builder
	for _, c := range campaigns {
		system := c.System
		if system == "" {
			system = "(not set)"
		}
		fmt.Fprintf(&desc, "**%s** — %s\n", c.Name, system)
		if c.Description != "" {
			fmt.Fprintf(&desc, "> %s\n", c.Description)
		}
	}

	embed := discord.Embed{
		Title:       "Campaigns",
		Description: desc.String(),
		Color:       campaignEmbedColor,
		Fields: []discord.EmbedField{
			{Name: "Total", Value: fmt.Sprintf("%d", len(campaigns)), Inline: new(true)},
		},
	}
	discordbot.RespondEmbed(e, embed)
}

// handleInfoLegacy reads campaign info from the YAML config (pre-DB fallback).
func (cc *CampaignCommands) handleInfoLegacy(e *events.ApplicationCommandInteractionCreate) {
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
		Color: campaignEmbedColor,
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

// handleLoad parses a YAML campaign attachment and imports it.
// When a CampaignWriter is configured, the campaign is also persisted to the
// management database and set as the tenant's active campaign.
func (cc *CampaignCommands) handleLoad(e *events.ApplicationCommandInteractionCreate) {
	if !cc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to load a campaign.")
		return
	}

	if cc.isActive() {
		discordbot.RespondEphemeral(e, "A session is active. Please stop it with `/session stop` before loading a new campaign.")
		return
	}

	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		discordbot.RespondEphemeral(e, "Unexpected interaction data type.")
		return
	}
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

	campaignName := cf.Campaign.Name
	if campaignName == "" {
		campaignName = "(unnamed)"
	}

	// Import entities into the entity store when available (full mode).
	var entityCount int
	store := cc.getStore()
	if store != nil {
		count, importErr := entity.ImportCampaign(ctx, store, cf)
		if importErr != nil {
			discordbot.FollowUp(e, fmt.Sprintf("Import error: %v (imported %d entities before error)", importErr, count))
			return
		}
		entityCount = count
	}

	// Persist to mgmt.campaigns when a writer is configured.
	if cc.writer != nil && cc.tenantID != "" {
		campaignID, writeErr := cc.writer.CreateCampaign(ctx, cc.tenantID, cf.Campaign.Name, cf.Campaign.System, "")
		if writeErr != nil {
			slog.Warn("discord: failed to persist campaign to DB", "tenant_id", cc.tenantID, "err", writeErr)
			// Non-fatal: the YAML was parsed and entities imported.
		} else if cc.updater != nil {
			// Set the newly created campaign as active.
			if setErr := cc.updater.SetActiveCampaign(ctx, cc.tenantID, campaignID); setErr != nil {
				slog.Warn("discord: failed to set active campaign", "tenant_id", cc.tenantID, "campaign_id", campaignID, "err", setErr)
			}
		}
	}

	msg := fmt.Sprintf("Campaign loaded!\n**Name:** %s\n**System:** %s", campaignName, cf.Campaign.System)
	if entityCount > 0 {
		msg += fmt.Sprintf("\n**Entities imported:** %d", entityCount)
	}
	discordbot.FollowUp(e, msg)
}

// handleSwitch switches the tenant's active campaign.
// When a CampaignReader is configured, campaigns are listed from the database.
// Falls back to YAML config when no reader is available.
func (cc *CampaignCommands) handleSwitch(e *events.ApplicationCommandInteractionCreate) {
	if !cc.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to switch campaigns.")
		return
	}

	if cc.isActive() {
		discordbot.RespondEphemeral(e, "A session is active. Please stop it with `/session stop` before switching campaigns.")
		return
	}

	data, ok := e.Data.(discord.SlashCommandInteractionData)
	if !ok {
		discordbot.RespondEphemeral(e, "Unexpected interaction data type.")
		return
	}
	name := data.String("name")
	if name == "" {
		discordbot.RespondEphemeral(e, "Please provide a campaign name.")
		return
	}

	// DB-backed path: validate against DB and persist selection.
	if cc.reader != nil && cc.updater != nil && cc.tenantID != "" {
		cc.handleSwitchDB(e, name)
		return
	}

	// Legacy fallback.
	discordbot.RespondEphemeral(e, fmt.Sprintf("Switched to campaign **%s**.", name))
}

// handleSwitchDB validates the campaign exists in the DB, updates the tenant's
// active campaign, and responds with an embed showing campaign details.
func (cc *CampaignCommands) handleSwitchDB(e *events.ApplicationCommandInteractionCreate, campaignID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	campaign, err := cc.reader.GetCampaign(ctx, cc.tenantID, campaignID)
	if err != nil {
		slog.Warn("discord: campaign not found for switch", "tenant_id", cc.tenantID, "campaign_id", campaignID, "err", err)
		discordbot.RespondEphemeral(e, fmt.Sprintf("Campaign %q not found.", campaignID))
		return
	}

	if err := cc.updater.SetActiveCampaign(ctx, cc.tenantID, campaignID); err != nil {
		slog.Warn("discord: failed to set active campaign", "tenant_id", cc.tenantID, "campaign_id", campaignID, "err", err)
		discordbot.RespondEphemeral(e, "Failed to switch campaign. Please try again.")
		return
	}

	system := campaign.System
	if system == "" {
		system = "(not set)"
	}

	embed := discord.Embed{
		Title: "Campaign Switched",
		Color: campaignEmbedColor,
		Fields: []discord.EmbedField{
			{Name: "Name", Value: campaign.Name, Inline: new(true)},
			{Name: "System", Value: system, Inline: new(true)},
		},
	}
	if campaign.Description != "" {
		embed.Description = campaign.Description
	}

	discordbot.RespondEmbed(e, embed)
}

// autocompleteCampaignSwitch provides autocomplete for /campaign switch.
// When a CampaignReader is configured, campaigns are queried from the database.
// Falls back to YAML config when no reader is available.
func (cc *CampaignCommands) autocompleteCampaignSwitch(e *events.AutocompleteInteractionCreate) {
	// DB-backed path.
	if cc.reader != nil && cc.tenantID != "" {
		cc.autocompleteDB(e)
		return
	}

	// Legacy fallback.
	cc.autocompleteLegacy(e)
}

// autocompleteDB queries campaigns from the database and filters by partial input.
func (cc *CampaignCommands) autocompleteDB(e *events.AutocompleteInteractionCreate) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	campaigns, err := cc.reader.ListForTenant(ctx, cc.tenantID)
	if err != nil {
		slog.Warn("discord: autocomplete campaign list failed", "tenant_id", cc.tenantID, "err", err)
		_ = e.AutocompleteResult(nil)
		return
	}

	partial := strings.ToLower(e.Data.String("name"))

	var choices []discord.AutocompleteChoice
	for _, c := range campaigns {
		if partial != "" && !strings.Contains(strings.ToLower(c.Name), partial) {
			continue
		}
		choices = append(choices, discord.AutocompleteChoiceString{
			Name:  c.Name,
			Value: c.ID,
		})
		// Discord limits autocomplete to 25 choices.
		if len(choices) >= 25 {
			break
		}
	}

	_ = e.AutocompleteResult(choices)
}

// autocompleteLegacy returns the single YAML config campaign name.
func (cc *CampaignCommands) autocompleteLegacy(e *events.AutocompleteInteractionCreate) {
	var choices []discord.AutocompleteChoice
	if cc.getCfg != nil {
		cfg := cc.getCfg()
		if cfg != nil && cfg.Name != "" {
			choices = append(choices, discord.AutocompleteChoiceString{
				Name:  cfg.Name,
				Value: cfg.Name,
			})
		}
	}
	_ = e.AutocompleteResult(choices)
}
