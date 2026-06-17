// Package gemini adapts the v2 LLM provider surface ([llm.Provider]) to Google's
// Gemini by preconfiguring the shared OpenAI-compatible adapter
// ([github.com/MrWong99/Glyphoxa/pkg/voice/llm/openaicompat]) for Gemini's
// OpenAI-compatibility endpoint
// (generativelanguage.googleapis.com/v1beta/openai/chat/completions) rather than
// the native generativelanguage surface.
//
// Two reasons for the compat endpoint (ADR-0036): (1) the deployment already
// drives Gemini through it for embeddings with the same key, so the live NPC
// reuses one auth path; (2) the compat endpoint correlates a tool result to its
// call by an opaque id — which maps 1:1 onto [llm.ToolResult.CallID] — whereas
// the native API matches function responses by name, which would force the
// adapter to recover each call's name from the prior assistant turn. The compat
// shape is the clean fit for the existing tool-use seam (ADR-0028); the endpoint
// choice is an internal detail. The adapter is therefore a thin preset over the
// SDK-backed core (ADR-0037) plus the Gemini-only thinking-cap knobs.
//
// Authentication is BYOK per ADR-0004: callers either pass the API key to [New]
// or set GEMINI_API_KEY. [New] never fails so that cassette-replay test binaries
// can link this package without an API key configured — missing-key errors
// surface at request time instead, matching the anthropic and stt/tts elevenlabs
// adapters' posture.
package gemini

import (
	"net/http"
	"os"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/openaicompat"
)

const (
	// DefaultBaseURL is the Gemini OpenAI-compatibility API root. The SDK appends
	// /chat/completions at request time. This matches the deployment's
	// providers.embeddings.base_url for the same key.
	DefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta/openai"

	// APIKeyEnv is the environment variable [New] consults when its apiKey
	// argument is empty. The keyring entry is service=glyphoxa key=gemini; the
	// manual-run path exports it into this var (see docs/agents/live-npc-run.md).
	APIKeyEnv = "GEMINI_API_KEY"

	// ProviderID is the stable string identifying this LLM adapter; it matches
	// the Provider Config's provider name (providers.llm.name: "gemini").
	ProviderID = "gemini"

	// DefaultModel is used when [llm.Request.Model] is empty. It mirrors the
	// deployment's providers.llm.model.
	DefaultModel = "gemini-2.5-flash"

	// DefaultMaxTokens caps a completion when [llm.Request.MaxTokens] is zero.
	// gemini-2.5-flash is a thinking model whose reasoning tokens count against
	// this ceiling, so the headroom is more generous than the anthropic adapter's
	// to keep a short spoken NPC turn from being truncated by thinking.
	DefaultMaxTokens = 2048

	// DefaultReasoningEffort caps gemini-2.5-flash's dynamic "thinking" by default
	// (ADR-0035). On the OpenAI-compat endpoint the max_tokens ceiling bounds
	// thinking *tokens* but NOT wall-time: by default thinking is dynamic, so a
	// reasoning-bait input can spend several seconds before the first content
	// token streams. reasoning_effort is the documented compat knob; for
	// gemini-2.5 it maps to an internal thinking_budget. "low" keeps a small
	// reasoning allowance while bounding the tail; override with
	// [WithReasoningEffort] / [WithThinkingBudget].
	//
	// Accepted values (compat endpoint): "none", "minimal", "low", "medium",
	// "high". "none" disables thinking on 2.5-flash; "" lets the model choose (the
	// old time-unbounded default).
	DefaultReasoningEffort = "low"
)

// Client is the Gemini LLM adapter: the shared [openaicompat.Client]
// preconfigured by [New] against Gemini's OpenAI-compat endpoint. Construct with
// [New]; the zero value is not usable. Safe for concurrent use across goroutines.
type Client = openaicompat.Client

// Option customises the Gemini [Client]. Gemini carries its own Option type
// (rather than re-exporting [openaicompat.Option]) because the thinking-cap knobs
// are resolved into the shared adapter's config with mutual-exclusivity rules.
type Option func(*settings)

