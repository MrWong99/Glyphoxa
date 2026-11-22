package anthropic

import "strings"

// ThinkingMode selects the Messages API `thinking.type` a request carries.
type ThinkingMode string

const (
	// ThinkingUnset sends no `thinking` field: the model runs its own default.
	// It is the zero value, so a model no policy names keeps today's request.
	ThinkingUnset ThinkingMode = ""

	// ThinkingAdaptive sends {"type":"adaptive"}: the model decides per request
	// whether to think. Rejected (400) by claude-haiku-4-5.
	ThinkingAdaptive ThinkingMode = "adaptive"

	// ThinkingDisabled sends {"type":"disabled"}. On claude-haiku-5-5 this was
	// measured to make the model write narration or reasoning as visible text in
	// front of every tool call, which a voice would speak — so it is a bench arm,
	// never the default (#702).
	ThinkingDisabled ThinkingMode = "disabled"
)

// Thinking is the per-model reasoning configuration of one request: the
// `thinking` mode and the `output_config.effort` level. The zero value sends
// neither field.
type Thinking struct {
	// Mode is the `thinking.type`; [ThinkingUnset] omits the field.
	Mode ThinkingMode

	// Effort is the `output_config.effort` level ("low", "medium", "high", …);
	// "" omits the field. claude-haiku-4-5 rejects it (400).
	Effort string
}

// HaikuVoiceModel is the Claude model a voice Agent gets when an Anthropic
// Provider Config names none (#702). It is not [DefaultModel]: that one is the
// adapter's fallback for a request with no model at all, which the off-session
// callers (recap, planning chat) rely on.
const HaikuVoiceModel = "claude-haiku-5-5"

// DefaultThinking is the adapter's built-in per-model policy, from the live
// measurements recorded for #702:
//
//   - claude-haiku-5-5 → adaptive thinking at effort low. Adaptive skips itself
//     on plain chat (same first-token time as thinking disabled) and produces
//     clean tool calls; the API's own default, effort medium, costs about a
//     second on every chat turn.
//   - everything else → nothing. claude-haiku-4-5 answers 400 to both fields,
//     and any other model keeps the request it has today, byte for byte, so the
//     recorded cassettes stay valid.
func DefaultThinking(model string) Thinking {
	if strings.HasPrefix(model, HaikuVoiceModel) {
		return Thinking{Mode: ThinkingAdaptive, Effort: "low"}
	}
	return Thinking{}
}

// ChatModel is the Claude model the Planning chat runs when a Tenant chooses
// Anthropic for it and names no model (ADR-0062 amendment 2026-11-22). It is a
// chat default only: "same as voice" keeps the voice model, and a Voice Session
// never resolves to it.
const ChatModel = "claude-sonnet-5-5"

// adaptiveThinkingFamilies are the model-id prefixes that accept
// {"type":"adaptive"} together with output_config.effort. A prefix covers a
// dated snapshot and later point releases of the same line. Everything else —
// claude-haiku-4-5 (400 on both fields), claude-opus-4-5 and older, a gateway
// id — is left alone.
var adaptiveThinkingFamilies = []string{
	"claude-haiku-5",
	"claude-sonnet-5",
	"claude-opus-5",
	"claude-opus-4-6", "claude-opus-4-7", "claude-opus-4-8",
	"claude-sonnet-4-6",
	"claude-fable-5",
	"claude-mythos-5",
}

// ChatThinking is the thinking policy of the Planning chat (ADR-0062 amendment
// 2026-11-22), passed to [WithThinkingFor] by llmbuild.NewChat. Prep chat
// trades latency for quality, the opposite of a spoken turn:
//
//   - the adaptive-capable families → adaptive thinking at effort medium. It
//     is sent explicitly because the API's own default differs per model
//     (high on Sonnet 5.5, medium on Opus 5.5 and Haiku 5.5, no thinking at
//     all on Opus 4.8), and one chat should not reason at three depths
//     depending on the model picked.
//   - claude-haiku-4-5 and every id this adapter does not know → nothing, the
//     same request [DefaultThinking] sends for them.
//
// Thinking tokens count against max_tokens, so a caller using this policy
// must leave room for them (planchat asks for 16000).
func ChatThinking(model string) Thinking {
	for _, family := range adaptiveThinkingFamilies {
		if strings.HasPrefix(model, family) {
			return Thinking{Mode: ThinkingAdaptive, Effort: "medium"}
		}
	}
	return Thinking{}
}

// forcedToolChoiceRejected are the model-id prefixes that answer 400 to a
// forced tool_choice ({"type":"any"} or {"type":"tool"}): `tool_choice: type
// "tool" and "any" are not supported for this model.`
var forcedToolChoiceRejected = []string{
	"claude-opus-5-5",
	"claude-sonnet-5-5",
	"claude-fable-5-1", "claude-fable-5-2",
	"claude-mythos-5-1", "claude-mythos-5-2",
}

// rejectsForcedToolChoice reports whether model answers 400 to a forced
// tool_choice. [Client.Complete] sends auto to such a model instead.
func rejectsForcedToolChoice(model string) bool {
	for _, prefix := range forcedToolChoiceRejected {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// WithThinkingFor replaces the per-model thinking policy ([DefaultThinking]).
// policy is called with the model a request resolves to and returns what that
// request sends; a zero [Thinking] sends neither field. It mirrors
// openaicompat.WithReasoningEffortFor: the knob is per model, because the
// models of one provider accept different shapes. A nil policy is ignored.
func WithThinkingFor(policy func(model string) Thinking) Option {
	return func(c *Client) {
		if policy != nil {
			c.thinking = policy
		}
	}
}

// wireThinking is the Messages API `thinking` object.
type wireThinking struct {
	Type string `json:"type"`
}

// wireOutputConfig is the Messages API `output_config` object; only the effort
// level is ever set here.
type wireOutputConfig struct {
	Effort string `json:"effort"`
}

// wireFields renders t as the two optional request fields, nil for each one
// that is not to be sent.
func (t Thinking) wireFields() (*wireThinking, *wireOutputConfig) {
	var th *wireThinking
	if t.Mode != ThinkingUnset {
		th = &wireThinking{Type: string(t.Mode)}
	}
	var oc *wireOutputConfig
	if t.Effort != "" {
		oc = &wireOutputConfig{Effort: t.Effort}
	}
	return th, oc
}
