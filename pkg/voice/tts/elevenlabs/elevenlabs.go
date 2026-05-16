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
	"net/http"
	"os"
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

// WithHTTPClient supplies a custom http.Client. The default has no overall
// timeout because streaming syntheses are long-lived; per-call deadlines must
// come from the request context.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

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
		http:    &http.Client{},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}
