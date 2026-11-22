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
