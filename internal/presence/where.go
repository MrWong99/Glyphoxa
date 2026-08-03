package presence

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// /where — the Party Marker's Discord half (#540, ADR-0060, extending the
// ADR-0010 GM-only surface).
//
// The marker is per Voice Session, and it is the one piece of world state that
// changes constantly WHILE the GM's hands are busy. The web Maps tab can set it by
// dragging, but a GM mid-scene is in Discord, not in a browser tab — so the same
// state gets a slash command, resolving Map and Pin by NAME with autocomplete over
// what the campaign actually has.
//
// It is GM-only, and answered ephemerally: where the party is going next is the
// GM's information until they say it out loud.
//
// No Defer: two indexed reads and one narrow UPDATE, well inside Discord's 3s
// deadline — the same posture as /say and /direct's local path.

// WhereControl is the live-session half the command needs: the Tenant-keyed active
// session (so a GM in Tenant B can never move Tenant A's party) plus the marker
// write. *session.Manager satisfies Active structurally, mirroring DirectControl.
type WhereControl interface {
	Active(ctx context.Context, tenantID uuid.UUID) (storage.VoiceSession, bool, error)
}

// WhereStore is the Map/Pin/marker surface /where drives; *storage.Store satisfies
// it. SetPartyMarker verifies in SQL that the Map and Pin belong to the Campaign,
// so this command cannot place a marker into another world even if a stale
// autocomplete value is replayed.
type WhereStore interface {
	ListMaps(ctx context.Context, campaignID uuid.UUID) ([]storage.CampaignMap, error)
	ListPins(ctx context.Context, campaignID, mapID uuid.UUID) ([]storage.MapPin, error)
	ResolveMapByName(ctx context.Context, campaignID uuid.UUID, name string) (storage.CampaignMap, error)
	ResolvePinByLabel(ctx context.Context, campaignID, mapID uuid.UUID, label string) (storage.MapPin, error)
	SetPartyMarker(ctx context.Context, campaignID, sessionID uuid.UUID, mapID, pinID uuid.NullUUID, x, y *float64) error
}

// clearKeyword is what the GM types into `map` to take the party off the map
// entirely. A dedicated word rather than an empty option, because an omitted
// required option is a Discord error, not a command.
const clearKeyword = "none"

