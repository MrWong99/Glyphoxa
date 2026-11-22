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
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
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

	// terms and language are the batch path's recognition hints, fixed at
	// construction (see [WithTerms], [WithLanguage]). The realtime path takes its
	// hints per session from [stt.StreamConfig] instead.
	terms    []string
	language string

	// keytermsRefused latches once the provider has refused a keyterm list this
	// client sent, on either transport. From then on the client sends none: it
	// fails open rather than lose every utterance to a list that will be refused
	// again (see [Client.refuseKeyterms]).
	keytermsRefused atomic.Bool
	log             *slog.Logger
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

// WithTerms biases every batch [Client.Transcribe] towards terms — the names at
// the table — by sending them as Scribe keyterms (#701). The list is sanitised
// and capped by [Keyterms]; list the terms by priority. Empty sends none, which
// is also what keeps the keyterm surcharge off the request.
//
// It is a construction-time option, not a per-call argument, because the caller
// builds one Client per Voice Session and the list is fixed for that session. A
// realtime session takes the same list through [stt.StreamConfig.Terms].
func WithTerms(terms []string) Option {
	return func(c *Client) { c.terms = Keyterms(terms) }
}

// WithLanguage pins every batch [Client.Transcribe] to one language
// (language_code) instead of letting Scribe detect it. Empty keeps
// auto-detection, the default: a table may mix languages. A realtime session
// takes the same hint through [stt.StreamConfig.Language].
func WithLanguage(code string) Option {
	return func(c *Client) { c.language = strings.TrimSpace(code) }
}

// WithLogger sets where the client reports the one thing it decides on its own:
// that the provider refused its keyterm list and it carries on without one. A nil
// log keeps the default ([slog.Default]).
func WithLogger(log *slog.Logger) Option {
	return func(c *Client) {
		if log != nil {
			c.log = log
		}
	}
}

// refuseKeyterms records that the provider refused a list of n keyterms on
// transport ("batch" or "realtime") and stops this client sending keyterms on
// BOTH transports for the rest of its life — one Voice Session connection cycle.
// It warns once; the provider's detail text may quote a term (a player's name),
// so it is logged at debug only.
func (c *Client) refuseKeyterms(transport string, n int, detail string) {
	if !c.keytermsRefused.CompareAndSwap(false, true) {
		return
	}
	c.log.Warn("stt keyterms: the provider refused the keyterm list; continuing WITHOUT keyterms, so names may be misheard again",
		"transport", transport, "terms", n)
	c.log.Debug("stt keyterms: refusal detail", "transport", transport, "detail", detail)
}

// liveKeyterms narrows terms — already sanitised — to what may still be sent:
// nothing once the provider has refused a list.
func (c *Client) liveKeyterms(terms []string) []string {
	if c.keytermsRefused.Load() {
		return nil
	}
	return terms
}

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
		log:     slog.Default(),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}
