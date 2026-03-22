package commands

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"

	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
	"github.com/MrWong99/glyphoxa/internal/entity"
)

const (
	entityAddModalID          = "entity_add_modal"
	entityRemoveCancelID      = "entity_remove_cancel"
	entityRemoveConfirmPrefix = "entity_remove_confirm:"
	maxImportSize             = 10 << 20 // 10 MB
)

// EntityCommands holds the dependencies for /entity slash commands.
type EntityCommands struct {
	perms    *discordbot.PermissionChecker
	getStore func() entity.Store
}

// NewEntityCommands creates an EntityCommands and registers its handlers
// with the router.
func NewEntityCommands(perms *discordbot.PermissionChecker, getStore func() entity.Store) *EntityCommands {
	return &EntityCommands{
		perms:    perms,
		getStore: getStore,
	}
}

// Register registers the /entity command group with the router.
func (ec *EntityCommands) Register(router *discordbot.CommandRouter) {
	def := ec.Definition()
	router.RegisterCommand("entity", def, func(e *events.ApplicationCommandInteractionCreate) {
		discordbot.RespondEphemeral(e, "Please use a subcommand: `/entity add`, `/entity list`, `/entity remove`, or `/entity import`.")
	})
	router.RegisterHandler("entity/add", ec.handleAdd)
	router.RegisterHandler("entity/list", ec.handleList)
	router.RegisterHandler("entity/remove", ec.handleRemove)
	router.RegisterHandler("entity/import", ec.handleImport)

	router.RegisterAutocomplete("entity/remove", ec.autocompleteRemove)

	router.RegisterModal(entityAddModalID, ec.handleAddModal)
	router.RegisterComponent(entityRemoveCancelID, ec.handleRemoveCancel)
	router.RegisterComponentPrefix(entityRemoveConfirmPrefix, ec.handleRemoveConfirm)
}

// Definition returns the /entity SlashCommandCreate for Discord registration.
func (ec *EntityCommands) Definition() discord.SlashCommandCreate {
	return discord.SlashCommandCreate{
		Name:        "entity",
		Description: "Manage campaign entities (NPCs, locations, items, etc.)",
		Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionSubCommand{
				Name:        "add",
				Description: "Add a new entity via a form",
			},
			discord.ApplicationCommandOptionSubCommand{
				Name:        "list",
				Description: "List all entities, optionally filtered by type",
				Options: []discord.ApplicationCommandOption{
					discord.ApplicationCommandOptionString{
						Name:        "type",
						Description: "Filter by entity type",
						Choices: []discord.ApplicationCommandOptionChoiceString{
							{Name: "NPC", Value: string(entity.EntityNPC)},
							{Name: "Location", Value: string(entity.EntityLocation)},
							{Name: "Item", Value: string(entity.EntityItem)},
							{Name: "Faction", Value: string(entity.EntityFaction)},
							{Name: "Quest", Value: string(entity.EntityQuest)},
							{Name: "Lore", Value: string(entity.EntityLore)},
						},
					},
				},
			},
			discord.ApplicationCommandOptionSubCommand{
				Name:        "remove",
				Description: "Remove an entity by name",
				Options: []discord.ApplicationCommandOption{
					discord.ApplicationCommandOptionString{
						Name:         "name",
						Description:  "Entity name",
						Required:     true,
						Autocomplete: true,
					},
				},
			},
			discord.ApplicationCommandOptionSubCommand{
				Name:        "import",
				Description: "Import entities from a YAML or JSON file attachment",
			},
		},
	}
}