// settings accumulates [Option] state before [New] resolves it into
// [openaicompat.Option]s.
type settings struct {
	baseURL    string
	model      string
	httpClient *http.Client

	// reasoningEffort, when non-empty, is sent as the OpenAI-compat
	// reasoning_effort field to bound thinking wall-time. Mutually exclusive with
	// thinkingBudget: if thinkingBudget is set (non-nil) it wins and
	// reasoning_effort is omitted, since the endpoint rejects both at once.
	reasoningEffort string
	// thinkingBudget, when non-nil, is sent as the explicit 2.5 thinking-token cap
	// under extra_body.google.thinking_config.thinking_budget (0 = thinking off,
	// -1 = dynamic/unbounded, N = at most N reasoning tokens). Takes precedence
	// over reasoningEffort.
	thinkingBudget *int
}

// WithBaseURL overrides the API base URL. Useful for tests (httptest server) and
// self-hosted gateways.
func WithBaseURL(u string) Option { return func(s *settings) { s.baseURL = u } }

// WithModel sets the default model used when [llm.Request.Model] is empty.
func WithModel(m string) Option { return func(s *settings) { s.model = m } }

// WithHTTPClient supplies a custom http.Client (see [openaicompat.WithHTTPClient]).
func WithHTTPClient(h *http.Client) Option { return func(s *settings) { s.httpClient = h } }

// WithReasoningEffort overrides the reasoning_effort sent to the compat endpoint
// (bound thinking wall-time). Accepted: "none", "minimal", "low", "medium",
// "high"; "" disables the cap (model chooses, the old time-unbounded behaviour).
// Setting a non-empty effort clears any [WithThinkingBudget] (the two are
// mutually exclusive on the wire); pass "" to fall back to a budget instead.
func WithReasoningEffort(effort string) Option {
	return func(s *settings) {
		s.reasoningEffort = effort
		s.thinkingBudget = nil
	}
}

// WithThinkingBudget pins the explicit 2.5 thinking-token cap
// (extra_body.google.thinking_config.thinking_budget): 0 turns thinking off, -1
// restores dynamic/unbounded thinking, N caps reasoning at N tokens. The precise
// alternative to [WithReasoningEffort]'s coarse buckets; it takes precedence and
// suppresses reasoning_effort, since the endpoint rejects both at once.
func WithThinkingBudget(tokens int) Option {
	return func(s *settings) {
		t := tokens
		s.thinkingBudget = &t
	}
}

// New constructs a Gemini [Client]. If apiKey is empty it falls back to the
// GEMINI_API_KEY environment variable; if that is also empty, the returned client
// still links — calls return a "missing API key" error rather than panicking on
// construction, so cassette-replay test binaries can import this package
// unconditionally.
func New(apiKey string, opts ...Option) *Client {
	if apiKey == "" {
		apiKey = os.Getenv(APIKeyEnv)
	}
	s := &settings{
		baseURL:         DefaultBaseURL,
		model:           DefaultModel,
		reasoningEffort: DefaultReasoningEffort,
	}
	for _, opt := range opts {
		opt(s)
	}

	base := []openaicompat.Option{
		openaicompat.WithProviderName(ProviderID),
		openaicompat.WithAPIKey(apiKey),
		openaicompat.WithAPIKeyEnv(APIKeyEnv),
		openaicompat.WithBaseURL(s.baseURL),
		openaicompat.WithModel(s.model),
		openaicompat.WithDefaultMaxTokens(DefaultMaxTokens),
	}
	if s.httpClient != nil {
		base = append(base, openaicompat.WithHTTPClient(s.httpClient))
	}

	// Apply the thinking cap (ADR-0035). thinking_budget and reasoning_effort
	// overlap and the endpoint rejects both, so an explicit budget wins and
	// suppresses reasoning_effort; otherwise the configured effort (default "low")
	// rides as the top-level field, and an empty effort sends neither.
	switch {
	case s.thinkingBudget != nil:
		base = append(base, openaicompat.WithExtraFields(map[string]any{
			"extra_body": map[string]any{
				"google": map[string]any{
					"thinking_config": map[string]any{
						"thinking_budget": *s.thinkingBudget,
					},
				},
			},
		}))
	case s.reasoningEffort != "":
		base = append(base, openaicompat.WithReasoningEffort(s.reasoningEffort))
	}

	return openaicompat.New(base...)
}
