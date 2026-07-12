package tool

import (
	"context"
	"encoding/json"
	"fmt"
)

// Recap Tool budgets/defaults (#372, #297 decision 5). The recap prose is a whole
// session's condensed narrative, so its ceiling is far larger than the knowledge
// Tools' per-row budget — but still bounded so one recall can't swamp the next
// generation's context. Pinned as consts (not magic numbers) so the bounds are one
// edit away and testable.
const (
	// DefaultRecapSessions is the session count when the LLM omits "sessions".
	DefaultRecapSessions = 1
	// MaxRecapSessions is the hard ceiling on "sessions"; a larger request is
	// clamped in the handler (the schema is advisory, ADR-0029).
	MaxRecapSessions = 3
	// RecapResultBudgetRunes bounds the whole rendered recap, counted in RUNES so a
	// multibyte-heavy (German) recap is never over-budget nor split mid-codepoint.
	// Sized for HONEST end-to-end deliverability, not the raw recap length: the
	// Butler RELAYS this text through its own answer completion, capped at
	// groq.DefaultMaxTokens (1024 ≈ ~3.5k runes of German). A larger budget would
	// let a near-budget recap get clipped mid-prose by the relay despite the "do not
	// shorten" instruction. So the Tool truncates to what the relay can actually
	// carry; the full untruncated recap stays available via /glyphoxa recap (the
	// slash surface splits it across ordered followups, #271). The Description tells
	// the model as much.
	RecapResultBudgetRunes = 3500
)

// recapInputSchema is the JSON Schema recap declares to the LLM. "sessions" is
// optional (no required fields): the model asks WHAT window to recap, never WHICH
// session id — session selection lives entirely in the adapter (ADR-0029), so the
// model can never name another Campaign's session.
var recapInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "sessions": {
      "type": "integer",
      "minimum": 1,
      "maximum": 3,
      "description": "How many most recent ended sessions to recap (default 1)."
    }
  },
  "additionalProperties": false
}`)

// recapArgs is the decoded shape of recap's LLM args: an optional session count.
type recapArgs struct {
	Sessions int `json:"sessions"`
}

// clampedRecapSessions resolves the requested session count against the
// default/ceiling. The schema is advisory; the model can emit anything, so the
// bound is enforced here (ADR-0029: never trust the model to honour constraints).
func clampedRecapSessions(requested int) int {
	if requested <= 0 {
		return DefaultRecapSessions
	}
	if requested > MaxRecapSessions {
		return MaxRecapSessions
	}
	return requested
}

// Recap is the read-only recap built-in (#372, #297 decision 5): a Butler-flavoured
// summary of this Campaign's most recent ended Voice Session(s). It wraps the recap
// service behind the storage-free [Recapper] seam so pkg/tool never imports the
// recap engine or storage. The session window is resolved INSIDE the adapter (active
// campaign → newest ended non-empty rows), never from the LLM args (ADR-0029), so
// the model can never recap another Campaign's session.
//
// It executes inline (ReadOnly, ADR-0030): the Butler needs the recap prose in hand
// to relay it in the same turn. It carries no per-grant scope (SupportsScope false):
// the Campaign comes from the active session, not a grant.
//
// A nil source means the Tool is registered but recap is not wired in this mode
// (the standalone bench, the grant-editor RPC); Execute then reports it is
// unavailable rather than panic.
type Recap struct {
	src Recapper
}

// NewRecap builds the Tool over src. A nil src is allowed — the Tool registers but
// reports unavailable at Execute time (the zero-Deps modes).
func NewRecap(src Recapper) *Recap {
	return &Recap{src: src}
}

// Name implements [Tool].
func (*Recap) Name() string { return "recap" }

// Description implements [Tool].
func (*Recap) Description() string {
	return "Summarize what happened in this campaign's most recent ended voice session(s). " +
		"Use when asked to recap the last session. " +
		"Relay the returned recap to the players; do not shorten it. " +
		"A very long recap may be trimmed to fit a spoken reply; for the full text, the GM can run /glyphoxa recap."
}

// InputSchema implements [Tool].
func (*Recap) InputSchema() json.RawMessage { return recapInputSchema }

// ReadOnly implements [Tool]: a recap reads transcripts and mutates nothing
// (ADR-0030), so the loop runs it inline.
func (*Recap) ReadOnly() bool { return true }

// SupportsScope implements [Tool]: recap is campaign-scoped for everyone (the
// Campaign comes from the active session, not a grant), so it carries no narrowing
// config.
func (*Recap) SupportsScope() bool { return false }

// Execute implements [Tool]. It recaps the active Campaign's most recent ended
// session(s) and returns the recap prose for the LLM to relay. The session window
// is resolved inside the adapter (from the active session), never from the args —
// the model cannot recap another Campaign. grantConfig is ignored (no scope). A nil
// source yields the unavailable error; the result is rune-truncated to
// RecapResultBudgetRunes so one recall can't swamp the prompt budget.
func (r *Recap) Execute(ctx context.Context, args json.RawMessage, _ any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if r.src == nil {
		return "", fmt.Errorf("recap: recap is unavailable in this mode")
	}
	var a recapArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("recap: invalid arguments: %w", err)
	}
	text, err := r.src.RecapLastSessions(ctx, clampedRecapSessions(a.Sessions))
	if err != nil {
		return "", fmt.Errorf("recap: %w", err)
	}
	return truncateRunes(text, RecapResultBudgetRunes), nil
}
