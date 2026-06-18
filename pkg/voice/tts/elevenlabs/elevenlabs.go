// Package elevenlabs implements the v2 TTS provider surface against the
// ElevenLabs HTTP API, targeting the eleven_v3 model and its inline
// bracketed audio-tag vocabulary.
//
// Per ADR-0023 the [Client] satisfies the full v1.0 capability matrix:
// [tts.Synthesizer] (plus the required AudioMarkupPrompt), [tts.VoiceLister],
// [tts.VoiceCloner], [tts.VoiceDesigner], and [tts.DialogueSynthesizer].
//
// Authentication is BYOK per ADR-0004: callers either pass the API key to
// [New] or set ELEVENLABS_API_KEY. [New] never fails so that cassette-replay
// test binaries can link this package without an API key configured —
// missing-key errors surface at request time instead.
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
	// argument is empty.
	APIKeyEnv = "ELEVENLABS_API_KEY"

	// ProviderID is the stable string written into [tts.Voice.ProviderID]
	// for every voice this adapter returns.
	ProviderID = "elevenlabs"
)

// Client is the ElevenLabs TTS adapter. Construct with [New]; the zero value
// is not usable. Safe for concurrent use across goroutines (the underlying
// http.Client is).
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
// bounds the connection-establishment and time-to-first-audio-byte phases but
// sets no overall Timeout (a synthesis streams for the length of the reply); the
// per-call end-to-end bound is the request context's deadline (the per-turn floor
// context, cancelled on barge-in).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// defaultHTTPClient bounds the connection-establishment and response-header
// phases so a black-holed synthesis endpoint fails fast instead of holding the
// reply goroutine open. It mirrors the STT adapter and the LLM clients, but with
// voice-tight values: a synthesis must start emitting audio in well under a
// second (first-audio TTFB is ~0.25–0.45s in practice), so ResponseHeaderTimeout
// is 5s — ~10× the observed max, enough headroom to never trip on a real reply
// yet failing a hung connection in 5s rather than tens of seconds. It bounds
// time-to-first-RESPONSE-byte only, NOT the streamed audio that follows, so a
// long multi-sentence reply is never clipped. No overall Timeout, for the same
// reason. The barge/per-turn ctx is still the end-to-end cancel.
func defaultHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 5 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

// New constructs a [Client]. If apiKey is empty it falls back to the
// ELEVENLABS_API_KEY environment variable; if that is also empty, the
// returned client still links — calls return a "missing API key" error
// rather than panicking on construction, so cassette-replay test binaries
// can import this package unconditionally.
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
