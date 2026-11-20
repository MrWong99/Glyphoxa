package presence

import (
	"context"
	"fmt"

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
//
// pool is the claim-plane control relay for the replicas>1 fleet (#503, nil in
// -mode all) — see MuteCommand. The no-Defer note above holds for the LOCAL
// path only: the cross-pod branch DOES Defer, since the relay polls the hosting
// worker past Discord's 3s deadline.
func SayCommand(mgr SayControl, agents AgentLister, pool PoolControl) Command {
	return Command{
		Path:        "say",
		Description: "Make an NPC say the given text in the running voice session.",
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
		Autocomplete: npcAutocomplete(mgr, agents, pool),
		Handle: func(ctx context.Context, ic *Interaction) error {
			vs, active, err := mgr.Active(ctx, ic.TenantID())
			if err != nil || !active {
				// No session for THIS Tenant (#488): a session live for another Tenant is
				// invisible here, so a GM can never puppet a foreign Tenant's session. When
				// the pool shows it live on ANOTHER worker, relay the say through the
				// claim plane (#503); else the plain no-session guard.
				return handleCrossPodSay(ctx, ic, agents, pool)
			}
			text, _ := ic.String("text")
			agent, ok, err := targetNPC(ctx, ic, agents, vs.CampaignID, "as")
			if !ok {
				return err
			}
			if !hasVoice(agent) {
				return ic.ReplyEphemeral(voicelessReply(agent.Name))
			}
			if err := mgr.SayAs(ctx, ic.TenantID(), agent.ID.String(), text); err != nil {
				return replyLocalControlError(ic, "say", err)
			}
			return ic.ReplyEphemeral(fmt.Sprintf("Speaking as %s.", agent.Name))
		},
	}
}

// hasVoice reports whether an NPC can be heard: an empty VoiceID never reaches
// TTS, so a /say for it would be silently dropped.
func hasVoice(a storage.Agent) bool {
	v, err := storage.VoiceFromJSON(a.Voice)
	return err == nil && v.VoiceID != ""
}

// voicelessReply answers a /say for an NPC with no voice.
func voicelessReply(name string) string {
	return fmt.Sprintf("%s has no voice yet. Pick one in the Cast tab of the web app, then try again.", name)
}

// handleCrossPodSay is /say's cross-pod branch (#503) — the say sibling of
// handleCrossPodMute: Defer FIRST, resolve the puppet target against the POOL
// session's Campaign roster, relay through the claim plane, confirm/err.
func handleCrossPodSay(ctx context.Context, ic *Interaction, agents AgentLister, pool PoolControl) error {
	vs, live := poolActive(ctx, pool, ic.TenantID())
	if !live {
		return ic.ReplyEphemeral(noSessionReply)
	}
	if err := ic.Defer(true); err != nil {
		return fmt.Errorf("presence: defer cross-pod say: %w", err)
	}
	text, _ := ic.String("text")
	agent, ok, err := targetNPC(ctx, ic, agents, vs.CampaignID, "as")
	if !ok {
		return err
	}
	if !hasVoice(agent) {
		return ic.ReplyEphemeral(voicelessReply(agent.Name))
	}
	err = pool.SayAs(ctx, ic.TenantID(), agent.ID.String(), text)
	return replyControlOutcome(ic, err, fmt.Sprintf("Speaking as %s.", agent.Name))
}
