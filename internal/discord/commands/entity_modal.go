package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/disgoorg/disgo/events"

	discordbot "github.com/MrWong99/glyphoxa/internal/discord"
	"github.com/MrWong99/glyphoxa/internal/entity"
)

// handleAddModal processes the entity creation modal submission.
func (ec *EntityCommands) handleAddModal(e *events.ModalSubmitInteractionCreate) {
	name := strings.TrimSpace(e.Data.Text("entity_name"))
	entityType := strings.TrimSpace(strings.ToLower(e.Data.Text("entity_type")))
	description := strings.TrimSpace(e.Data.Text("entity_description"))
	tagsRaw := strings.TrimSpace(e.Data.Text("entity_tags"))

	if name == "" {
		discordbot.RespondEphemeral(e, "Entity name is required.")
		return
	}

	eType := entity.EntityType(entityType)
	if !eType.IsValid() {
		discordbot.RespondEphemeral(e, fmt.Sprintf(
			"Invalid entity type %q. Valid types: npc, location, item, faction, quest, lore.",
			entityType,
		))
		return
	}

	var tags []string
	if tagsRaw != "" {
		for t := range strings.SplitSeq(tagsRaw, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tags = append(tags, t)
			}
		}
	}

	def := entity.EntityDefinition{
		Name:        name,
		Type:        eType,
		Description: description,
		Tags:        tags,
	}

	if err := entity.Validate(def); err != nil {
		discordbot.RespondEphemeral(e, fmt.Sprintf("Validation error: %v", err))
		return
	}

	store := ec.getStore()
	if store == nil {
		discordbot.RespondEphemeral(e, "Entity commands are not available in gateway mode.")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	created, err := store.Add(ctx, def)
	if err != nil {
		discordbot.RespondError(e, fmt.Errorf("add entity: %w", err))
		return
	}

	discordbot.RespondEphemeral(e, fmt.Sprintf(
		"Entity created!\n**Name:** %s\n**Type:** %s\n**ID:** `%s`",
		created.Name, created.Type, created.ID,
	))
}
