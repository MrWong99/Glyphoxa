// Package elevenlabs implements the v2 STT provider surface against the
// ElevenLabs HTTP API, targeting the scribe_v2 model.
//
// The [Client] satisfies [stt.Recognizer]: utterance-scoped batch
// transcription that maps one-to-one to a POST /v1/speech-to-text call. The
// orchestrator's STT stage forwards [audio.Frame]s and republishes the
// returned [stt.Transcript] as [voiceevent.STTFinal] per ADR-0020.
//
// Authentication is BYOK per ADR-0004: callers either pass the API key to
// [New] or set ELEVENLABS_API_KEY. [New] never fails so that cassette-replay
// test binaries can link this package without an API key configured —
// missing-key errors surface at request time instead, matching the TTS
// adapter's posture.
package elevenlabs

import (
	"net"
	"net/http"
	"os"
	"time"
)

const (
	// DefaultBaseURL is the ElevenLabs production API root.
	DefaultBaseURL = "https://api.elevenlabs.io"

	// APIKeyEnv is the environment variable [New] consults when its apiKey
	// argument is empty. Shared with the TTS adapter — one BYOK key per
	// ElevenLabs Tenant covers every Component the provider offers.
	APIKeyEnv = "ELEVENLABS_API_KEY"

	// ProviderID is the stable identifier for this STT adapter. Matches the
	// TTS adapter's ProviderID because a single ElevenLabs Provider Config
	// covers every Component (ADR-0004).
	ProviderID = "elevenlabs"
)

// Client is the ElevenLabs STT adapter. Construct with [New]; the zero value
// is not usable. Safe for concurrent use across goroutines.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// Option mutates a [Client] during construction.
type Option func(*Client)

// WithBaseURL overrides the API base URL. Useful for tests (httptest server)
// and self-hosted ElevenLabs deployments.
func WithBaseURL(u string) Option { return func(c *Client) { c.baseURL = u } }

// WithHTTPClient supplies a custom http.Client. The default ([defaultHTTPClient])
// bounds the connection-establishment and response-header phases but sets no
// overall Timeout; the per-call end-to-end bound is the request context's
// deadline (the orchestrator STT stage's per-request timeout).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// defaultHTTPClient bounds the connection-establishment and response-header
// phases so a black-holed scribe endpoint fails in seconds instead of hanging
// the single serial transcription worker (and, behind it, the whole voice loop)
// until session shutdown. No overall Timeout: a Scribe POST uploads the utterance
// audio first, and an http.Client.Timeout would cap that upload as well as the
// transcription — the end-to-end bound is the caller's ctx deadline (the
// orchestrator STT stage's per-request timeout, default 15s).
//
// Scribe's response is a single JSON blob (non-streaming), so ResponseHeaderTimeout
// bounds the WHOLE transcription once the body is sent — the exact "connected but
// never answers" hang the live wedge hit. We expect a response well under 2s
// (stt_request p95 ~1.4s), so 10s gives generous headroom for a long utterance on
// a slow day while still failing a hang in 10s. Dial/TLS are voice-tight (5s) —
// a healthy connect is sub-second; these only delay failure on a cold connect to
// a dead endpoint.
func defaultHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

// New constructs a [Client]. If apiKey is empty it falls back to the
// ELEVENLABS_API_KEY environment variable; if that is also empty, the
// returned client still links — calls return a "missing API key" error
// rather than panicking on construction.
func New(apiKey string, opts ...Option) *Client {
	if apiKey == "" {
		apiKey = os.Getenv(APIKeyEnv)
	}
	c := &Client{
		apiKey:  apiKey,
		baseURL: DefaultBaseURL,
		http:    defaultHTTPClient(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}
