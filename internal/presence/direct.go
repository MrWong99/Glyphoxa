package presence

import (
	"context"
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// DirectControl is the live Voice Session control surface the /direct command
// drives (ADR-0059): the active-session snapshot (guard + roster resolution)
// and the directive set/clear. *session.Manager satisfies it structurally — no
// import, mirroring the say seam (SayControl).
type DirectControl interface {
	// Active reports THIS Tenant's live Voice Session (S3, #488): Tenant-keyed, so
	// a GM in Tenant B can never direct Tenant A's NPCs.
	Active(ctx context.Context, tenantID uuid.UUID) (storage.VoiceSession, bool, error)
	// DirectAs sets (non-empty text) or clears (empty text) the GM directive for
	// the voiced Character NPC with agentID in the Tenant's live Voice Session;
	// turns bounds how many committed Agent turns it rides (0 = sticky). It
	// validates the active session + campaign membership atomically and errors
	// when the session ended or the agent is not a voiced NPC of the Active
	// Campaign.
	DirectAs(ctx context.Context, tenantID uuid.UUID, agentID, text string, turns int) error
}

// directMaxTurns caps the turns option: a directive is a scene-scoped nudge,
// not a persona edit — past ~25 turns the GM wants the sticky default (omit
// turns) or a Persona change instead.
const directMaxTurns = 25

// DirectCommand builds the FLAT /direct as:<agent> [note] [turns] command
// (ADR-0059, extending the ADR-0010 GM-only surface): the Regie track. Where
// /say PUPPETS an NPC (the GM's words, verbatim), /direct STEERS it — a private
// stage note ("Bart lies about the key") folded into that one Agent's Hot
// Context for its next `turns` committed replies (omitted: until cleared or
// session end), never heard, seen, or transcript-recorded by the table. Omitting
// `note` clears the Agent's active directive. The `as` option autocompletes the
// voiced Character NPCs exactly like /say; the Address-Only Butler is excluded
// (the GM's own assistant needs no secret stage notes).
//
// There is deliberately NO Defer on the local path (mirrors /say): one roster
// read plus an in-memory state write, always answered ephemerally — the
// directive must never surface in channel chat. The cross-pod branch DOES
// Defer, since the claim-plane relay polls past Discord's 3s deadline (#503).
func DirectCommand(mgr DirectControl, agents AgentLister, pool PoolControl) Command {
	return Command{
		Path:        "direct",
		Description: "Privately steer an NPC's next replies with a hidden GM note.",
		GMOnly:      true,
		Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionString{
				Name:         "as",
				Description:  "The NPC to direct.",
				Required:     true,
				Autocomplete: true,
			},
			discord.ApplicationCommandOptionString{
				Name:        "note",
				Description: "The hidden direction (omit to clear the NPC's current one).",
			},
			discord.ApplicationCommandOptionInt{
				Name:        "turns",
				Description: "How many of the NPC's replies the note steers (omit: until cleared).",
				MinValue:    intPtr(1),
				MaxValue:    intPtr(directMaxTurns),
			},
		},
		Autocomplete: npcAutocomplete(mgr, agents, pool),
		Handle: func(ctx context.Context, ic *Interaction) error {
			vs, active, err := mgr.Active(ctx, ic.TenantID())
			if err != nil || !active {
				// No session for THIS Tenant (#488): when the pool shows it live on
				// ANOTHER worker, relay the directive through the claim plane (#503);
				// else the plain no-session guard.
				return handleCrossPodDirect(ctx, ic, agents, pool)
			}
			note, _ := ic.String("note")
			turns64, _ := ic.Int("turns")
			agent, ok, err := targetNPC(ctx, ic, agents, vs.CampaignID, "as")
			if !ok {
				return err
			}
			if err := mgr.DirectAs(ctx, ic.TenantID(), agent.ID.String(), strings.TrimSpace(note), int(turns64)); err != nil {
				return replyLocalControlError(ic, "direct", err)
			}
			return ic.ReplyEphemeral(directConfirmation(agent.Name, strings.TrimSpace(note), int(turns64)))
		},
	}
}

// handleCrossPodDirect is /direct's cross-pod branch (#503) — the direct sibling
// of handleCrossPodSay: Defer FIRST, resolve the target against the POOL
// session's Campaign roster, relay through the claim plane, confirm/err.
func handleCrossPodDirect(ctx context.Context, ic *Interaction, agents AgentLister, pool PoolControl) error {
	vs, live := poolActive(ctx, pool, ic.TenantID())
	if !live {
		return ic.ReplyEphemeral(noSessionReply)
	}
	if err := ic.Defer(true); err != nil {
		return fmt.Errorf("presence: defer cross-pod direct: %w", err)
	}
	note, _ := ic.String("note")
	turns64, _ := ic.Int("turns")
	agent, ok, err := targetNPC(ctx, ic, agents, vs.CampaignID, "as")
	if !ok {
		return err
	}
	err = pool.DirectAs(ctx, ic.TenantID(), agent.ID.String(), strings.TrimSpace(note), int(turns64))
	return replyControlOutcome(ic, err, directConfirmation(agent.Name, strings.TrimSpace(note), int(turns64)))
}

// directConfirmation words the GM-only acknowledgement: set (bounded or
// sticky) vs cleared. It never echoes the note back into the channel beyond
// the ephemeral reply the invoking GM alone sees.
func directConfirmation(name, note string, turns int) string {
	if note == "" {
		return fmt.Sprintf("Cleared the direction for %s.", name)
	}
	if turns > 0 {
		return fmt.Sprintf("Directing %s for the next %d replies.", name, turns)
	}
	return fmt.Sprintf("Directing %s until cleared.", name)
}

func intPtr(v int) *int { return &v }
