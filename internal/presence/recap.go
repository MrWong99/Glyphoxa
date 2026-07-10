package presence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/recap"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// recapOpTimeout bounds the deferred recap work. After a Defer the dispatch
// first-response watchdog is stopped, so the interaction ctx no longer carries
// Discord's 3s deadline; the LLM recap — which fans out map-reduce windows and can
// run for many seconds — runs under this explicit budget instead of an open-ended
// one, well inside Discord's minutes-long follow-up window. A slow or stuck LLM
// call is cut off here and surfaces as the friendly "took too long" reply (#271).
// It is a var (not a const) so a test can shrink it to assert the timeout
// behaviorally without a 120s wait; production keeps 120s.
var recapOpTimeout = 120 * time.Second

// recapListLimit caps how many recent Voice Sessions the picker scans for a
// recappable (ended, non-empty) row and the autocomplete offers before its own
// 25-choice cap. 50 matches the web past-session picker's server policy
// (rpc.listSessionsLimit, #270) so the slash surface sees the same window — enough
// that a pageful of running/failed/empty rows can't hide a real recappable one.
const recapListLimit = 50

// Delivery choice values for /glyphoxa recap (#271 decision 6). They live HERE, in
// the presence tier that owns the slash surface — deliberately NOT in proto, since
// the RPC recap (#274) is a separate surface that does not share this Discord-only
// delivery vocabulary.
const (
	deliveryVoiced    = "voiced"    // Butler voices it in the voice channel (degrades to public text in v1)
	deliveryPublic    = "public"    // public in-channel text
	deliveryEphemeral = "ephemeral" // GM-only ephemeral text — the DEFAULT
)

// voicedDegradeHint prefixes a voiced recap that could not actually be voiced
// (no ButlerVoicer wired, or no live session) — it is delivered as public text
// with this explanation instead of silently changing modes (#271 decision 6a).
const voicedDegradeHint = "Voiced recap needs the Butler in the voice channel — text instead:"

// RecapEngine is the one-shot recap service the command drives: given Voice Session
// ids it renders their transcript and returns a Butler-flavoured recap (#272).
// *recap.Engine satisfies it; tests use a fake.
type RecapEngine interface {
	Recap(ctx context.Context, sessionIDs []uuid.UUID) (recap.Result, error)
}

// RecapStore is the storage surface /glyphoxa recap needs: the shared slash
// Active-Campaign resolver (SessionStore, so the recap surface resolves the
// campaign identically to /glyphoxa start/search — never a divergent copy) plus
// the Voice Session reads the picker uses (one by id for an explicit pick, and a
// recent list for the default latest-ended pick + autocomplete). *storage.Store
// satisfies it; tests use a fake.
type RecapStore interface {
	SessionStore
	GetVoiceSession(ctx context.Context, id uuid.UUID) (storage.VoiceSession, error)
	ListVoiceSessions(ctx context.Context, campaignID uuid.UUID, limit int) ([]storage.VoiceSession, error)
}

// ButlerVoicer is the decision-6a socket for a truly VOICED recap: speaking the
// recap prose into the live voice channel as the Butler. NOBODY implements it in
// this epic — the Butler is address-only and has never been voiced (ADR-0009/0024),
// so the wiring passes nil and a `voiced` request degrades to public text with a
// hint. Epic 7 fills this in.
type ButlerVoicer interface {
	SpeakAsButler(ctx context.Context, text string) error
}

// compile-time proofs the concrete store/engine satisfy the seams.
var (
	_ RecapStore  = (*storage.Store)(nil)
	_ RecapEngine = (*recap.Engine)(nil)
)

