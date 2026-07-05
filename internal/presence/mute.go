package presence

import (
	"context"
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// SessionMuter is the live Voice Session control surface the mute commands drive
// (#211): the active-session snapshot (for the guard + the campaign the
// autocomplete lists against) and the per-Agent / all mute toggles.
// *session.Manager satisfies it structurally — no import, mirroring the RPC seam.
type SessionMuter interface {
	Snapshot() (storage.VoiceSession, bool)
	SetAgentMute(agentID string, muted bool) ([]string, error)
	SetAllMute(ctx context.Context, muted bool) ([]string, error)
}

// AgentLister is the Active Campaign roster read the mute autocomplete + resolver
// need (#211): the Butler + Character NPCs, from the agents table (not the voiced
// wirenpc Roster, which is unreachable here). *storage.Store satisfies it.
type AgentLister interface {
	ListAgents(ctx context.Context, campaignID uuid.UUID) ([]storage.Agent, error)
}

// maxAutocompleteChoices is Discord's hard cap on autocomplete choices per
// response.
const maxAutocompleteChoices = 25

// MuteCommand builds /glyphoxa mute <npc> (#211, ADR-0010 GM-only): it mutes one
// Agent of the Active Campaign in the live Voice Session. The option is named
// `npc` for table-facing familiarity but accepts ANY Agent (the Butler included).
// Its autocomplete offers each Agent (Name shown, Agent UUID as the value); the
// handler resolves a picked UUID against the roster, or a typed free-text name
// (then aliases, case-insensitive), and replies with a clear ephemeral error on
// no/ambiguous match. A mute with no active Voice Session is refused ephemerally.
// There is deliberately NO unmute command — the web panel owns un-muting.
func MuteCommand(mgr SessionMuter, agents AgentLister) Command {
	return Command{
		Path:        "glyphoxa mute",
		Description: "Mute one NPC voice in the active Voice Session.",
		GMOnly:      true,
		Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionString{
				Name:         "npc",
				Description:  "The NPC (or Butler) to mute.",
				Required:     true,
				Autocomplete: true,
			},
		},
		Autocomplete: func(ctx context.Context, ac *Autocomplete) ([]discord.AutocompleteChoice, error) {
			vs, active := mgr.Snapshot()
			if !active {
				return nil, nil // no session: nothing to mute, offer nothing
			}
			roster, err := agents.ListAgents(ctx, vs.CampaignID)
			if err != nil {
				return nil, err
			}
			_, focused := ac.Focused()
			prefix := strings.ToLower(strings.TrimSpace(focused))
			choices := make([]discord.AutocompleteChoice, 0, len(roster))
			for _, a := range roster {
				if prefix != "" && !strings.HasPrefix(strings.ToLower(a.Name), prefix) {
					continue
				}
				choices = append(choices, discord.AutocompleteChoiceString{Name: a.Name, Value: a.ID.String()})
				if len(choices) >= maxAutocompleteChoices {
					break
				}
			}
			return choices, nil
		},
		Handle: func(ctx context.Context, ic *Interaction) error {
			vs, active := mgr.Snapshot()
			if !active {
				return ic.ReplyEphemeral("No Voice Session is active.")
			}
			input, _ := ic.String("npc")
			roster, err := agents.ListAgents(ctx, vs.CampaignID)
			if err != nil {
				return fmt.Errorf("presence: list agents for mute: %w", err) // unexpected → generic reply
			}
			agent, found, ambiguous := resolveAgent(roster, input)
			if ambiguous {
				return ic.ReplyEphemeral(fmt.Sprintf("%q matches more than one Agent — pick one from the list.", strings.TrimSpace(input)))
			}
			if !found {
				return ic.ReplyEphemeral(fmt.Sprintf("No Agent named %q in the Active Campaign.", strings.TrimSpace(input)))
			}
			if _, err := mgr.SetAgentMute(agent.ID.String(), true); err != nil {
				// The only expected failure is a session ending between the snapshot and
				// the write; surface it as the same ephemeral guard rather than a generic
				// error, so a racing Stop reads cleanly.
				return ic.ReplyEphemeral("No Voice Session is active.")
			}
			return ic.ReplyEphemeral(fmt.Sprintf("%s is muted.", agent.Name))
		},
	}
}

// MuteAllCommand builds /glyphoxa muteall (#211, ADR-0010 GM-only): it mutes every
// Agent of the Active Campaign in the live Voice Session, refused ephemerally when
// no Voice Session is active. Un-muting everyone is the web panel's job.
func MuteAllCommand(mgr SessionMuter) Command {
	return Command{
		Path:        "glyphoxa muteall",
		Description: "Mute every NPC voice in the active Voice Session.",
		GMOnly:      true,
		Handle: func(ctx context.Context, ic *Interaction) error {
			if _, active := mgr.Snapshot(); !active {
				return ic.ReplyEphemeral("No Voice Session is active.")
			}
			ids, err := mgr.SetAllMute(ctx, true)
			if err != nil {
				return ic.ReplyEphemeral("No Voice Session is active.")
			}
			return ic.ReplyEphemeral(fmt.Sprintf("Muted all %d Agent voices.", len(ids)))
		},
	}
}

// resolveAgent resolves a /glyphoxa mute npc value to a roster Agent (#211):
// first as an Agent UUID (the autocomplete-picked value), else a case-insensitive
// name match, else a case-insensitive alias match. It reports found, and
// ambiguous when a free-text term matches more than one Agent.
func resolveAgent(roster []storage.Agent, input string) (agent storage.Agent, found, ambiguous bool) {
	trimmed := strings.TrimSpace(input)
	if id, err := uuid.Parse(trimmed); err == nil {
		for _, a := range roster {
			if a.ID == id {
				return a, true, false
			}
		}
		return storage.Agent{}, false, false
	}

	lower := strings.ToLower(trimmed)
	var byName []storage.Agent
	for _, a := range roster {
		if strings.ToLower(a.Name) == lower {
			byName = append(byName, a)
		}
	}
	matches := byName
	if len(matches) == 0 {
		for _, a := range roster {
			for _, al := range a.Aliases {
				if strings.ToLower(al) == lower {
					matches = append(matches, a)
					break
				}
			}
		}
	}
	switch len(matches) {
	case 0:
		return storage.Agent{}, false, false
	case 1:
		return matches[0], true, false
	default:
		return storage.Agent{}, false, true
	}
}
