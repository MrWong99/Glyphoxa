package wirenpc

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// TapeConsentStore is the durable consent surface behind the buttons (#306): a row
// per (Campaign, Speaker) is the source of truth for consent. *storage.Store
// satisfies it. It embeds the read the tape reseeds from.
type TapeConsentStore interface {
	TapeConsentReader
	UpsertTapeConsent(ctx context.Context, campaignID uuid.UUID, discordUserID string) error
	DeleteTapeConsent(ctx context.Context, campaignID uuid.UUID, discordUserID string) error
}

// Tape consent button custom-id scheme (#306, ADR-0051). The disclosure message
// wirenpc posts carries a Consent and a Revoke button; each button's custom id
// encodes the action and the Campaign it applies to, so the presence layer's
// component handler knows which Campaign to write consent for without any
// per-message state. Format: "gx:tape:{grant|revoke}:<campaign-uuid>".
const (
	tapeConsentCustomPrefix = "gx:tape:"
	tapeConsentGrant        = "grant"
	tapeConsentRevoke       = "revoke"
)

// tapeGrantCustomID / tapeRevokeCustomID build a button custom id for a Campaign.
func tapeGrantCustomID(campaignID uuid.UUID) string {
	return tapeConsentCustomPrefix + tapeConsentGrant + ":" + campaignID.String()
}

func tapeRevokeCustomID(campaignID uuid.UUID) string {
	return tapeConsentCustomPrefix + tapeConsentRevoke + ":" + campaignID.String()
}

// ParseTapeConsentCustomID parses a tape-consent button custom id, returning the
// Campaign it applies to and whether it is a grant (true) or revoke (false). ok is
// false for any custom id that is not a well-formed tape-consent id — the presence
// component handler ignores those (they belong to other component interactions).
func ParseTapeConsentCustomID(customID string) (campaignID uuid.UUID, granted, ok bool) {
	rest, found := strings.CutPrefix(customID, tapeConsentCustomPrefix)
	if !found {
		return uuid.Nil, false, false
	}
	action, idStr, found := strings.Cut(rest, ":")
	if !found {
		return uuid.Nil, false, false
	}
	switch action {
	case tapeConsentGrant:
		granted = true
	case tapeConsentRevoke:
		granted = false
	default:
		return uuid.Nil, false, false
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return uuid.Nil, false, false
	}
	return id, granted, true
}

// tapeDisclosureContent is the in-channel consent notice (ADR-0051): it tells the
// table the session may be recorded into a short rolling buffer for highlights,
// and that only speakers who press Consent are captured — nothing is recorded from
// anyone who does not opt in.
const tapeDisclosureContent = "**Session Highlights are enabled.** With your consent, this session's audio is " +
	"kept in a short (~2 minute) rolling buffer so memorable moments can be clipped for the GM's review. " +
	"Only players who press **Consent** are recorded — press it to opt in, or **Revoke** to opt out at any time. " +
	"Nothing leaves this server without an explicit GM action."

// postTapeDisclosure posts the consent-disclosure message with Consent/Revoke
// buttons to the voice channel after the Bot joins (#306). It is a package var so
// tests substitute a spy and connectAndServe stays free of a live Discord REST. The
// production implementation sends via the standing client's REST.
var postTapeDisclosure = func(ctx context.Context, client *bot.Client, channel snowflake.ID, campaignID uuid.UUID) error {
	msg := discord.MessageCreate{
		Content: tapeDisclosureContent,
		Components: []discord.LayoutComponent{
			discord.NewActionRow(
				discord.NewPrimaryButton("Consent", tapeGrantCustomID(campaignID)),
				discord.NewDangerButton("Revoke", tapeRevokeCustomID(campaignID)),
			),
		},
	}
	if _, err := client.Rest.CreateMessage(channel, msg); err != nil {
		return fmt.Errorf("wirenpc: post tape disclosure: %w", err)
	}
	return nil
}

// Consent reply text (#306). Centralised here so both the all-mode presence
// handler and the voice-mode listener reply identically.
const (
	tapeConsentGrantedReply = "You're now included in Session Highlights for this campaign. You can press **Revoke** any time to opt out."
	tapeConsentRevokedReply = "You've opted out of Session Highlights. Your audio won't be recorded, and anything already buffered is discarded."
	tapeConsentFailedReply  = "Sorry — could not update your choice. Please try again."
)

// ApplyTapeConsent is the transport-agnostic core behind a consent button press
// (#306, ADR-0051), shared by the all-mode presence handler and the voice-mode
// client listener so both topologies behave identically. It parses the custom id,
// writes (or deletes) the Speaker's durable consent row, then publishes
// [voiceevent.TapeConsentChanged] on bus so the live tape reconciles — the DB write
// happens BEFORE the event (the MuteChanged ordering precedent), so a reactor that
// re-reads storage on the event always sees the change.
//
// ok is false for a custom id that is not a tape-consent button (the caller ignores
// it). A storage failure returns ok=true (the button WAS ours) with an apologetic
// reply and publishes nothing — the durable state did not change, so neither must
// the live tape — and is LOGGED here (the presser only sees "try again"; the
// operator needs the cause). A nil logger discards.
func ApplyTapeConsent(ctx context.Context, store TapeConsentStore, bus *voiceevent.Bus, now func() time.Time, log *slog.Logger, customID, userID string) (reply string, ok bool) {
	campaignID, granted, ok := ParseTapeConsentCustomID(customID)
	if !ok {
		return "", false
	}
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	if granted {
		if err := store.UpsertTapeConsent(ctx, campaignID, userID); err != nil {
			log.Error("tape: upsert consent", "campaign", campaignID, "speaker", userID, "err", err)
			return tapeConsentFailedReply, true
		}
	} else {
		if err := store.DeleteTapeConsent(ctx, campaignID, userID); err != nil {
			log.Error("tape: delete consent", "campaign", campaignID, "speaker", userID, "err", err)
			return tapeConsentFailedReply, true
		}
	}

	if bus != nil {
		bus.Publish(voiceevent.TapeConsentChanged{
			At:         now(),
			CampaignID: campaignID.String(),
			SpeakerID:  userID,
			Granted:    granted,
		})
	}
	if granted {
		return tapeConsentGrantedReply, true
	}
	return tapeConsentRevokedReply, true
}

// tapeConsentListener builds the component-interaction listener the STANDALONE
// voice-mode client carries (#306, finding 5): with no boot-owned presence in that
// topology, the per-cycle client itself must answer the disclosure's buttons, or
// every press fails "interaction failed". It applies the consent change (write +
// publish on the same per-cycle bus the tape subscribes to) and replies ephemerally.
// A non-tape component is ignored. A nil store disables it (returns nil).
func tapeConsentListener(store TapeConsentStore, bus *voiceevent.Bus, log *slog.Logger) func(*events.ComponentInteractionCreate) {
	if store == nil {
		return nil
	}
	return func(e *events.ComponentInteractionCreate) {
		reply, ok := ApplyTapeConsent(context.Background(), store, bus, time.Now, log, e.Data.CustomID(), e.User().ID.String())
		if !ok {
			return
		}
		msg := discord.MessageCreate{Content: reply, Flags: discord.MessageFlagEphemeral}
		if err := e.CreateMessage(msg); err != nil && log != nil {
			log.Warn("wirenpc: reply to tape consent button", "err", err)
		}
	}
}