// WhereCommand builds the flat /where map:<map> [at:<pin>] command.
func WhereCommand(mgr WhereControl, store WhereStore) Command {
	return Command{
		Path:        "where",
		Description: "Set where the party is, so the NPCs know their surroundings.",
		GMOnly:      true,
		Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionString{
				Name:         "map",
				Description:  `The map the party is on ("none" to clear).`,
				Required:     true,
				Autocomplete: true,
			},
			discord.ApplicationCommandOptionString{
				Name:         "at",
				Description:  "A pin on that map to stand at (omit for somewhere on the map).",
				Autocomplete: true,
			},
		},
		Autocomplete: func(ctx context.Context, ac *Autocomplete) ([]discord.AutocompleteChoice, error) {
			vs, active, err := mgr.Active(ctx, ac.TenantID())
			if err != nil || !active {
				return nil, nil // no session: nothing to place, offer nothing
			}
			name, focused := ac.Focused()
			prefix := strings.ToLower(strings.TrimSpace(focused))

			if name == "at" {
				// The pin list depends on the map already chosen, so an unresolvable map
				// offers nothing rather than every pin in the campaign.
				m, merr := store.ResolveMapByName(ctx, vs.CampaignID, ac.Option("map"))
				if merr != nil {
					return nil, nil
				}
				pins, perr := store.ListPins(ctx, vs.CampaignID, m.ID)
				if perr != nil {
					return nil, perr
				}
				choices := make([]discord.AutocompleteChoice, 0, len(pins))
				for _, p := range pins {
					label := p.Label()
					if prefix != "" && !strings.HasPrefix(strings.ToLower(label), prefix) {
						continue
					}
					choices = append(choices, discord.AutocompleteChoiceString{Name: label, Value: label})
					if len(choices) >= maxAutocompleteChoices {
						break
					}
				}
				return choices, nil
			}

			maps, merr := store.ListMaps(ctx, vs.CampaignID)
			if merr != nil {
				return nil, merr
			}
			// "none" is offered first so clearing is discoverable rather than folklore.
			choices := []discord.AutocompleteChoice{
				discord.AutocompleteChoiceString{Name: "none — take the party off the map", Value: clearKeyword},
			}
			for _, m := range maps {
				if prefix != "" && !strings.HasPrefix(strings.ToLower(m.Name), prefix) {
					continue
				}
				choices = append(choices, discord.AutocompleteChoiceString{Name: m.Name, Value: m.Name})
				if len(choices) >= maxAutocompleteChoices {
					break
				}
			}
			return choices, nil
		},
		Handle: func(ctx context.Context, ic *Interaction) error {
			vs, active, err := mgr.Active(ctx, ic.TenantID())
			if err != nil || !active {
				return ic.ReplyEphemeral("No Voice Session is active.")
			}
			mapName := strings.TrimSpace(mustString(ic, "map"))
			at := strings.TrimSpace(mustString(ic, "at"))

			if strings.EqualFold(mapName, clearKeyword) {
				if err := store.SetPartyMarker(ctx, vs.CampaignID, vs.ID,
					uuid.NullUUID{}, uuid.NullUUID{}, nil, nil); err != nil {
					return whereWriteReply(ic, err)
				}
				return ic.ReplyEphemeral("The party is nowhere in particular now.")
			}

			m, err := store.ResolveMapByName(ctx, vs.CampaignID, mapName)
			switch {
			case errors.Is(err, storage.ErrNotFound):
				return ic.ReplyEphemeral(fmt.Sprintf("No map called %q in this campaign.", mapName))
			case errors.Is(err, storage.ErrConflict):
				return ic.ReplyEphemeral(fmt.Sprintf("More than one map is called %q — rename one, or pick from the list.", mapName))
			case err != nil:
				return fmt.Errorf("presence: resolve map for where: %w", err)
			}

			pinID := uuid.NullUUID{}
			where := m.Name
			if at != "" {
				p, perr := store.ResolvePinByLabel(ctx, vs.CampaignID, m.ID, at)
				switch {
				case errors.Is(perr, storage.ErrNotFound):
					return ic.ReplyEphemeral(fmt.Sprintf("Nothing called %q is pinned on %s.", at, m.Name))
				case errors.Is(perr, storage.ErrConflict):
					return ic.ReplyEphemeral(fmt.Sprintf("More than one pin on %s is called %q.", m.Name, at))
				case perr != nil:
					return fmt.Errorf("presence: resolve pin for where: %w", perr)
				}
				pinID = uuid.NullUUID{UUID: p.ID, Valid: true}
				where = fmt.Sprintf("%s, at %s", m.Name, p.Label())
			}

			if err := store.SetPartyMarker(ctx, vs.CampaignID, vs.ID,
				uuid.NullUUID{UUID: m.ID, Valid: true}, pinID, nil, nil); err != nil {
				return whereWriteReply(ic, err)
			}
			// The GM is told when the placement will not actually reach the table, rather
			// than being left to wonder why the NPCs never mention it.
			if m.GMPrivate {
				return ic.ReplyEphemeral(fmt.Sprintf(
					"The party is on %s. It is a GM-private map, so the NPCs will not mention it.", where))
			}
			return ic.ReplyEphemeral(fmt.Sprintf("The party is on %s.", where))
		},
	}
}

// whereWriteReply maps the marker write's expected failure — the session ended
// between the snapshot and the write — onto the same ephemeral guard the other
// live-session commands give.
func whereWriteReply(ic *Interaction, err error) error {
	if errors.Is(err, storage.ErrNotFound) {
		return ic.ReplyEphemeral("No Voice Session is active.")
	}
	return fmt.Errorf("presence: set party marker: %w", err)
}

// mustString reads an option, treating "absent" as empty — /where has no option
// whose absence is distinguishable from an empty value.
func mustString(ic *Interaction, name string) string {
	v, _ := ic.String(name)
	return v
}
