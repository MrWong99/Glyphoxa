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

// SessionStore is the storage surface the /glyphoxa session commands need, all
// TENANT-SCOPED (#490): listing the invoking Tenant's campaigns for `use` and its
// autocomplete, loading one by id (with an explicit Tenant check by the caller),
// durably persisting the operator's Active Campaign choice, and reading that
// per-operator selection back within the Tenant. *storage.Store satisfies it; tests
// use a fake.
type SessionStore interface {
	ListCampaignsInTenant(ctx context.Context, tenantID uuid.UUID) ([]storage.Campaign, error)
	GetCampaign(ctx context.Context, id uuid.UUID) (storage.Campaign, error)
	SetActiveCampaign(ctx context.Context, discordUserID string, campaignID uuid.UUID) error
	GetActiveCampaignForUserInTenant(ctx context.Context, tenantID uuid.UUID, discordUserID string) (storage.Campaign, error)
}

// VoiceControl is the in-process voice-loop control surface /glyphoxa start and
// end drive — the SAME *session.Manager the web SessionService RPC uses, so both
// surfaces share one active-session record and can never double-start or diverge
// (AC4). *session.Manager satisfies it; tests use a fake.
type VoiceControl interface {
	Start(ctx context.Context, tenantID, campaignID uuid.UUID) (storage.VoiceSession, error)
	Stop(ctx context.Context, tenantID uuid.UUID) (storage.VoiceSession, error)
	// Active reports THIS Tenant's live Voice Session (S3, #488): the per-Tenant read
	// replacing the process-wide Snapshot, so start/end/search resolve against only
	// the invoking Tenant's own session — a session live for another Tenant is simply
	// invisible here, which is exactly the cross-tenant guard #490 needed (now for
	// free, no post-hoc campaign re-check).
	Active(ctx context.Context, tenantID uuid.UUID) (storage.VoiceSession, bool, error)
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
			// Tenant-scoped: only the invoking Tenant's own campaigns are matchable, so
			// a free-text name or a pasted foreign UUID can never select another
			// Tenant's campaign (#490).
			campaigns, err := store.ListCampaignsInTenant(ctx, ic.TenantID())
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
			campaigns, err := store.ListCampaignsInTenant(ctx, ac.TenantID())
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

			c, err := resolveActiveCampaign(ctx, store, voice, ic.TenantID(), ic.UserID())
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

			// Tenant-scoped end-to-end (#488): Stop keys on the invoking Tenant, so a
			// Tenant B GM's /glyphoxa end can only ever stop Tenant B's own session — a
			// session live for Tenant A is invisible and reports ErrNoActiveSession. The
			// #490 cross-tenant guard collapses into the keyed op (no Snapshot pre-check).
			if _, err := voice.Stop(ctx, ic.TenantID()); err != nil {
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
// order, all TENANT-SCOPED (#490): (1) the campaign of the live Voice Session, if
// one is running IN THIS TENANT; (2) the operator's durable /glyphoxa use selection
// within this Tenant; else (3) FAIL with ErrNoActiveCampaign. There is deliberately
// NO most-recently-created fallback on the slash surface (unlike the web tier) —
// the GM has the /glyphoxa use affordance right there, so the strict path avoids
// silently binding the wrong campaign.
func resolveActiveCampaign(ctx context.Context, store SessionStore, voice VoiceControl, tenantID uuid.UUID, discordUserID string) (storage.Campaign, error) {
	// (1) A live Voice Session pins the Active Campaign to its own campaign, so
	// start/end operate on exactly what is running (and a second start collides).
	// Active is Tenant-keyed (#488), so it returns ONLY this Tenant's live session —
	// a session live for another Tenant is invisible, so the #490 cross-tenant
	// re-check is no longer needed. A running session whose campaign row has vanished
	// (ErrNotFound) falls through to the durable selection rather than failing.
	if vs, active, err := voice.Active(ctx, tenantID); err == nil && active {
		c, cerr := store.GetCampaign(ctx, vs.CampaignID)
		switch {
		case cerr == nil:
			return c, nil
		case errors.Is(cerr, storage.ErrNotFound):
			// fall through to the durable selection
		default:
			return storage.Campaign{}, cerr
		}
	}

	// (2) The operator's explicit, durable choice — resolved only when it points at
	// a campaign in THIS Tenant (a selection pointing elsewhere reads back absent).
	c, err := store.GetActiveCampaignForUserInTenant(ctx, tenantID, discordUserID)
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
	case errors.Is(err, session.ErrSessionLimit):
		// #488: the process is at its concurrent-session cap
		// (GLYPHOXA_MAX_VOICE_SESSIONS). A distinct, user-visible refusal — retryable
		// once another Tenant's session ends.
		return "The server is already running the maximum number of concurrent voice sessions — try again once one ends.", true
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
