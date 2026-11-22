// Package elevenlabs implements the v2 TTS provider surface against the
// ElevenLabs HTTP API. Voices synthesize on [DefaultModel] (eleven_v4_turbo,
// #702) unless their Settings name another model; the v3/v4 families share an
// inline bracketed audio-tag vocabulary the agent prompt teaches
// ([Client.AudioMarkupPrompt]).
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
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
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
// second (first audio measured 2026-11-22: ~0.14s warm on eleven_v4_turbo,
// ~0.45–0.8s on eleven_v3), so ResponseHeaderTimeout is 5s — well over 5× the
// observed max, enough headroom to never trip on a real reply
// yet failing a hung connection in 5s rather than tens of seconds. It bounds
// time-to-first-RESPONSE-byte only, NOT the streamed audio that follows, so a
// long multi-sentence reply is never clipped. No overall Timeout, for the same
// reason. The barge/per-turn ctx is still the end-to-end cancel.
func defaultHTTPClient() *http.Client {
	return &http.Client{Transport: defaultTransport()}
}

// Connection-pool tuning for the synthesis hot path (#702). A reply's first
// sentence that has to dial pays TCP+TLS (~60–130 ms measured against the real
// API, on a ~135 ms first-audio budget for eleven_v4_turbo), so the pool keeps
// connections warm:
//   - idleConnsPerHost: the pipelined look-ahead (#626) runs two Synthesize calls
//     at once, plus a [Client.Warm] or a voice preview can overlap; the stdlib
//     default of 2 would close the surplus connection after every pair.
//   - idleConnTimeout: long enough to span the quiet gaps of a table conversation;
//     the vendor's own idle close (observed between ~20 and ~60 s) is what really
//     bounds reuse, which is what [Client.Warm] is for.
const (
	idleConnsPerHost = 4
	idleConnTimeout  = 90 * time.Second
)

// defaultTransport is the transport behind [defaultHTTPClient]; see there and
// the pool constants above.
func defaultTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConns:          idleConnsPerHost * 4,
		MaxIdleConnsPerHost:   idleConnsPerHost,
		IdleConnTimeout:       idleConnTimeout,
	}
}

// warmTimeout bounds one [Client.Warm] so a black-holed endpoint never holds the
// caller's goroutine; it matches the dial and TLS bounds above.
const warmTimeout = 5 * time.Second

// Warm opens (or refreshes) a pooled connection to the API so the next synthesis
// starts on an established TCP+TLS connection instead of dialling. It sends one
// HEAD request to the API root — no audio, no characters billed — and drains the
// answer, whatever its status, back into the pool. The live wiring calls it when a
// Voice Session connects, when a participant starts speaking (the reply follows
// within a second or two) and after a barge (which drops the connection a
// mid-stream sentence was using).
//
// Only a transport failure is an error; callers treat it as advisory (the
// synthesis would simply dial). A keyless client is a no-op: it cannot
// synthesize, and cassette tiers must never touch the network.
func (c *Client) Warm(ctx context.Context) error {
	if c.apiKey == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, warmTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, strings.TrimRight(c.baseURL, "/")+"/", nil)
	if err != nil {
		return fmt.Errorf("elevenlabs.Warm: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("elevenlabs.Warm: %w", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
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
