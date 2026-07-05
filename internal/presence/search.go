package presence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/google/uuid"

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

// TranscriptSearch is the storage surface /glyphoxa search needs: the ONE shared
// search path (AC4 — the same storage.SearchTranscriptLines the web RPC calls)
// plus the stored Active Campaign fallback. The live Voice Session's campaign is
// resolved separately (activeCampaign), matching the web RPC's scope precedence.
type TranscriptSearch interface {
	SearchTranscriptLines(ctx context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.TranscriptLine, error)
	GetActiveCampaign(ctx context.Context) (storage.Campaign, error)
}

// SearchCommand builds the /glyphoxa search slash command (ADR-0010: GM-only,
// operator-allowlisted). It searches the operator's Active Campaign transcript via
// the SAME storage path the web uses (AC4) and quotes the top matches with speaker
// + timestamp (ADR-0011 amendment). The Campaign is resolved server-side — the
// live Voice Session's campaign (activeCampaign), else the stored Active Campaign —
// never client-supplied (AC5), so a search never crosses into another campaign.
//
// The DB search can exceed Discord's ~2.5s interaction deadline, so the handler
// Defers first (which stops the dispatch first-response watchdog) and then bounds
// its own DB work with transcriptSearchTimeout, because the post-Defer ctx no
// longer carries that deadline.
func SearchCommand(search TranscriptSearch, activeCampaign func() (uuid.UUID, bool)) Command {
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

			campaignID, ok, err := resolveSearchCampaign(dbCtx, search, activeCampaign)
			if err != nil {
				return fmt.Errorf("presence: resolve active campaign for search: %w", err)
			}
			if !ok {
				return ic.ReplyEphemeral("No Active Campaign yet — run `/glyphoxa use` to set one.")
			}

			lines, err := search.SearchTranscriptLines(dbCtx, campaignID, query, transcriptSearchLimit)
			if err != nil {
				return fmt.Errorf("presence: search transcript for campaign %s: %w", campaignID, err)
			}
			if len(lines) == 0 {
				return ic.ReplyEphemeral(fmt.Sprintf("No lines match %q.", query))
			}
			return ic.ReplyEphemeral(formatTranscriptMatches(query, lines))
		},
	}
}

// resolveSearchCampaign returns the Campaign to search, matching the web RPC's
// precedence: the live Voice Session's campaign first, else the stored Active
// Campaign. ok is false only when there is neither (never-run state), which the
// caller answers with the "/glyphoxa use" hint. A storage error other than
// ErrNotFound is propagated.
func resolveSearchCampaign(ctx context.Context, search TranscriptSearch, activeCampaign func() (uuid.UUID, bool)) (uuid.UUID, bool, error) {
	if activeCampaign != nil {
		if id, ok := activeCampaign(); ok {
			return id, true, nil
		}
	}
	c, err := search.GetActiveCampaign(ctx)
	if errors.Is(err, storage.ErrNotFound) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	return c.ID, true, nil
}

// formatTranscriptMatches renders the top matches as an ephemeral reply: a header
// plus each hit quoted with its speaker (and pill tag, e.g. "Bart (NPC)") and a
// UTC HH:MM:SS timestamp (ADR-0011 amendment: quotes lines with speaker +
// timestamp). UTC keeps it deterministic — the bot has no per-user timezone.
func formatTranscriptMatches(query string, lines []storage.TranscriptLine) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Top matches for %q:\n", query)
	for _, l := range lines {
		who := l.Who
		if l.Tag != "" {
			who = fmt.Sprintf("%s (%s)", l.Who, l.Tag)
		}
		fmt.Fprintf(&b, "**%s** · %s — %q\n", who, l.TS.UTC().Format("15:04:05"), l.Text)
	}
	return strings.TrimRight(b.String(), "\n")
}
