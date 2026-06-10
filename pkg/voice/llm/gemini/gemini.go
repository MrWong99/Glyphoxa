// Package gemini implements the v2 LLM provider surface ([llm.Provider])
// against Google's Gemini API, streaming completions over SSE and translating
// Gemini's tool-call deltas into the [llm] message vocabulary.
//
// It targets Gemini's OpenAI-compatibility endpoint
// (generativelanguage.googleapis.com/v1beta/openai/chat/completions) rather
// than the native generativelanguage surface. Two reasons: (1) the deployment
// already drives Gemini through this compat endpoint for embeddings with the
// same key (see the glyphoxa-config providers block), so the live NPC reuses
// one auth path; (2) the compat endpoint correlates a tool result to its call
// by an opaque id — which maps 1:1 onto [llm.ToolResult.CallID] — whereas the
// native API matches function responses by name, which would force the adapter
// to recover each call's name from the prior assistant turn. The compat shape
// is the clean fit for the existing tool-use seam (ADR-0028). The adapter still
// presents as the "gemini" provider with a Gemini model id; the endpoint choice
// is an internal detail.
//
// Authentication is BYOK per ADR-0004: callers either pass the API key to
// [New] or set GEMINI_API_KEY. [New] never fails so that cassette-replay test
// binaries can link this package without an API key configured — missing-key
// errors surface at request time instead, matching the anthropic and stt/tts
// elevenlabs adapters' posture.
package gemini

import (
	"net"
	"net/http"
	"os"
	"time"
)

const (
	// DefaultBaseURL is the Gemini OpenAI-compatibility API root. The
	// /chat/completions path is appended at request time. This matches the
	// deployment's providers.embeddings.base_url for the same key.
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

	// DefaultReasoningEffort caps gemini-2.5-flash's dynamic "thinking" by
	// default (Sprint 2 / B2 — latency.md H1). On the OpenAI-compat endpoint the
	// max_tokens ceiling bounds thinking *tokens* but NOT wall-time: by default
	// thinking is dynamic, so a reasoning-bait input can spend several seconds
	// before the first content token streams — the best match for the
	// "manchmal sehr spät" tail. reasoning_effort is the documented compat knob;
	// for gemini-2.5 it maps to an internal thinking_budget. "low" keeps a small
	// reasoning allowance (a short NPC turn rarely needs more) while bounding the
	// tail; override with [WithReasoningEffort] / [WithThinkingBudget]. Empirically
	// pinned against the default via a live A/B — see docs/adr/0035.
	//
	// Accepted values (compat endpoint): "none", "minimal", "low", "medium",
	// "high". "none" disables thinking on 2.5-flash; "" lets the model choose
	// (the old time-unbounded default).
	DefaultReasoningEffort = "low"
)

// Client is the Gemini LLM adapter. Construct with [New]; the zero value is not
// usable. Safe for concurrent use across goroutines (the underlying http.Client
// is).
type Client struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client

	// reasoningEffort, when non-empty, is sent as the OpenAI-compat
	// reasoning_effort field to bound thinking wall-time (B2). Mutually exclusive
	// with thinkingBudget: if thinkingBudget is set (non-nil) it wins and
	// reasoning_effort is omitted, since the endpoint rejects both at once.
	reasoningEffort string
	// thinkingBudget, when non-nil, is sent as the explicit 2.5 thinking-token
	// cap under extra_body.google.thinking_config.thinking_budget (0 = thinking
	// off, -1 = dynamic/unbounded, N = at most N reasoning tokens). The precise
	// escape hatch when "low" is too coarse; takes precedence over reasoningEffort.
	thinkingBudget *int
}

// Option mutates a [Client] during construction.
type Option func(*Client)

// WithBaseURL overrides the API base URL. Useful for tests (httptest server)
// and self-hosted gateways.
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = u } }

// WithHTTPClient supplies a custom http.Client. The default has no overall
// timeout because streaming completions are long-lived — but it does bound
// dialing, TLS, and time-to-first-response-header (see [New]); the per-call
// deadline for the whole exchange must come from the request context.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// defaultHTTPClient bounds the connection-establishment phases so a black-holed
// endpoint fails in seconds instead of hanging a turn. No overall Timeout: that
// would also cap healthy long-lived SSE streams — the end-to-end bound is the
// caller's ctx deadline (the agent loop's TurnTimeout).
func defaultHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

// WithModel sets the default model used when [llm.Request.Model] is empty.
func WithModel(m string) Option { return func(c *Client) { c.model = m } }

// WithReasoningEffort overrides the reasoning_effort sent to the compat endpoint
// (B2 — bound thinking wall-time). Accepted: "none", "minimal", "low", "medium",
// "high"; "" disables the cap (model chooses, the old time-unbounded behaviour).
// Setting a non-empty effort clears any [WithThinkingBudget] (the two are
// mutually exclusive on the wire); pass "" to fall back to a budget instead.
func WithReasoningEffort(effort string) Option {
	return func(c *Client) {
		c.reasoningEffort = effort
		c.thinkingBudget = nil
	}
}

// WithThinkingBudget pins the explicit 2.5 thinking-token cap
// (extra_body.google.thinking_config.thinking_budget): 0 turns thinking off,
// -1 restores dynamic/unbounded thinking, N caps reasoning at N tokens. The
// precise alternative to [WithReasoningEffort]'s coarse buckets; it takes
// precedence and suppresses reasoning_effort, since the endpoint rejects both
// at once.
func WithThinkingBudget(tokens int) Option {
	return func(c *Client) {
		t := tokens
		c.thinkingBudget = &t
	}
}

// New constructs a [Client]. If apiKey is empty it falls back to the
// GEMINI_API_KEY environment variable; if that is also empty, the returned
// client still links — calls return a "missing API key" error rather than
// panicking on construction, so cassette-replay test binaries can import this
// package unconditionally.
func New(apiKey string, opts ...Option) *Client {
	if apiKey == "" {
		apiKey = os.Getenv(APIKeyEnv)
	}
	c := &Client{
		apiKey:          apiKey,
		baseURL:         DefaultBaseURL,
		model:           DefaultModel,
		http:            defaultHTTPClient(),
		reasoningEffort: DefaultReasoningEffort,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}
