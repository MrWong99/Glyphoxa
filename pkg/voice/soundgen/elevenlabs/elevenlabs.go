// Package elevenlabs implements the [soundgen.Generator] surface against the
// ElevenLabs sound-generation and Music HTTP APIs (#312, Epic 8) — the
// endpoint-wrapper sibling of the tts and stt ElevenLabs adapters.
//
// Authentication is BYOK per ADR-0004: sound generation rides the Tenant's
// existing `tts` Provider Config key (one ElevenLabs key covers every Component
// the provider offers), so callers either pass that key to [New] or set
// ELEVENLABS_API_KEY. [New] never fails so that cassette-replay test binaries
// can link this package without an API key configured — missing-key errors
// surface at request time instead.
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
	// argument is empty. Deliberately the same variable as the tts and stt
	// adapters: one BYOK key per ElevenLabs Tenant covers every Component.
	APIKeyEnv = "ELEVENLABS_API_KEY"

	// ProviderID is the stable ElevenLabs provider identifier, matching the
	// tts adapter's — the ADR-0004 amendment's "iff the configured tts
	// provider is ElevenLabs" check compares against this.
	ProviderID = "elevenlabs"

	// SFXModel is the sound-effects model the sting endpoint targets — also
	// the model id sting usage meters under (ADR-0046 price map, ADR-0004
	// amendment: SFX and Music meter separately).
	SFXModel = "eleven_text_to_sound_v2"

	// MusicModel is the Music composition model — the Music metering model id.
	MusicModel = "music_v1"
)

// Client is the ElevenLabs sound-generation adapter. Construct with [New]; the
// zero value is not usable. Safe for concurrent use across goroutines (the
// underlying http.Client is).
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

// WithHTTPClient supplies a custom http.Client. The default
// ([defaultHTTPClient]) bounds connection establishment but allows a long
// response-header phase; the per-call end-to-end bound is the request
// context's deadline (the background job's lease, ADR-0049).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// defaultHTTPClient bounds the connection-establishment phase like the tts and
// stt adapters, but with a generation-scale ResponseHeaderTimeout: unlike a
// synthesis (first audio byte in well under a second), the sound and Music
// endpoints return no headers until the WHOLE asset is generated — a Music
// track can take minutes — so 5m gives real generations headroom while a
// black-holed endpoint still fails without waiting on TCP defaults. The
// enrichment job's lease context remains the end-to-end cancel.
func defaultHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 5 * time.Minute,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

// New constructs a [Client]. If apiKey is empty it falls back to the
// ELEVENLABS_API_KEY environment variable; if that is also empty, the returned
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
		http:    defaultHTTPClient(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}