// RecapCommand builds `/glyphoxa recap` (ADR-0010: GM-only, operator-allowlisted).
// With no options it recaps the LATEST ENDED Voice Session of the operator's Active
// Campaign — resolved by the SHARED strict slash resolver (ADR-0009: live session's
// campaign → durable /glyphoxa use selection → fail; no most-recently-created
// fallback). An explicit `session` option recaps that session, provided it belongs
// to the Active Campaign. `delivery` picks how the recap is returned (#271 decision
// 6): voiced by the Butler (degrades to public text today), public text, or GM-only
// ephemeral text (the default). A recap over Discord's 2000-char cap is delivered as
// multiple ordered Followups, never truncated.
//
// It Defers first — the LLM recap can run well past Discord's 3s deadline — so the
// defer visibility is decided from the delivery mode BEFORE the Defer, keeping the
// "thinking…" placeholder's visibility matched to the final reply's.
func RecapCommand(store RecapStore, voice VoiceControl, eng RecapEngine, butler ButlerVoicer) Command {
	return Command{
		Path:        "glyphoxa recap",
		Description: "Recap a Voice Session of the Active Campaign.",
		Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionString{
				Name:         "session",
				Description:  "Which session to recap (defaults to the latest one with a transcript).",
				Required:     false,
				Autocomplete: true,
			},
			discord.ApplicationCommandOptionString{
				Name:        "delivery",
				Description: "How to deliver the recap (default: only me).",
				Required:    false,
				Choices: []discord.ApplicationCommandOptionChoiceString{
					{Name: "Voice channel (Butler)", Value: deliveryVoiced},
					{Name: "Public text", Value: deliveryPublic},
					{Name: "Only me", Value: deliveryEphemeral},
				},
			},
		},
		GMOnly: true,
		Handle: func(ctx context.Context, ic *Interaction) error {
			return handleRecap(ctx, ic, store, voice, eng, butler)
		},
		Autocomplete: func(ctx context.Context, ac *Autocomplete) ([]discord.AutocompleteChoice, error) {
			c, err := resolveActiveCampaign(ctx, store, voice, ac.UserID())
			if errors.Is(err, ErrNoActiveCampaign) {
				// No Active Campaign yet → offer nothing (empty picker); not an error.
				return nil, nil
			}
			if err != nil {
				// A real resolver/DB failure is returned so the Registry logs it rather
				// than being silently swallowed as an empty picker (finding 6).
				return nil, err
			}
			sessions, err := store.ListVoiceSessions(ctx, c.ID, recapListLimit)
			if err != nil {
				return nil, err
			}
			_, typed := ac.Focused()
			return recapSessionChoices(sessions, typed), nil
		},
	}
}

