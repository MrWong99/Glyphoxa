package wirenpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
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
// Best-effort for the session: a failed post is logged (with the remedy) and the
// session continues, and the post is tried again on the next reconnect cycle.
// Transcription is disclosed here, but it is not gated on the disclosure
// landing — the Campaign's GM accepted the terms when they created it.
//
// The legacy standalone paths (-mode voice, -hardcoded) post the same notice
// even though they wire no transcript persistence: an accepted OVER-claim —
// telling a dev channel it is transcribed when nothing is stored is harmless in
// the direction that matters, and those paths are not the SaaS topology.

// transcriptionDisclosureContent is the always-on part of the notice: what is
// captured, why it is kept, and who can read it back.
const transcriptionDisclosureContent = "**This session is being transcribed.** " +
	"Everything spoken in this voice channel is transcribed and stored for this campaign, so the GM can " +
	"search it and get recaps later. Speak up if you would rather not be transcribed — the GM can end the session at any time."

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

// disclosurePostTimeout bounds the disclosure's REST call. The cycle posts it
// inline, so a Discord REST that never answers must not pin the cycle.
const disclosurePostTimeout = 10 * time.Second

// NoMentions is the AllowedMentions every Bot message carries: an empty parse
// list and no allowed ids, so nothing in the content pings anyone. Messages
// carry transcript-, LLM- or config-derived text, and an "@everyone" (or a
// user/role mention) in it must not reach the server. Discord parses every
// mention when allowed_mentions is absent, so it must be set explicitly.
func NoMentions() *discord.AllowedMentions {
	return &discord.AllowedMentions{Parse: []discord.AllowedMentionType{}}
}

// disclosureMessage builds the session-start disclosure message.
// withConsentButtons attaches the tape's Consent/Revoke action row (#306) — the
// buttons are stateless (the Campaign rides their custom ids), so the single
// message stays interactive for the whole session, including across reconnects.
func disclosureMessage(campaignID uuid.UUID, content string, withConsentButtons bool) discord.MessageCreate {
	msg := discord.MessageCreate{Content: content, AllowedMentions: NoMentions()}
	if withConsentButtons {
		msg.Components = []discord.LayoutComponent{
			discord.NewActionRow(
				discord.NewPrimaryButton("Consent", tapeGrantCustomID(campaignID)),
				discord.NewDangerButton("Revoke", tapeRevokeCustomID(campaignID)),
			),
		}
	}
	return msg
}

// postSessionDisclosure posts the session-start disclosure to the voice
// channel's text chat, bounded by ctx.
//
// It is a package var so tests substitute a spy and connectAndServe stays free
// of a live Discord REST.
var postSessionDisclosure = func(ctx context.Context, client *bot.Client, channel snowflake.ID, campaignID uuid.UUID, content string, withConsentButtons bool) error {
	if _, err := client.Rest.CreateMessage(channel, disclosureMessage(campaignID, content, withConsentButtons), rest.WithCtx(ctx)); err != nil {
		return fmt.Errorf("wirenpc: post session disclosure: %w", err)
	}
	return nil
}

// disclosureFailureRemedy is the operator-facing next step for a failed
// disclosure post. A permission rejection (403, or Discord's 50001 Missing
// Access / 50013 Missing Permissions) is the common one: the Bot was added with
// an invite that predates Send Messages, or the channel denies it.
func disclosureFailureRemedy(err error) string {
	var restErr *rest.Error
	if errors.As(err, &restErr) {
		forbidden := restErr.Response != nil && restErr.Response.StatusCode == http.StatusForbidden
		if forbidden || restErr.Code == rest.JSONErrorCodeMissingAccess || restErr.Code == rest.JSONErrorCodeLackPermissionsToPerformAction {
			return "give the Bot the View Channel and Send Messages permissions on this voice channel's text chat " +
				"(or re-add it with the invite link on the Configuration screen); retried on the next reconnect"
		}
	}
	return "retried on the next reconnect"
}

// discloseSession posts the session-start disclosure ONCE per Voice Session
// (#519): the always-on transcription notice, plus the Rollover Tape's consent
// disclosure and buttons when the Campaign is armed (#306), as a single message
// on the voice channel's text chat. Best-effort for the session — a failed post
// is logged and the session continues serving; capture stays consent-gated
// either way — but the guard arms only once a post LANDS, so a failure is tried
// again on the next reconnect cycle.
func discloseSession(ctx context.Context, client *bot.Client, channel snowflake.ID, cfg Config, disclosed *sessionOnce, log *slog.Logger) {
	disclosed.do(func() bool {
		tapeArmed := cfg.Tape != nil
		content := sessionDisclosureContent(cfg.PrivacyPolicyURL, tapeArmed)
		postCtx, cancel := context.WithTimeout(ctx, disclosurePostTimeout)
		defer cancel()
		if err := postSessionDisclosure(postCtx, client, channel, cfg.CampaignID, content, tapeArmed); err != nil {
			log.Error("players were not shown the transcription notice: posting it to the voice channel's text chat failed",
				"err", err, "channel", channel, "campaign", cfg.CampaignID, "tape_armed", tapeArmed,
				"remedy", disclosureFailureRemedy(err))
			return false
		}
		return true
	})
}

// sessionOnce guards a once-per-Voice-Session side effect across the reconnect
// cycles of one [Run] (#519). Run owns the value and hands a pointer to each
// cycle; cycles are strictly sequential on one goroutine (runWithReconnect), so
// no lock is needed. A nil receiver never fires — the paths that build a cycle
// outside Run stay unchanged.
type sessionOnce struct{ done bool }

// do runs f until it reports success, once per reconnect cycle at most: the
// guard arms only when f returns true, so a disclosure Discord rejected is
// retried on the next cycle (players must see it), while one that landed is
// never repeated. Cycles are backed off, so a persistent rejection costs one
// attempt per reconnect, not a spam loop.
func (o *sessionOnce) do(f func() bool) {
	if o == nil || o.done {
		return
	}
	o.done = f()
}