// handleAdd opens the entity creation modal.
func (ec *EntityCommands) handleAdd(e *events.ApplicationCommandInteractionCreate) {
	if !ec.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to add entities.")
		return
	}

	discordbot.RespondModal(e, discord.ModalCreate{
		CustomID: entityAddModalID,
		Title:    "Add Entity",
		Components: []discord.LayoutComponent{
			discord.NewLabel("Name", discord.TextInputComponent{
				CustomID:    "entity_name",
				Style:       discord.TextInputStyleShort,
				Placeholder: "e.g., Gundren Rockseeker",
				Required:    true,
				MaxLength:   100,
			}),
			discord.NewLabel("Type (npc, location, item, faction, quest, lore)", discord.TextInputComponent{
				CustomID:    "entity_type",
				Style:       discord.TextInputStyleShort,
				Placeholder: "npc",
				Required:    true,
				MaxLength:   20,
			}),
			discord.NewLabel("Description", discord.TextInputComponent{
				CustomID:    "entity_description",
				Style:       discord.TextInputStyleParagraph,
				Placeholder: "A dwarf merchant who hired the party...",
				MaxLength:   2000,
			}),
			discord.NewLabel("Tags (comma-separated)", discord.TextInputComponent{
				CustomID:    "entity_tags",
				Style:       discord.TextInputStyleShort,
				Placeholder: "ally, phandalin, quest-giver",
				MaxLength:   200,
			}),
		},
	})
}

// handleList responds with a formatted embed of entities.
func (ec *EntityCommands) handleList(e *events.ApplicationCommandInteractionCreate) {
	if !ec.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to list entities.")
		return
	}

	opts := entity.ListOptions{}
	data := e.Data.(discord.SlashCommandInteractionData)
	if typeFilter, ok := data.OptString("type"); ok {
		opts.Type = entity.EntityType(typeFilter)
	}

	store := ec.getStore()
	if store == nil {
		discordbot.RespondEphemeral(e, "Entity commands are not available in gateway mode.")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	entities, err := store.List(ctx, opts)
	if err != nil {
		discordbot.RespondError(e, fmt.Errorf("list entities: %w", err))
		return
	}

	if len(entities) == 0 {
		discordbot.RespondEphemeral(e, "No entities found.")
		return
	}

	var fields []discord.EmbedField
	for idx, ent := range entities {
		if idx >= 25 {
			break
		}
		desc := ent.Description
		if len(desc) > 100 {
			desc = desc[:97] + "..."
		}
		value := fmt.Sprintf("**Type:** %s", ent.Type)
		if desc != "" {
			value += fmt.Sprintf("\n%s", desc)
		}
		if len(ent.Tags) > 0 {
			value += fmt.Sprintf("\n**Tags:** %s", strings.Join(ent.Tags, ", "))
		}
		fields = append(fields, discord.EmbedField{
			Name:  ent.Name,
			Value: value,
		})
	}

	title := "Entities"
	if opts.Type != "" {
		title = fmt.Sprintf("Entities (%s)", opts.Type)
	}

	discordbot.RespondEmbed(e, discord.Embed{
		Title:  title,
		Fields: fields,
		Footer: &discord.EmbedFooter{
			Text: fmt.Sprintf("%d total", len(entities)),
		},
	})
}

// handleRemove prompts confirmation before removing an entity.
func (ec *EntityCommands) handleRemove(e *events.ApplicationCommandInteractionCreate) {
	if !ec.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to remove entities.")
		return
	}

	data := e.Data.(discord.SlashCommandInteractionData)
	name := data.String("name")
	if name == "" {
		discordbot.RespondEphemeral(e, "Please provide an entity name.")
		return
	}

	store := ec.getStore()
	if store == nil {
		discordbot.RespondEphemeral(e, "Entity commands are not available in gateway mode.")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	entities, err := store.List(ctx, entity.ListOptions{})
	if err != nil {
		discordbot.RespondError(e, fmt.Errorf("list entities: %w", err))
		return
	}

	var match *entity.EntityDefinition
	for idx := range entities {
		if strings.EqualFold(entities[idx].Name, name) {
			match = &entities[idx]
			break
		}
	}

	if match == nil {
		discordbot.RespondEphemeral(e, fmt.Sprintf("Entity %q not found.", name))
		return
	}

	err = e.CreateMessage(discord.MessageCreate{
		Embeds: []discord.Embed{{
			Title:       "Remove Entity",
			Description: fmt.Sprintf("Remove entity **%s** (%s)? This cannot be undone.", match.Name, match.Type),
			Color:       0xFF4444,
		}},
		Components: []discord.LayoutComponent{
			discord.NewActionRow(
				discord.NewSecondaryButton("Cancel", entityRemoveCancelID),
				discord.NewDangerButton("Confirm Remove", entityRemoveConfirmPrefix+match.ID),
			),
		},
		Flags: discord.MessageFlagEphemeral,
	})
	if err != nil {
		slog.Warn("discord: failed to send remove confirmation", "err", err)
	}
}

