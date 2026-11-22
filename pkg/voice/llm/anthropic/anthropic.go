// Package anthropic implements the v2 LLM provider surface ([llm.Provider])
// against the Anthropic Messages API, streaming completions over SSE and
// translating Claude's tool-use blocks into the [llm] message vocabulary.
//
// Authentication is BYOK per ADR-0004: callers either pass the API key to
// [New] or set ANTHROPIC_API_KEY. [New] never fails so that cassette-replay
// test binaries can link this package without an API key configured —
// missing-key errors surface at request time instead, matching the
// stt/tts elevenlabs adapters' posture.
package anthropic

import (
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"
)

const (
	// DefaultBaseURL is the Anthropic production API root.
	DefaultBaseURL = "https://api.anthropic.com"

	// APIKeyEnv is the environment variable [New] consults when its apiKey
	// argument is empty.
	APIKeyEnv = "ANTHROPIC_API_KEY"

	// ProviderID is the stable string identifying this LLM adapter; it matches
	// the Provider Config's provider name (ADR-0004).
	ProviderID = "anthropic"

	// DefaultModel is used when [llm.Request.Model] is empty. Per the project's
	// model policy this is the current flagship Claude (CLAUDE.md).
	DefaultModel = "claude-opus-4-8"

	// DefaultMaxTokens caps a completion when [llm.Request.MaxTokens] is zero.
	// Spoken NPC turns are short; this is generous headroom, not a target.
	DefaultMaxTokens = 1024

	// apiVersion is the Anthropic API version header value the Messages
	// endpoint requires.
	apiVersion = "2023-06-01"
)

// Client is the Anthropic LLM adapter. Construct with [New]; the zero value is
// not usable. Safe for concurrent use across goroutines (the underlying
// http.Client is).
type Client struct {
	apiKey  string
	baseURL string
	model   string
	http    *http.Client

	// thinking is the per-model reasoning policy ([DefaultThinking] unless
	// [WithThinkingFor] replaced it).
	thinking func(model string) Thinking

	// caching is the per-model prompt-caching gate ([DefaultPromptCaching]
	// unless [WithPromptCachingFor] replaced it).
	caching func(model string) bool

	// log receives the adapter's few operational warnings ([WithLogger]).
	log *slog.Logger

	// thoughts keeps the thinking blocks of recent assistant tool turns so the
	// next tool round can replay them (passthrough.go).
	thoughts *thinkingStore
}

// Option mutates a [Client] during construction.
type Option func(*Client)

// WithBaseURL overrides the API base URL. Useful for tests (httptest server)
// and self-hosted gateways.
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = u } }

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

// WithLogger sets where the adapter logs its operational warnings — today one:
// a replayed thinking block the API refused (see [Client.Complete]). The
// default is [slog.Default]; a nil logger is ignored.
func WithLogger(l *slog.Logger) Option {
	return func(c *Client) {
		if l != nil {
			c.log = l
		}
	}
}

// WithModel sets the default model used when [llm.Request.Model] is empty.
func WithModel(m string) Option { return func(c *Client) { c.model = m } }

// New constructs a [Client]. If apiKey is empty it falls back to the
// ANTHROPIC_API_KEY environment variable; if that is also empty, the returned
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
		http:    defaultHTTPClient(),

		thinking: DefaultThinking,
		caching:  DefaultPromptCaching,
		thoughts: newThinkingStore(),
		log:      slog.Default(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}
