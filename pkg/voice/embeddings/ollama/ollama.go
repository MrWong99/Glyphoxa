// Package ollama implements the v2 Embeddings provider surface against a local
// Ollama server, targeting the nomic-embed-text model (ADR-0011).
//
// The [Client] satisfies [github.com/MrWong99/Glyphoxa/pkg/voice/embeddings.Provider]:
// batch text embedding that maps to a single POST /api/embed call. The async
// embedding worker (ADR-0011) forwards Transcript chunk text and stores the
// returned vector(768).
//
// Unlike the LLM/STT adapters, Ollama embeddings are KEYLESS in v1.0 — a local
// server, no BYOK credential (ADR-0004's OpenAI sibling adds a key later behind
// the same interface). Endpoint resolution is the consumer wiring's job (#116
// reads GLYPHOXA_OLLAMA_URL); this adapter takes an explicit base URL via
// [WithBaseURL], defaulting to [DefaultBaseURL]. The HTTP surface is hand-rolled
// (mirroring the STT ElevenLabs adapter) rather than an SDK or the OpenAI-compat
// layer, because Ollama's /api/embed wire is not the OpenAI embeddings wire.
package ollama

import (
	"net"
	"net/http"
	"time"
)

const (
	// ProviderID is the stable identifier for this Embeddings adapter, matching
	// the Ollama Provider Config's provider slot (ADR-0004).
	ProviderID = "ollama"

	// DefaultModel is the v1.0 embedding model (ADR-0011): 768-dim, local.
	DefaultModel = "nomic-embed-text"

	// DefaultBaseURL is the loopback Ollama server root. The consumer wiring
	// (#116) overrides it from GLYPHOXA_OLLAMA_URL via [WithBaseURL].
	DefaultBaseURL = "http://127.0.0.1:11434"
)

// Client is the Ollama Embeddings adapter. Construct with [New]; the zero value
// is not usable. Safe for concurrent use across goroutines.
type Client struct {
	baseURL string
	model   string
	http    *http.Client
}

// Option mutates a [Client] during construction.
type Option func(*Client)

// WithBaseURL overrides the Ollama server base URL. Used by tests (httptest
// server) and by the consumer wiring that reads GLYPHOXA_OLLAMA_URL (#116).
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = u } }

// WithModel overrides the embedding model. Defaults to [DefaultModel];
// switching the model changes the vector space and requires a backfill
// (ADR-0011), so this is an operational escape hatch, not a per-call knob.
func WithModel(m string) Option { return func(c *Client) { c.model = m } }

// WithHTTPClient supplies a custom http.Client. The default
// ([defaultHTTPClient]) bounds the connection-establishment and response-header
// phases but sets no overall Timeout; the per-call end-to-end bound is the
// request context's deadline (the embedding worker's per-batch timeout).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// defaultHTTPClient bounds the connect/TLS/response-header phases so a
// black-holed Ollama endpoint fails in seconds rather than stalling the async
// embedding worker. No overall Timeout: a large batch's response body may take
// a while to stream, and the end-to-end bound is the caller's ctx deadline.
func defaultHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

// New constructs a [Client] with the loopback default base URL and
// nomic-embed-text model. Pass [WithBaseURL], [WithModel], or [WithHTTPClient]
// to override. New never fails so test binaries link the package
// unconditionally; endpoint problems surface at the first [Client.Embed] call.
func New(opts ...Option) *Client {
	c := &Client{
		baseURL: DefaultBaseURL,
		model:   DefaultModel,
		http:    defaultHTTPClient(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}
