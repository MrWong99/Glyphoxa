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
	SetAgentMute(ctx context.Context, agentID string, muted bool) ([]string, error)
	SetAllMute(ctx context.Context, muted bool) ([]string, error)
}

// AgentLister is the Active Campaign roster read the mute/say autocomplete +
// resolver need (#211): the Butler + Character NPCs, from the agents table (not the
// voiced wirenpc Roster, which is unreachable here). *storage.Store satisfies it.
// The mute surface narrows this to the Character NPCs (see voiced): the Butler is
// voiced now (ADR-0009 #299 amendment) but stays Address-Only and is never offered
// as a mute target (mute is matcher-owned and Character-only).
//
// GetCampaign loads the live session's campaign so the handler can enforce the
// TENANT guard (#490): the manager is single-active and its VoiceSession Snapshot
// carries no Tenant, so a GM must confirm the running session's campaign belongs to
// the invoking Tenant before muting/puppeting it — otherwise a GM in Tenant B could
// drive Tenant A's live session.
type AgentLister interface {
	ListAgents(ctx context.Context, campaignID uuid.UUID) ([]storage.Agent, error)
	GetCampaign(ctx context.Context, id uuid.UUID) (storage.Campaign, error)
}

// campaignTenantReader loads one Campaign by id — the narrow read the cross-tenant
// session guard needs (#490). *storage.Store, AgentLister and SessionStore all
// satisfy it.
type campaignTenantReader interface {
	GetCampaign(ctx context.Context, id uuid.UUID) (storage.Campaign, error)
}

// sessionInTenant reports whether the live Voice Session (its campaign) belongs to
// tenantID — the cross-tenant guard shared by mute/say/muteall/end and the voiced
// recap (#490): the Manager is single-active (#488 not merged) and its Snapshot
// carries no Tenant, so a GM in Tenant B must confirm the running session's campaign
// is its OWN before driving it. A missing/foreign campaign reads as false, so the
// caller treats the session as "not active for this Tenant".
func sessionInTenant(ctx context.Context, r campaignTenantReader, vs storage.VoiceSession, tenantID uuid.UUID) bool {
	c, err := r.GetCampaign(ctx, vs.CampaignID)
	return err == nil && c.TenantID == tenantID
}

// maxAutocompleteChoices is Discord's hard cap on autocomplete choices per
// response.
const maxAutocompleteChoices = 25

// MuteCommand builds /glyphoxa mute <npc> (#211, ADR-0010 GM-only): it mutes one
// voiced Agent of the Active Campaign in the live Voice Session. The option is
// named `npc` for table-facing familiarity; its autocomplete + resolver offer the
// voiced Character NPCs only — the Address-Only Butler (never voiced,
// ADR-0009/ADR-0024) is excluded, since muting it would silence nothing.
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
			if !active || !sessionInTenant(ctx, agents, vs, ac.TenantID()) {
				return nil, nil // no session for this Tenant: nothing to mute, offer nothing
			}
			roster, err := agents.ListAgents(ctx, vs.CampaignID)
			if err != nil {
				return nil, err
			}
			_, focused := ac.Focused()
			prefix := strings.ToLower(strings.TrimSpace(focused))
			choices := make([]discord.AutocompleteChoice, 0, len(roster))
			for _, a := range voiced(roster) {
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
			if !active || !sessionInTenant(ctx, agents, vs, ic.TenantID()) {
				// No session, or the single active session belongs to another Tenant (#490):
				// a GM must never mute a foreign Tenant's live session.
				return ic.ReplyEphemeral("No Voice Session is active.")
			}
			input, _ := ic.String("npc")
			roster, err := agents.ListAgents(ctx, vs.CampaignID)
			if err != nil {
				return fmt.Errorf("presence: list agents for mute: %w", err) // unexpected → generic reply
			}
			agent, found, ambiguous := resolveAgent(voiced(roster), input)
			if ambiguous {
				return ic.ReplyEphemeral(fmt.Sprintf("%q matches more than one Agent — pick one from the list.", strings.TrimSpace(input)))
			}
			if !found {
				return ic.ReplyEphemeral(fmt.Sprintf("No Agent named %q in the Active Campaign.", strings.TrimSpace(input)))
			}
			if _, err := mgr.SetAgentMute(ctx, agent.ID.String(), true); err != nil {
				// The expected failures are a session ending between the snapshot and the
				// write, or the resolved Agent no longer being in the (now-different)
				// active Campaign; both surface as the same ephemeral guard rather than a
				// generic error, so a racing Stop / session swap reads cleanly.
				return ic.ReplyEphemeral("No Voice Session is active.")
			}
			return ic.ReplyEphemeral(fmt.Sprintf("%s is muted.", agent.Name))
		},
	}
}

// MuteAllCommand builds /glyphoxa muteall (#211, ADR-0010 GM-only): it mutes every
// voiced Agent of the Active Campaign (the Character NPCs; the Address-Only Butler
// is excluded) in the live Voice Session, refused ephemerally when no Voice
// Session is active. Un-muting everyone is the web panel's job.
func MuteAllCommand(mgr SessionMuter, agents AgentLister) Command {
	return Command{
		Path:        "glyphoxa muteall",
		Description: "Mute every NPC voice in the active Voice Session.",
		GMOnly:      true,
		Handle: func(ctx context.Context, ic *Interaction) error {
			if vs, active := mgr.Snapshot(); !active || !sessionInTenant(ctx, agents, vs, ic.TenantID()) {
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

// voiced drops the Address-Only Butler from a campaign roster, leaving the Agents
// a GM can actually mute. The Butler (agent_role='butler') is in the voiced Cast
// now (ADR-0009 #299 amendment), but it stays Address-Only and mute is
// matcher-owned and Character-only, so muting it would be an inert control — it is
// neither offered by the autocomplete nor resolvable by name/UUID here. The
// manager rejects it too (see session.LiveSession.SetAgentMute); this just keeps
// the /glyphoxa surface from ever presenting a mute target that does nothing.
func voiced(agents []storage.Agent) []storage.Agent {
	out := make([]storage.Agent, 0, len(agents))
	for _, a := range agents {
		if a.Role != storage.AgentRoleButler {
			out = append(out, a)
		}
	}
	return out
}