// handleRecap runs one /glyphoxa recap interaction. It ALWAYS Defers ephemerally:
// the placeholder is then GM-only on every path, so an error (which is always an
// ephemeral reply) never leaves a dangling PUBLIC "thinking…" for the whole channel.
// A public/voiced-degraded SUCCESS is delivered as a PUBLIC Followup instead — a
// followup carries its own visibility independent of the ephemeral placeholder. The
// pick + engine call run under recapOpTimeout post-Defer (the Defer stopped the
// first-response watchdog).
func handleRecap(ctx context.Context, ic *Interaction, store RecapStore, voice VoiceControl, eng RecapEngine, butler ButlerVoicer) error {
	delivery := normalizeDelivery(ic)
	sessionOpt, _ := ic.String("session")
	sessionOpt = strings.TrimSpace(sessionOpt)

	// A `voiced` request can only truly voice when a ButlerVoicer is wired AND a
	// session is live; otherwise it degrades to public text (decision 6a). Known here
	// without a DB round trip. A successful text reply is PUBLIC for `public` and for
	// a degraded `voiced`, else GM-only ephemeral.
	_, live := voice.Snapshot()
	voiceMode := delivery == deliveryVoiced && butler != nil && live
	publicReply := !voiceMode && (delivery == deliveryPublic || delivery == deliveryVoiced)

	if err := ic.Defer(true); err != nil {
		return fmt.Errorf("presence: defer recap: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, recapOpTimeout)
	defer cancel()

	c, err := resolveActiveCampaign(ctx, store, voice, ic.UserID())
	if errors.Is(err, ErrNoActiveCampaign) {
		return ic.ReplyEphemeral("No Active Campaign yet — run /glyphoxa use campaign:<name> first.")
	}
	if err != nil {
		return fmt.Errorf("presence: resolve active campaign for recap: %w", err)
	}

	sessionID, ok, err := pickRecapSession(ctx, ic, store, c.ID, sessionOpt)
	if err != nil {
		return err
	}
	if !ok {
		return nil // pickRecapSession already sent the ephemeral reason
	}

	res, err := eng.Recap(ctx, []uuid.UUID{sessionID})
	switch {
	case errors.Is(err, recap.ErrNoTranscript):
		// A normal state (an empty ended session, or one the default filter let
		// through by a race) — not a failure, so no ERROR log, just a plain reason.
		return ic.ReplyEphemeral("That session has no transcript to recap.")
	case errors.Is(err, context.DeadlineExceeded):
		// The recapOpTimeout fired: a slow/stuck LLM. Friendly, no raw error, no ERROR
		// log (an expected slow-path, not a bug).
		return ic.ReplyEphemeral("The recap took too long to put together — try again in a moment.")
	case err != nil:
		// Unexpected failure: the Registry answers the returned error with a generic
		// ephemeral message via Followup (the ACK is already sent) and logs the wrapped
		// cause. No raw error text ever reaches the GM.
		return fmt.Errorf("presence: recap session %s: %w", sessionID, err)
	}

	return deliverRecap(ctx, ic, butler, voiceMode, delivery, publicReply, res.Text)
}

// pickRecapSession resolves which Voice Session to recap. An explicit `session`
// option must parse as a UUID and belong to the Active Campaign, else an ephemeral
// error is sent and the engine is NOT called (ok=false); its STATUS is not
// constrained — an explicit id may target any session, including a running one for a
// partial recap, matching #274's GenerateRecap RPC so the two surfaces don't diverge.
// With no option it picks the campaign's latest RECAPPABLE session — the newest
// ENDED row with a non-empty transcript (line_count, written at close, skips a
// running row on top AND an empty ended row, un-hiding an older real session). NOT
// GetLatestVoiceSession, which returns the running one while live.
func pickRecapSession(ctx context.Context, ic *Interaction, store RecapStore, campaignID uuid.UUID, sessionOpt string) (uuid.UUID, bool, error) {
	if sessionOpt != "" {
		id, perr := uuid.Parse(sessionOpt)
		if perr != nil {
			return uuid.UUID{}, false, ic.ReplyEphemeral("I couldn't read that session id — pick one from the autocomplete list.")
		}
		vs, err := store.GetVoiceSession(ctx, id)
		if errors.Is(err, storage.ErrNotFound) || (err == nil && vs.CampaignID != campaignID) {
			return uuid.UUID{}, false, ic.ReplyEphemeral("That session isn't part of your Active Campaign.")
		}
		if err != nil {
			return uuid.UUID{}, false, fmt.Errorf("presence: load voice session %s for recap: %w", id, err)
		}
		return vs.ID, true, nil
	}

	sessions, err := store.ListVoiceSessions(ctx, campaignID, recapListLimit)
	if err != nil {
		return uuid.UUID{}, false, fmt.Errorf("presence: list voice sessions for recap: %w", err)
	}
	for _, vs := range sessions {
		if isRecappable(vs) {
			return vs.ID, true, nil
		}
	}
	return uuid.UUID{}, false, ic.ReplyEphemeral("No recappable session found among the recent sessions of this campaign.")
}

// isRecappable reports whether a Voice Session can seed a default/autocomplete recap:
// it must have ENDED and have a recorded transcript (line_count > 0). A running,
// failed, or empty-ended session is excluded from the automatic pick — though an
// explicit id may still target one (see pickRecapSession).
func isRecappable(vs storage.VoiceSession) bool {
	return vs.Status == storage.VoiceSessionEnded && vs.LineCount > 0
}

// deliverRecap routes the finished recap prose to its chosen surface. A voiced recap
// is spoken by the Butler then confirmed ephemerally; a voiced request that could not
// be voiced (the common v1 case) is degraded to public text with a leading hint. Text
// delivery — public or ephemeral — is sent as one or more ordered Followups, each
// kept under Discord's 2000-char cap (never truncated, #271).
func deliverRecap(ctx context.Context, ic *Interaction, butler ButlerVoicer, voiceMode bool, delivery string, publicReply bool, text string) error {
	if voiceMode {
		if err := butler.SpeakAsButler(ctx, text); err != nil {
			return fmt.Errorf("presence: voice recap via butler: %w", err)
		}
		// The confirmation is GM-only, matching the ephemeral placeholder. As the first
		// post-Defer reply it lands as the placeholder edit (registry-wide rule, #335) at
		// the Defer's ephemeral visibility — exactly right.
		return ic.Followup("Recap voiced in the voice channel.", true)
	}

	body := text
	if delivery == deliveryVoiced {
		// Could not voice it — deliver as public text, explaining the switch.
		body = voicedDegradeHint + "\n\n" + text
	}
	parts := splitFollowups(body, discordMessageLimit)

	if publicReply {
		// The placeholder is ephemeral (we always Defer ephemerally) and a public recap
		// must not land in it — Discord fixes the original-response edit to the Defer's
		// visibility, so the channel would see nothing. Consume the placeholder FIRST
		// with a short GM-only note, THEN post the recap as real PUBLIC followups. The
		// registry-wide post-Defer rule (#335) makes the first reply the placeholder edit
		// on its own, so this is a plain ReplyEphemeral — no manual EditOriginal.
		if err := ic.ReplyEphemeral("Recap posted below."); err != nil {
			return err
		}
		for _, part := range parts {
			if err := ic.Followup(part, false); err != nil {
				return err
			}
		}
		return nil
	}

	// Ephemeral delivery: the placeholder is already ephemeral, so the first post-Defer
	// message resolves it via EditOriginal (registry-wide rule, #335) and the rest are
	// ephemeral followups — all GM-only, same visibility.
	for _, part := range parts {
		if err := ic.Followup(part, true); err != nil {
			return err
		}
	}
	return nil
}

// normalizeDelivery reads the `delivery` option, defaulting an absent or unknown
// value to the GM-only ephemeral mode (the safe default, #271).
func normalizeDelivery(ic *Interaction) string {
	v, _ := ic.String("delivery")
	switch strings.TrimSpace(v) {
	case deliveryVoiced:
		return deliveryVoiced
	case deliveryPublic:
		return deliveryPublic
	default:
		return deliveryEphemeral
	}
}

// splitFollowups breaks text into ordered chunks each at most limit RUNES (never
// bytes — German recaps), preferring to break on a newline, then a space, so a chunk
// ends at a natural boundary rather than mid-word; only a single unbroken run longer
// than limit is hard-cut. The boundary whitespace is dropped so it does not lead the
// next chunk. Concatenating the chunks (re-inserting a single break) reproduces the
// text in order. Never truncates: every rune is delivered (#271).
func splitFollowups(text string, limit int) []string {
	if limit <= 0 {
		return []string{text}
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}
	var parts []string
	for len(runes) > limit {
		cut := limit
		if i := lastBreakRune(runes, limit, '\n'); i > 0 {
			cut = i
		} else if i := lastBreakRune(runes, limit, ' '); i > 0 {
			cut = i
		}
		parts = append(parts, string(runes[:cut]))
		rest := runes[cut:]
		// Drop the boundary whitespace we broke on (not a hard mid-run cut).
		if cut < limit && len(rest) > 0 && (rest[0] == '\n' || rest[0] == ' ') {
			rest = rest[1:]
		}
		runes = rest
	}
	if len(runes) > 0 {
		parts = append(parts, string(runes))
	}
	return parts
}

// lastBreakRune returns the last index in [1, limit) where runes[i]==ch, or -1. The
// lower bound of 1 avoids an empty leading chunk when a break sits at index 0.
func lastBreakRune(runes []rune, limit int, ch rune) int {
	for i := limit - 1; i > 0; i-- {
		if runes[i] == ch {
			return i
		}
	}
	return -1
}

// recapSessionChoices builds the /glyphoxa recap `session` autocomplete: the Active
// Campaign's RECAPPABLE Voice Sessions only — ended, with a recorded transcript
// (running/failed/empty rows are excluded; an explicit id may still target any of
// them). Each choice is LABELLED "2006-01-02 15:04 · N lines" and CARRIES the session
// UUID as its value. Capped at Discord's 25-choice limit. When the GM has typed,
// choices whose label contains the typed text (case-insensitive) are kept.
func recapSessionChoices(sessions []storage.VoiceSession, typed string) []discord.AutocompleteChoice {
	needle := strings.ToLower(strings.TrimSpace(typed))
	choices := make([]discord.AutocompleteChoice, 0, len(sessions))
	for _, vs := range sessions {
		if !isRecappable(vs) {
			continue
		}
		label := fmt.Sprintf("%s · %d lines", vs.StartedAt.UTC().Format(sessionLabelTimeFormat), vs.LineCount)
		if needle != "" && !strings.Contains(strings.ToLower(label), needle) {
			continue
		}
		choices = append(choices, discord.AutocompleteChoiceString{Name: label, Value: vs.ID.String()})
		if len(choices) == discordChoiceLimit {
			break
		}
	}
	return choices
}

// sessionLabelTimeFormat is the UTC day+minute stamp shown in the recap session
// picker; UTC keeps it deterministic (the bot has no per-user timezone).
const sessionLabelTimeFormat = "2006-01-02 15:04"
