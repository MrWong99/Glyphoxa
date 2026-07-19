package presence

import (
	"context"
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// SayControl is the live Voice Session control surface the /say command drives
// (#295): the active-session snapshot (for the guard + the campaign the
// autocomplete/resolver list against) and the direct-speech publish. *session.Manager
// satisfies it structurally — no import, mirroring the mute seam (SessionMuter).
type SayControl interface {
	// Active reports THIS Tenant's live Voice Session (S3, #488): Tenant-keyed, so a
	// GM in Tenant B never sees (nor can puppet) Tenant A's session.
	Active(ctx context.Context, tenantID uuid.UUID) (storage.VoiceSession, bool, error)
	// SayAs asks the voiced Character NPC with agentID to speak text verbatim in the
	// Tenant's live Voice Session, bypassing Address Detection and the LLM (GM
	// puppeteering, ADR-0024). It validates the active session + campaign membership
	// atomically and returns an error (mapped here to the same ephemeral no-session
	// refusal) when the session ended or the agent is not a voiced NPC of the Active
	// Campaign.
	SayAs(ctx context.Context, tenantID uuid.UUID, agentID, text string) error
}

// SayCommand builds the FLAT /say <text> as:<agent> command (ADR-0010: GM-only,
// requires an active Voice Session). It is the GM's puppet control: the addressed
// Agent speaks the given text verbatim in the voice channel, bypassing Address
// Detection and the Agent's own LLM (ADR-0024 — /say is the explicit-target path,
// so it publishes SpeakRequested, never AddressRouted, and so never wakes the LLM
// Replier). The `as` option autocompletes the voiced Character NPCs (Name shown,
// Agent UUID as the value); the handler resolves a picked UUID against the roster,
// or a typed free-text name/alias, and replies with a clear ephemeral error on
// no/ambiguous match. A /say with no active Voice Session is refused ephemerally.
//
// There is deliberately NO Defer here (mirrors the mute command): the handler does
// one roster read plus a bus publish — both fast — and always replies ephemerally,
// so the #335 first-post-Defer EditOriginal routing rule is simply not in play.
//
// The Address-Only Butler is excluded from the puppet targets: it is never voiced
// (ADR-0009/0024), so puppeteering it needs the Butler voicer on-ramp — the
// #299-blocked follow-up (ButlerVoicer = SayAs(GetButler id)).
func SayCommand(mgr SayControl, agents AgentLister) Command {
	return Command{
		Path:        "say",
		Description: "Make an NPC speak the given text in the active Voice Session.",
		GMOnly:      true,
		Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionString{
				Name:        "text",
				Description: "What the NPC should say.",
				Required:    true,
			},
			discord.ApplicationCommandOptionString{
				Name:         "as",
				Description:  "The NPC to speak as.",
				Required:     true,
				Autocomplete: true,
			},
		},
		Autocomplete: func(ctx context.Context, ac *Autocomplete) ([]discord.AutocompleteChoice, error) {
			vs, active, err := mgr.Active(ctx, ac.TenantID())
			if err != nil || !active {
				return nil, nil // no session for this Tenant: nothing to puppet, offer nothing
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
			vs, active, err := mgr.Active(ctx, ic.TenantID())
			if err != nil || !active {
				// No session for THIS Tenant (#488): a session live for another Tenant is
				// invisible here, so a GM can never puppet a foreign Tenant's session.
				return ic.ReplyEphemeral("No Voice Session is active.")
			}
			text, _ := ic.String("text")
			input, _ := ic.String("as")
			roster, err := agents.ListAgents(ctx, vs.CampaignID)
			if err != nil {
				return fmt.Errorf("presence: list agents for say: %w", err) // unexpected → generic reply
			}
			agent, found, ambiguous := resolveAgent(voiced(roster), input)
			if ambiguous {
				return ic.ReplyEphemeral(fmt.Sprintf("%q matches more than one Agent — pick one from the list.", strings.TrimSpace(input)))
			}
			if !found {
				return ic.ReplyEphemeral(fmt.Sprintf("No Agent named %q in the Active Campaign.", strings.TrimSpace(input)))
			}
			if err := mgr.SayAs(ctx, ic.TenantID(), agent.ID.String(), text); err != nil {
				// The expected failures are a session ending between the snapshot and the
				// publish, or the resolved Agent no longer being in the (now-different)
				// active Campaign; both surface as the same ephemeral guard rather than a
				// generic error, so a racing Stop / session swap reads cleanly (mirrors mute).
				return ic.ReplyEphemeral("No Voice Session is active.")
			}
			return ic.ReplyEphemeral(fmt.Sprintf("Speaking as %s.", agent.Name))
		},
	}
}
