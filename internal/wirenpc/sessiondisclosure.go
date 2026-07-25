package wirenpc

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"
)

// Voice Session transcription disclosure (#519).
//
// Players at the table are data subjects who may never open the web app: the
// voice channel is the ONLY surface that reaches them. Transcription, however,
// is always-on for every Voice Session (ADR-0011/ADR-0040 — every utterance
// becomes Transcript Lines and Chunks persisted for the Campaign), and until
// now only the OPT-IN Rollover Tape disclosed anything (#306, ADR-0051).
//
// So on Voice Session start the Bot posts one disclosure to the bound text
// surface — the voice channel's own text chat, the same channel the tape
// disclosure uses. When the Campaign is also tape-armed the two are COMBINED
// into a single message (the consent buttons follow the transcription notice)
// rather than posted twice.
//
// Once per Voice Session, not once per reconnect cycle: [connectAndServe] runs
// again on every Discord reconnect (#44), and a channel filling with identical
// notices is its own kind of noise. [sessionOnce] carries that across the
// cycles of one Run.
//
// Best-effort throughout: a failed post is logged and the session continues.
// Transcription is disclosed here, but it is not gated on the disclosure
// landing — the Campaign's GM accepted the terms when they created it.

// transcriptionDisclosureContent is the always-on part of the notice: what is
// captured, why it is kept, and who can read it back.
const transcriptionDisclosureContent = "**This session is being transcribed.** " +
	"Everything spoken in this voice channel is transcribed and stored for this campaign, so the GM can " +
	"search it and get recaps later. Speak up if you would rather not be recorded — the GM can end the session at any time."

// transcriptionDisclosure renders the notice, appending the deployment's
// privacy-policy URL when one is configured (GLYPHOXA_PRIVACY_POLICY_URL). A
// deployment that has not published a policy posts the notice without a link
// rather than a broken one — the disclosure itself is the point.
func transcriptionDisclosure(privacyPolicyURL string) string {
	if u := strings.TrimSpace(privacyPolicyURL); u != "" {
		return transcriptionDisclosureContent + " Privacy policy: <" + u + ">"
	}
	return transcriptionDisclosureContent
}

// sessionDisclosureContent is the ONE message posted at Voice Session start: the
// transcription notice, plus — when the Campaign is tape-armed — the Rollover
// Tape's consent disclosure beneath it (#306). The caller attaches the tape's
// Consent/Revoke buttons under exactly the same condition, so a tape-armed
// session still gets its consent flow, in one message instead of two.
func sessionDisclosureContent(privacyPolicyURL string, tapeArmed bool) string {
	content := transcriptionDisclosure(privacyPolicyURL)
	if tapeArmed {
		content += "\n\n" + tapeDisclosureContent
	}
	return content
}

// postSessionDisclosure posts the session-start disclosure to the voice
// channel's text chat. withConsentButtons attaches the tape's Consent/Revoke
// action row (#306) — the buttons are stateless (the Campaign rides their custom
// ids), so the single message stays interactive for the whole session, including
// across reconnects.
//
// It is a package var so tests substitute a spy and connectAndServe stays free
// of a live Discord REST.
var postSessionDisclosure = func(ctx context.Context, client *bot.Client, channel snowflake.ID, campaignID uuid.UUID, content string, withConsentButtons bool) error {
	msg := discord.MessageCreate{Content: content}
	if withConsentButtons {
		msg.Components = []discord.LayoutComponent{
			discord.NewActionRow(
				discord.NewPrimaryButton("Consent", tapeGrantCustomID(campaignID)),
				discord.NewDangerButton("Revoke", tapeRevokeCustomID(campaignID)),
			),
		}
	}
	if _, err := client.Rest.CreateMessage(channel, msg); err != nil {
		return fmt.Errorf("wirenpc: post session disclosure: %w", err)
	}
	return nil
}

// discloseSession posts the session-start disclosure ONCE per Voice Session
// (#519): the always-on transcription notice, plus the Rollover Tape's consent
// disclosure and buttons when the Campaign is armed (#306), as a single message
// on the voice channel's text chat. Best-effort — a failed post is logged and
// the session continues serving; capture stays consent-gated either way.
func discloseSession(ctx context.Context, client *bot.Client, channel snowflake.ID, cfg Config, disclosed *sessionOnce, log *slog.Logger) {
	disclosed.do(func() {
		tapeArmed := cfg.Tape != nil
		content := sessionDisclosureContent(cfg.PrivacyPolicyURL, tapeArmed)
		if err := postSessionDisclosure(ctx, client, channel, cfg.CampaignID, content, tapeArmed); err != nil {
			log.Warn("post session disclosure", "err", err, "tape_armed", tapeArmed)
		}
	})
}

// sessionOnce guards a once-per-Voice-Session side effect across the reconnect
// cycles of one [Run] (#519). Run owns the value and hands a pointer to each
// cycle; cycles are strictly sequential on one goroutine (runWithReconnect), so
// no lock is needed. A nil receiver never fires — the paths that build a cycle
// outside Run stay unchanged.
type sessionOnce struct{ done bool }

// do runs f the first time it is called, and never again. It arms BEFORE
// running f: a best-effort side effect that fails is not retried on the next
// reconnect cycle (the disclosure is "exactly once per session", and a Discord
// REST that rejected the post is likelier to spam than to succeed later).
func (o *sessionOnce) do(f func()) {
	if o == nil || o.done {
		return
	}
	o.done = true
	f()
}
