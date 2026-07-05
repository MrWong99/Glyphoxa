package presence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// sessionOpTimeout bounds the deferred work of /glyphoxa start and end. After a
// Defer the dispatch first-response watchdog is stopped, so the interaction ctx
// no longer carries Discord's 3s deadline; the manager Start/Stop — which can
// wait on the voice loop unwinding and the ended_at write — runs under this
// explicit budget instead of an open-ended one. It sits comfortably inside
// Discord's minutes-long follow-up window.
const sessionOpTimeout = 30 * time.Second

// discordChoiceLimit is Discord's hard cap on autocomplete choices per response.
const discordChoiceLimit = 25

// ErrNoActiveCampaign is returned by resolveActiveCampaign when neither a live
// Voice Session nor the operator's durable /glyphoxa use selection resolves a
// campaign. The slash surface deliberately has NO most-recently-created fallback
// (ADR-0009): it fails and tells the GM to run /glyphoxa use — an affordance the
// GM has right there.
var ErrNoActiveCampaign = errors.New("presence: no active campaign")

// SessionStore is the storage surface the /glyphoxa session commands need:
// listing campaigns for `use` and its autocomplete, loading one by id, durably
// persisting the operator's Active Campaign choice, and reading that per-operator
// selection back. *storage.Store satisfies it; tests use a fake.
type SessionStore interface {
	ListCampaigns(ctx context.Context) ([]storage.Campaign, error)
	GetCampaign(ctx context.Context, id uuid.UUID) (storage.Campaign, error)
	SetActiveCampaign(ctx context.Context, discordUserID string, campaignID uuid.UUID) error
	GetActiveCampaignForUser(ctx context.Context, discordUserID string) (storage.Campaign, error)
}

// VoiceControl is the in-process voice-loop control surface /glyphoxa start and
// end drive — the SAME *session.Manager the web SessionService RPC uses, so both
// surfaces share one active-session record and can never double-start or diverge
// (AC4). *session.Manager satisfies it; tests use a fake.
type VoiceControl interface {
	Start(ctx context.Context, tenantID, campaignID uuid.UUID) (storage.VoiceSession, error)
	Stop(ctx context.Context) (storage.VoiceSession, error)
	Snapshot() (storage.VoiceSession, bool)
}

// UseCommand builds `/glyphoxa use <campaign>` (ADR-0010, GM only): it durably
// records the operator's Active Campaign choice so both the slash-command surface
// and the web Session screen honor one explicit selection over the implicit
// most-recently-created default (AC1). The `campaign` option resolves by campaign
// UUID (the value an autocomplete choice carries) or, as a free-text fallback, by
// case-insensitive name; an unresolvable input is answered with a graceful
// ephemeral error naming it.
func UseCommand(store SessionStore) Command {
	return Command{
		Path:        "glyphoxa use",
		Description: "Set the Active Campaign.",
		Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionString{
				Name:         "campaign",
				Description:  "Campaign to make active.",
				Required:     true,
				Autocomplete: true,
			},
		},
		GMOnly: true,
		Handle: func(ctx context.Context, ic *Interaction) error {
			input, _ := ic.String("campaign")
			input = strings.TrimSpace(input)
			if input == "" {
				return ic.ReplyEphemeral("Name a campaign, e.g. /glyphoxa use campaign:<name>.")
			}
			campaigns, err := store.ListCampaigns(ctx)
			if err != nil {
				return fmt.Errorf("presence: list campaigns: %w", err)
			}
			c, ok := matchCampaign(campaigns, input)
			if !ok {
				return ic.ReplyEphemeral(fmt.Sprintf("No campaign matches %q. Pick one from the autocomplete list.", input))
			}
			if err := store.SetActiveCampaign(ctx, ic.UserID(), c.ID); err != nil {
				return fmt.Errorf("presence: set active campaign: %w", err)
			}
			return ic.ReplyEphemeral(fmt.Sprintf("Active Campaign set to %q.", c.Name))
		},
		Autocomplete: func(ctx context.Context, ac *Autocomplete) ([]discord.AutocompleteChoice, error) {
			_, typed := ac.Focused()
			campaigns, err := store.ListCampaigns(ctx)
			if err != nil {
				return nil, err
			}
			return campaignChoices(campaigns, typed), nil
		},
	}
}

// StartCommand builds `/glyphoxa start` (ADR-0010, GM only): it resolves the
// Active Campaign (ADR-0009) and drives the SAME in-process session.Manager the
// web Start button uses, binding a Voice Session to that campaign (AC2/AC4). It
// Defers first — the manager write is a domain operation that must not race
// Discord's 3s deadline — then follows up with the outcome (an ephemeral
// confirmation, or an ephemeral precondition error mirroring the web surface:
// no campaign, already active, Discord unconfigured, …).
func StartCommand(store SessionStore, voice VoiceControl) Command {
	return Command{
		Path:        "glyphoxa start",
		Description: "Start the voice session for the Active Campaign.",
		GMOnly:      true,
		Handle: func(ctx context.Context, ic *Interaction) error {
			if err := ic.Defer(true); err != nil {
				return fmt.Errorf("presence: defer start: %w", err)
			}
			ctx, cancel := context.WithTimeout(ctx, sessionOpTimeout)
			defer cancel()

			c, err := resolveActiveCampaign(ctx, store, voice, ic.UserID())
			if errors.Is(err, ErrNoActiveCampaign) {
				return ic.ReplyEphemeral("No Active Campaign yet — run /glyphoxa use campaign:<name> first.")
			}
			if err != nil {
				return fmt.Errorf("presence: resolve active campaign: %w", err)
			}

			if _, err := voice.Start(ctx, c.TenantID, c.ID); err != nil {
				if msg, ok := startErrorMessage(err); ok {
					return ic.ReplyEphemeral(msg)
				}
				return fmt.Errorf("presence: start voice session: %w", err)
			}
			return ic.ReplyEphemeral(fmt.Sprintf("Voice session running for %q.", c.Name))
		},
	}
}

