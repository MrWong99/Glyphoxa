package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Result budgets shared by the knowledge Tools (#296), mirroring the kgfacts
// discipline (internal/kgfacts): a hard ceiling on the whole result plus a
// per-item cap so no single oversized row blows the prompt budget. Pinned as
// consts, not magic numbers, so the bound is one edit away and testable.
const (
	// MaxToolResultChars bounds a knowledge Tool's whole rendered result. A
	// tool-role message is prompt-injected verbatim, so this keeps one tool call
	// from dominating the next generation's context regardless of DB size.
	MaxToolResultChars = 2000
	// MaxTranscriptLineRunes caps one rendered transcript line's spoken text, in
	// runes (rune-safe truncation, never a split codepoint).
	MaxTranscriptLineRunes = 300
	// DefaultSearchLimit is the row cap when the LLM omits "limit".
	DefaultSearchLimit = 5
	// MaxSearchLimit is the hard ceiling on "limit"; a larger request is clamped,
	// re-validated in the handler because the schema is advisory (ADR-0029).
	MaxSearchLimit = 10
)

// searchArgs is the shared decoded shape of the knowledge Tools' LLM args: a
// free-text query and an optional row limit. Both Tools declare the same schema.
type searchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// clampedLimit resolves the requested row limit against the defaults/ceiling.
// The schema is advisory; the model can emit anything, so the bound is enforced
// here (ADR-0029: never trust the model to honour constraints).
func clampedLimit(requested int) int {
	if requested <= 0 {
		return DefaultSearchLimit
	}
	if requested > MaxSearchLimit {
		return MaxSearchLimit
	}
	return requested
}

// searchInputSchema is the JSON Schema both knowledge Tools declare to the LLM.
var searchInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {
      "type": "string",
      "description": "The words to search for."
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 10,
      "description": "Maximum number of results to return (default 5)."
    }
  },
  "required": ["query"],
  "additionalProperties": false
}`)

// TranscriptSearch is the read-only transcript_search built-in (#296): a
// relevance search over the active Campaign's persisted transcript (ADR-0011
// tsvector path), campaign-scoped inside the adapter. It executes inline
// (ReadOnly) — the model needs the recalled lines to keep talking ("earlier you
// promised…"). It carries no per-grant scope: every Agent that holds the grant
// searches its own Campaign's transcript, no narrowing (SupportsScope false).
//
// A nil source means the Tool is registered but transcript retrieval is not
// wired in this mode; Execute then reports it is unavailable rather than panic.
type TranscriptSearch struct {
	src TranscriptSearcher
}

// NewTranscriptSearch builds the Tool over src. A nil src is allowed — the Tool
// registers but reports unavailable at Execute time (the standalone bench path).
func NewTranscriptSearch(src TranscriptSearcher) *TranscriptSearch {
	return &TranscriptSearch{src: src}
}

// Name implements [Tool].
func (*TranscriptSearch) Name() string { return "transcript_search" }

// Description implements [Tool].
func (*TranscriptSearch) Description() string {
	return "Search the transcript of this campaign's past sessions for what was said. " +
		"Use it to recall earlier conversations, promises, or events."
}

// InputSchema implements [Tool].
func (*TranscriptSearch) InputSchema() json.RawMessage { return searchInputSchema }

// ReadOnly implements [Tool]: a transcript search mutates no state (ADR-0030).
func (*TranscriptSearch) ReadOnly() bool { return true }

// SupportsScope implements [Tool]: transcript_search is campaign-scoped for
// everyone (the Campaign comes from the active session, not a grant), so it
// carries no narrowing config.
func (*TranscriptSearch) SupportsScope() bool { return false }

// Execute implements [Tool]. It searches the active Campaign's transcript for
// the query and renders the matches as numbered lines the LLM can read back. The
// Campaign is resolved inside the adapter (from the active session), never from
// the args — the model cannot search another Campaign. grantConfig is ignored
// (no scope). A nil source yields the unavailable error; no matches yields a
// friendly "none" line, not an error.
func (ts *TranscriptSearch) Execute(ctx context.Context, args json.RawMessage, _ any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if ts.src == nil {
		return "", fmt.Errorf("transcript_search: transcript retrieval is unavailable in this mode")
	}
	var a searchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("transcript_search: invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Query) == "" {
		return "", fmt.Errorf("transcript_search: query must not be empty")
	}

	hits, err := ts.src.SearchTranscript(ctx, a.Query, clampedLimit(a.Limit))
	if err != nil {
		return "", fmt.Errorf("transcript_search: %w", err)
	}
	return renderTranscriptHits(hits), nil
}

// renderTranscriptHits projects hits into numbered lines "N. [kind] who: text",
// each line's text rune-truncated to MaxTranscriptLineRunes, the whole result
// bounded to MaxToolResultChars — counted in RUNES to match the per-line rune cap
// (a byte count would let a multibyte-heavy result overrun the budget) — with a
// deterministic prefix-stop: a line that would overrun the budget, and every line
// after it, is dropped rather than the block skip-scanned. The FIRST line is
// ALWAYS emitted (one line's runes never exceed the budget), so a real match is
// never rendered as the "none" line. An empty set renders "none".
func renderTranscriptHits(hits []TranscriptHit) string {
	if len(hits) == 0 {
		return "no matching transcript lines"
	}
	var b strings.Builder
	runes := 0
	n := 0
	for _, h := range hits {
		line := fmt.Sprintf("%d. [%s] %s: %s", n+1, h.Kind, h.Who, truncateRunes(h.Text, MaxTranscriptLineRunes))
		add := line
		if n > 0 {
			add = "\n" + line
		}
		addRunes := utf8.RuneCountInString(add)
		if n > 0 && runes+addRunes > MaxToolResultChars {
			break // budget reached; the first line is always kept.
		}
		b.WriteString(add)
		runes += addRunes
		n++
	}
	return b.String()
}

// truncateRunes trims s to at most max runes, appending an ellipsis when it cut —
// rune-safe so a multibyte character is never split (mirrors kgfacts).
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
