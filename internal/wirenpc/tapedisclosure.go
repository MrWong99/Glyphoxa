package wirenpc

import (
	"context"
	"fmt"
	"strings"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"
)

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