// handleRemoveCancel handles the cancel button on entity removal.
func (ec *EntityCommands) handleRemoveCancel(e *events.ComponentInteractionCreate) {
	discordbot.RespondEphemeral(e, "Entity removal cancelled.")
}

// handleRemoveConfirm handles the confirm button on entity removal.
func (ec *EntityCommands) handleRemoveConfirm(e *events.ComponentInteractionCreate) {
	customID := e.Data.CustomID()
	entityID := strings.TrimPrefix(customID, entityRemoveConfirmPrefix)
	if entityID == "" {
		discordbot.RespondEphemeral(e, "Invalid entity ID.")
		return
	}

	store := ec.getStore()
	if store == nil {
		discordbot.RespondEphemeral(e, "Entity commands are not available in gateway mode.")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := store.Remove(ctx, entityID); err != nil {
		discordbot.RespondError(e, fmt.Errorf("remove entity: %w", err))
		return
	}

	discordbot.RespondEphemeral(e, fmt.Sprintf("Entity `%s` removed.", entityID))
}

// autocompleteRemove provides name autocomplete for /entity remove.
func (ec *EntityCommands) autocompleteRemove(e *events.AutocompleteInteractionCreate) {
	store := ec.getStore()
	if store == nil {
		_ = e.AutocompleteResult(nil)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	entities, err := store.List(ctx, entity.ListOptions{})
	if err != nil {
		slog.Warn("discord: entity autocomplete failed", "err", err)
		_ = e.AutocompleteResult(nil)
		return
	}

	typed := strings.ToLower(e.Data.String("name"))

	var choices []discord.AutocompleteChoice
	for _, ent := range entities {
		if typed != "" && !strings.Contains(strings.ToLower(ent.Name), typed) {
			continue
		}
		choices = append(choices, discord.AutocompleteChoiceString{
			Name:  fmt.Sprintf("%s (%s)", ent.Name, ent.Type),
			Value: ent.Name,
		})
		if len(choices) >= 25 {
			break
		}
	}

	_ = e.AutocompleteResult(choices)
}

// handleImport processes a file attachment import.
func (ec *EntityCommands) handleImport(e *events.ApplicationCommandInteractionCreate) {
	if !ec.perms.IsDM(e) {
		discordbot.RespondEphemeral(e, "You need the DM role to import entities.")
		return
	}

	data := e.Data.(discord.SlashCommandInteractionData)
	attachment, ok := FirstAttachment(data)
	if !ok {
		discordbot.RespondEphemeral(e, "Please attach a YAML or JSON file to import.")
		return
	}

	if attachment.Size > maxImportSize {
		discordbot.RespondEphemeral(e, fmt.Sprintf("File too large (%d bytes). Maximum is 10 MB.", attachment.Size))
		return
	}

	format := DetectFormat(attachment.Filename)
	if format == FormatUnknown {
		discordbot.RespondEphemeral(e, "Unsupported file format. Use .yaml, .yml, or .json.")
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

	store := ec.getStore()
	if store == nil {
		discordbot.FollowUp(e, "Entity commands are not available in gateway mode.")
		return
	}
	var count int

	switch format {
	case FormatYAML:
		cf, parseErr := entity.LoadCampaignFromReader(dl.Body)
		if parseErr != nil {
			discordbot.FollowUp(e, fmt.Sprintf("Failed to parse YAML: %v", parseErr))
			return
		}
		count, err = entity.ImportCampaign(ctx, store, cf)
	case FormatJSON:
		count, err = entity.ImportFoundryVTT(ctx, store, dl.Body)
	}

	if err != nil {
		discordbot.FollowUp(e, fmt.Sprintf("Import error: %v (imported %d entities before error)", err, count))
		return
	}

	discordbot.FollowUp(e, fmt.Sprintf("Import complete. **%d** entities imported from `%s`.", count, attachment.Filename))
}
