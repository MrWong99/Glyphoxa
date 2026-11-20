package presence

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Shared pieces of the live-control commands (/glyphoxa mute, /say, /direct):
// the NPC autocomplete, the roster-target resolution with its replies, and the
// answer to a failed local control. One copy each, so the three commands word
// and behave alike.

// noSessionReply is the guard every live control answers with when the Tenant
// has no running voice session.
const noSessionReply = "No voice session is running."

// controlFailedReply answers a live control that failed for a reason other
// than "no session": the cause is logged for the operator, the GM gets a next
// step instead of the misleading no-session guard.
const controlFailedReply = "Couldn't apply that to the voice session. Try again, or use the Session screen in the web app."

// activeReader is the live-session snapshot every control seam carries
// (SessionMuter, SayControl, DirectControl).
type activeReader interface {
	Active(ctx context.Context, tenantID uuid.UUID) (storage.VoiceSession, bool, error)
}

// liveSession resolves the Tenant's running session: the LOCAL Manager first,
// then the pool's Active (#503) for a session hosted by another worker.
func liveSession(ctx context.Context, local activeReader, pool PoolControl, tenantID uuid.UUID) (storage.VoiceSession, bool) {
	if vs, active, err := local.Active(ctx, tenantID); err == nil && active {
		return vs, true
	}
	return poolActive(ctx, pool, tenantID)
}

// npcAutocomplete offers the running session's voiced Character NPCs whose
// name starts with what the GM typed (Name shown, Agent UUID as the value). No
// session anywhere offers nothing. The Address-Only Butler is never offered
// (see voiced).
func npcAutocomplete(local activeReader, agents AgentLister, pool PoolControl) AutocompleteHandler {
	return func(ctx context.Context, ac *Autocomplete) ([]discord.AutocompleteChoice, error) {
		vs, active := liveSession(ctx, local, pool, ac.TenantID())
		if !active {
			return nil, nil
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
	}
}

// targetNPC resolves the `option` value of ic against campaignID's voiced
// roster. On no match or an ambiguous name it has already replied to the GM
// and reports ok=false; a roster read failure is returned for the Registry's
// generic reply + log.
func targetNPC(ctx context.Context, ic *Interaction, agents AgentLister, campaignID uuid.UUID, option string) (agent storage.Agent, ok bool, err error) {
	input, _ := ic.String(option)
	roster, err := agents.ListAgents(ctx, campaignID)
	if err != nil {
		return storage.Agent{}, false, fmt.Errorf("presence: list NPCs for %s: %w", option, err)
	}
	agent, found, ambiguous := resolveAgent(voiced(roster), input)
	switch {
	case ambiguous:
		return storage.Agent{}, false, ic.ReplyEphemeral(fmt.Sprintf("%q matches more than one NPC. Pick one from the list.", strings.TrimSpace(input)))
	case !found:
		return storage.Agent{}, false, ic.ReplyEphemeral(fmt.Sprintf("No NPC named %q in the running session's campaign.", strings.TrimSpace(input)))
	}
	return agent, true, nil
}

// replyLocalControlError answers a live control the LOCAL Manager refused. The
// session ending between the snapshot and the write reads as the no-session
// guard, and an NPC that left the campaign meanwhile says so; anything else is
// an unexpected failure the GM must not mistake for "no session", so it is
// logged and gets its own reply (S4).
func replyLocalControlError(ic *Interaction, command string, err error) error {
	switch {
	case errors.Is(err, session.ErrNoActiveSession):
		return ic.ReplyEphemeral(noSessionReply)
	case errors.Is(err, session.ErrAgentNotInCampaign):
		return ic.ReplyEphemeral("That NPC isn't part of the running session's campaign any more.")
	}
	ic.logger().Error("presence: live control failed", "command", command, "tenant", ic.TenantID(), "err", err)
	return ic.ReplyEphemeral(controlFailedReply)
}
