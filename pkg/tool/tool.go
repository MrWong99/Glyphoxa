// Package tool is Glyphoxa v2's internal Tool framework: one uniform Tool
// interface, a dumb in-process Registry, and the generic tool-use loop that
// drives an LLM through tool calls (ADR-0028/0029/0030).
//
// A Tool's backing — built-in (in-process Go, lowest latency) or a future MCP
// Server (out-of-process) — is hidden behind the [Tool] interface; consumers
// (the Agent loop, the orchestrator) only ever see Tools. The framework is a
// reusable building block with no dependency on the voice orchestrator: the
// Agent loop assembles its prompt, then hands messages + granted tools to
// [Loop.Run] and gets back the LLM's final text.
//
// v1.0 ships exactly one Tool — [Dice] — and only the read-only inline
// execution path of ADR-0030. The side-effecting / deferred-to-turn-commit
// path is deliberately not built (no side-effecting Tool exists yet); a Tool
// that reports it is not read-only is rejected by the loop rather than
// silently inlined. See package-level ADR references.
package tool

import (
	"context"
	"encoding/json"
)

// Tool is the single internal interface every callable presents, regardless of
// backing (ADR-0028). The LLM is shown [Tool.Name], [Tool.Description], and
// [Tool.InputSchema]; the loop calls [Tool.Execute] when the model emits a
// matching tool_call.
type Tool interface {
	// Name is the stable identifier the LLM uses to call the Tool and the key
	// it is registered under. Must be unique within a [Registry].
	Name() string

	// Description is the natural-language summary declared to the LLM so it
	// knows when to call the Tool.
	Description() string

	// InputSchema is the JSON Schema for the Tool's arguments, declared to the
	// LLM and used (by the model) to shape the args it emits. Returning nil or
	// an empty schema means "no arguments".
	InputSchema() json.RawMessage

	// ReadOnly reports whether the Tool only reads state (ADR-0030). Read-only
	// Tools (dice, future query_knowledge) execute inline during generation and
	// are safe to speculate. A Tool that is *not* read-only must defer its
	// effect to turn-commit; that machinery is not built in v1.0, so the loop
	// refuses to execute a non-read-only Tool inline rather than mutate state
	// from a possibly-discarded draft.
	ReadOnly() bool

	// SupportsScope reports whether a per-grant Config can NARROW this Tool's
	// authority for one Agent (ADR-0029) — the bit the grant editor keys its
	// scope UI off. A Tool that supports a scope (a future remember_knowledge
	// granted "only about yourself" vs campaign-wide) exposes a scope editor; one
	// that does not (dice carries no config) is a plain on/off grant. It is a
	// declaration ABOUT the grant config, independent of ReadOnly: the LLM never
	// sees the scope, and the handler still enforces whatever Config it receives.
	SupportsScope() bool

	// Execute runs the Tool with the LLM-supplied args and the caller's
	// per-grant config (ADR-0029). grantConfig narrows the Tool's authority for
	// this Agent and is enforced here, in the handler, never by the LLM — the
	// model cannot widen its scope by crafting clever args. grantConfig is nil
	// when the grant carries no config (dice's always is). The returned string
	// is fed back to the LLM as the tool-role result. ctx is the turn's
	// context: honoring its cancellation lets barge-in tear down an in-flight
	// call (ADR-0030).
	Execute(ctx context.Context, args json.RawMessage, grantConfig any) (string, error)
}
