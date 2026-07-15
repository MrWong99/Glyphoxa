package presence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/discordmsg"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// transcriptSearchLimit is how many top matches /glyphoxa search quotes back
// (ADR-0011 amendment: a short list of the most relevant lines). Kept small so
// the ephemeral reply stays readable; the web surfaces the full ranked set.
const transcriptSearchLimit = 5

// transcriptSearchTimeout bounds the DB search AFTER the interaction is Deferred.
// Post-Defer the dispatch watchdog is stopped, so the ctx has no deadline (that is
// the whole point of Defer, ADR-0010 amendment); a slow or stuck DB would
// otherwise hang the handler indefinitely, so the handler caps its own DB work.
const transcriptSearchTimeout = 10 * time.Second

// Discord rejects a message whose content exceeds 2000 characters (a Followup over
// it 400s and the GM sees nothing), so replies are bounded: discordMessageLimit is
// the hard cap ([discordmsg.Limit] — the shared splitter's source of truth),
// maxQuoteChars caps a single quoted line (a coalesced Agent reply is
// multi-sentence and can be long), and maxQueryEcho caps an echoed query (a string
// option can be up to 6000 chars).
const (
	discordMessageLimit = discordmsg.Limit
	maxQuoteChars       = 200
	maxQueryEcho        = 100
)

// SearchStore is the storage surface /glyphoxa search needs: the shared slash
// Active-Campaign resolution (SessionStore, reused by resolveActiveCampaign so the
// slash surface has ONE resolver, not a divergent copy — #216/#108) plus the ONE
// shared transcript search path (AC4 — the same storage.SearchTranscriptLines the
// web RPC calls). *storage.Store satisfies it; tests use a fake.
type SearchStore interface {
	SessionStore
	SearchTranscriptLines(ctx context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.TranscriptLine, error)
}

// SearchCommand builds the /glyphoxa search slash command (ADR-0010: GM-only,
// operator-allowlisted). It searches the operator's Active Campaign transcript via
// the SAME storage path the web uses (AC4) and quotes the top matches with speaker
// + timestamp (ADR-0011 amendment). The Campaign is resolved server-side by the
// SHARED slash resolver resolveActiveCampaign (ADR-0009 order: live Voice Session's
// campaign → the operator's durable /glyphoxa use selection → fail) — never
// client-supplied (AC5), and identical to /glyphoxa start so the two never diverge.
// There is deliberately no most-recently-created fallback on the slash surface (the
// GM has the /glyphoxa use affordance right there).
//
// The DB search can exceed Discord's ~2.5s interaction deadline, so the handler
// Defers first (which stops the dispatch first-response watchdog) and then bounds
// its own DB work with transcriptSearchTimeout, because the post-Defer ctx no
// longer carries that deadline.
func SearchCommand(store SearchStore, voice VoiceControl) Command {
	return Command{
		Path:        "glyphoxa search",
		Description: "Search the Active Campaign's transcript.",
		Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionString{
				Name:        "query",
				Description: "What to search the transcript for.",
				Required:    true,
			},
		},
		GMOnly: true,
		Handle: func(ctx context.Context, ic *Interaction) error {
			raw, _ := ic.String("query")
			query := strings.TrimSpace(raw)
			if query == "" {
				// A blank/whitespace query has nothing to search — a clear ephemeral
				// hint, no Defer and no DB round trip.
				return ic.ReplyEphemeral("Give me something to search for, e.g. `/glyphoxa search dragon`.")
			}

			// Defer (ephemeral): the DB search may run past the 3s window, and the
			// results are GM-only. After Defer the ctx has no deadline, so the DB work
			// below is bounded by our own timeout.
			if err := ic.Defer(true); err != nil {
				return err
			}
			dbCtx, cancel := context.WithTimeout(ctx, transcriptSearchTimeout)
			defer cancel()

			// The SAME resolver /glyphoxa start uses (no divergent copy): live session
			// → durable /glyphoxa use selection → ErrNoActiveCampaign.
			c, err := resolveActiveCampaign(dbCtx, store, voice, ic.UserID())
			if errors.Is(err, ErrNoActiveCampaign) {
				return ic.ReplyEphemeral("No Active Campaign yet — run /glyphoxa use campaign:<name> first.")
			}
			if err != nil {
				return fmt.Errorf("presence: resolve active campaign for search: %w", err)
			}

			lines, err := store.SearchTranscriptLines(dbCtx, c.ID, query, transcriptSearchLimit)
			if err != nil {
				return fmt.Errorf("presence: search transcript for campaign %s: %w", c.ID, err)
			}
			if len(lines) == 0 {
				return ic.ReplyEphemeral(fmt.Sprintf("No lines match %q.", truncateRunes(query, maxQueryEcho)))
			}
			return ic.ReplyEphemeral(formatTranscriptMatches(query, lines))
		},
	}
}

// formatTranscriptMatches renders the top matches as an ephemeral reply: a header
// plus each hit quoted with its speaker (and pill tag, e.g. "Bart (NPC)") and a
// UTC HH:MM:SS timestamp (ADR-0011 amendment: quotes lines with speaker +
// timestamp). UTC keeps it deterministic — the bot has no per-user timezone. Every
// quoted line is truncated (a coalesced Agent reply can be long) and the whole
// reply is kept under Discord's 2000-char cap: a line that would push it over is
// dropped rather than risking a 400 that hides ALL the matches from the GM.
func formatTranscriptMatches(query string, lines []storage.TranscriptLine) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Top matches for %q:", truncateRunes(query, maxQueryEcho))
	for _, l := range lines {
		who := l.Who
		if l.Tag != "" {
			who = fmt.Sprintf("%s (%s)", l.Who, l.Tag)
		}
		line := fmt.Sprintf("\n**%s** · %s — %q", who, l.TS.UTC().Format("15:04:05"), truncateRunes(l.Text, maxQuoteChars))
		if b.Len()+len(line) > discordMessageLimit {
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

// truncateRunes clips s to at most max runes (never bytes — German campaigns), so
// a long line or query can't blow the Discord content cap; a clipped value gets a
// trailing ellipsis to signal the cut.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