// EndCommand builds `/glyphoxa end` (ADR-0010, GM only): it Stops the SAME
// in-process session.Manager the web Stop button uses, so ending is reflected in
// the one session record both surfaces read (AC2/AC4). Ending when none is
// running returns a clear ephemeral error (AC3). Like start it Defers, because
// Stop waits on the loop unwinding plus the ended_at write, and follows up
// ephemerally.
func EndCommand(voice VoiceControl) Command {
	return Command{
		Path:        "glyphoxa end",
		Description: "End the active voice session.",
		GMOnly:      true,
		Handle: func(ctx context.Context, ic *Interaction) error {
			if err := ic.Defer(true); err != nil {
				return fmt.Errorf("presence: defer end: %w", err)
			}
			ctx, cancel := context.WithTimeout(ctx, sessionOpTimeout)
			defer cancel()

			if _, err := voice.Stop(ctx); err != nil {
				if errors.Is(err, session.ErrNoActiveSession) {
					return ic.ReplyEphemeral("No voice session is running.")
				}
				return fmt.Errorf("presence: stop voice session: %w", err)
			}
			return ic.ReplyEphemeral("Voice session ended.")
		},
	}
}

// resolveActiveCampaign resolves the operator's Active Campaign in the ADR-0009
// order: (1) the campaign of the live Voice Session, if one is running; (2) the
// operator's durable /glyphoxa use selection; else (3) FAIL with
// ErrNoActiveCampaign. There is deliberately NO most-recently-created fallback on
// the slash surface (unlike the web tier) — the GM has the /glyphoxa use
// affordance right there, so the strict path avoids silently binding the wrong
// campaign.
func resolveActiveCampaign(ctx context.Context, store SessionStore, voice VoiceControl, discordUserID string) (storage.Campaign, error) {
	// (1) A live Voice Session pins the Active Campaign to its own campaign, so
	// start/end operate on exactly what is running (and a second start collides).
	if vs, active := voice.Snapshot(); active {
		c, err := store.GetCampaign(ctx, vs.CampaignID)
		if err == nil {
			return c, nil
		}
		if !errors.Is(err, storage.ErrNotFound) {
			return storage.Campaign{}, err
		}
		// A running session whose campaign row has vanished is not expected; fall
		// through to the durable selection rather than fail the command.
	}

	// (2) The operator's explicit, durable choice.
	c, err := store.GetActiveCampaignForUser(ctx, discordUserID)
	if errors.Is(err, storage.ErrNotFound) {
		return storage.Campaign{}, ErrNoActiveCampaign
	}
	if err != nil {
		return storage.Campaign{}, err
	}
	return c, nil
}

// startErrorMessage maps a manager Start precondition failure to its ephemeral
// GM-facing text, mirroring the web SessionService's Connect-code mapping so both
// surfaces report the same preconditions (AC3/AC4). ok is false for an unexpected
// error, which the caller propagates for a generic reply + log.
func startErrorMessage(err error) (string, bool) {
	switch {
	case errors.Is(err, session.ErrSessionActive):
		return "A voice session is already active — /glyphoxa end it first.", true
	case errors.Is(err, session.ErrDiscordNotConfigured):
		return "Discord isn't configured yet — set the Guild and voice channel on the web Configuration screen.", true
	case errors.Is(err, session.ErrDiscordTokenMissing):
		return "No Discord bot token is configured — add it on the web Configuration screen.", true
	case errors.Is(err, session.ErrDiscordTokenUndecryptable):
		return "The saved Discord bot token could not be decrypted; check the server $GLYPHOXA_SECRET (ADR-0004).", true
	case errors.Is(err, session.ErrVoiceUnavailable):
		return "Voice isn't available in this deployment mode.", true
	case errors.Is(err, session.ErrManagerClosed):
		return "The server is shutting down — try again shortly.", true
	default:
		return "", false
	}
}

// matchCampaign resolves a /glyphoxa use input to a campaign: first as a campaign
// UUID (the value an autocomplete choice carries), else as a case-insensitive
// exact name (the free-text fallback). ok is false when nothing matches.
func matchCampaign(campaigns []storage.Campaign, input string) (storage.Campaign, bool) {
	if id, err := uuid.Parse(input); err == nil {
		for _, c := range campaigns {
			if c.ID == id {
				return c, true
			}
		}
		return storage.Campaign{}, false
	}
	for _, c := range campaigns {
		if strings.EqualFold(c.Name, input) {
			return c, true
		}
	}
	return storage.Campaign{}, false
}

// campaignChoices builds the /glyphoxa use autocomplete choices: campaigns whose
// name contains the typed substring (case-insensitive), each choice DISPLAYING
// the name and CARRYING the campaign UUID as its value so a pick resolves
// unambiguously (no name collision). Capped at Discord's 25-choice limit.
func campaignChoices(campaigns []storage.Campaign, typed string) []discord.AutocompleteChoice {
	needle := strings.ToLower(strings.TrimSpace(typed))
	choices := make([]discord.AutocompleteChoice, 0, len(campaigns))
	for _, c := range campaigns {
		if needle != "" && !strings.Contains(strings.ToLower(c.Name), needle) {
			continue
		}
		choices = append(choices, discord.AutocompleteChoiceString{Name: c.Name, Value: c.ID.String()})
		if len(choices) == discordChoiceLimit {
			break
		}
	}
	return choices
}
