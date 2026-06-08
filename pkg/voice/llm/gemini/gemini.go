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
	"net/http"
	"os"
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
)

// Client is the Gemini LLM adapter. Construct with [New]; the zero value is not
// usable. Safe for concurrent use across goroutines (the underlying http.Client
// is).
type Client struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client
}

// Option mutates a [Client] during construction.
type Option func(*Client)

// WithBaseURL overrides the API base URL. Useful for tests (httptest server)
// and self-hosted gateways.
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = u } }

// WithHTTPClient supplies a custom http.Client. The default has no overall
// timeout because streaming completions are long-lived; per-call deadlines must
// come from the request context.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithModel sets the default model used when [llm.Request.Model] is empty.
func WithModel(m string) Option { return func(c *Client) { c.model = m } }

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
		apiKey:  apiKey,
		baseURL: DefaultBaseURL,
		model:   DefaultModel,
		http:    &http.Client{},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}
