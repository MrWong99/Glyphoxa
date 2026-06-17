// Package groq implements the v2 LLM provider surface ([llm.Provider]) against
// Groq's API, streaming completions over SSE and translating Groq's tool-call
// deltas into the [llm] message vocabulary.
//
// It targets Groq's OpenAI-compatibility endpoint
// (api.groq.com/openai/v1/chat/completions) — the same wire shape the gemini
// adapter uses. Groq serves open-weight models (Llama, etc.) behind that compat
// surface, so the adapter is a thin re-skin of the OpenAI-compat path: the
// system prompt rides as a "system"-role message, tool results correlate to
// their call by an opaque id that maps 1:1 onto [llm.ToolResult.CallID], and the
// tool-use loop (ADR-0028) slots in unchanged.
//
// Authentication is BYOK per ADR-0004: callers either pass the API key to [New]
// or set GROQ_API_KEY. [New] never fails so that cassette-replay test binaries
// can link this package without an API key configured — missing-key errors
// surface at request time instead, matching the anthropic, gemini, and stt/tts
// elevenlabs adapters' posture.
package groq

import (
	"net"
	"net/http"
	"os"
	"time"
)

const (
	// DefaultBaseURL is the Groq OpenAI-compatibility API root. The
	// /chat/completions path is appended at request time.
	DefaultBaseURL = "https://api.groq.com/openai/v1"

	// APIKeyEnv is the environment variable [New] consults when its apiKey
	// argument is empty.
	APIKeyEnv = "GROQ_API_KEY"

	// ProviderID is the stable string identifying this LLM adapter; it matches
	// the Provider Config's provider name (providers.llm.name: "groq").
	ProviderID = "groq"

	// DefaultModel is used when [llm.Request.Model] is empty. Groq's Llama 3.3
	// 70B production id; override per-client with [WithModel] or per-call with
	// [llm.Request.Model].
	DefaultModel = "llama-3.3-70b-versatile"

	// DefaultMaxTokens caps a completion when [llm.Request.MaxTokens] is zero.
	// Llama 3.3 70B is not a thinking model — every token counts toward the
	// spoken reply — so the ceiling matches the anthropic adapter's tighter
	// bound rather than gemini's thinking-token headroom.
	DefaultMaxTokens = 1024
)

// Client is the Groq LLM adapter. Construct with [New]; the zero value is not
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
// timeout because streaming completions are long-lived — but it does bound
// dialing, TLS, and time-to-first-response-header (see [New]); the per-call
// deadline for the whole exchange must come from the request context.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithModel sets the default model used when [llm.Request.Model] is empty.
func WithModel(m string) Option { return func(c *Client) { c.model = m } }

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

// New constructs a [Client]. If apiKey is empty it falls back to the
// GROQ_API_KEY environment variable; if that is also empty, the returned client
// still links — calls return a "missing API key" error rather than panicking on
// construction, so cassette-replay test binaries can import this package
// unconditionally.
func New(apiKey string, opts ...Option) *Client {
	if apiKey == "" {
		apiKey = os.Getenv(APIKeyEnv)
	}
	c := &Client{
		apiKey:  apiKey,
		baseURL: DefaultBaseURL,
		model:   DefaultModel,
		http:    defaultHTTPClient(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}
