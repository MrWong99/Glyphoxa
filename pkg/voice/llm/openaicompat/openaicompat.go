// Package openaicompat implements the v2 LLM provider surface ([llm.Provider])
// against any OpenAI-compatible /chat/completions endpoint, built on the
// official OpenAI Go SDK (github.com/openai/openai-go/v3) pointed at a
// configurable base URL.
//
// One adapter serves every provider that speaks the OpenAI chat-completions
// wire — Groq, Gemini's OpenAI-compat surface, OpenAI itself, local Ollama —
// because the only per-provider differences are the base URL, the API key, the
// default model, and a few pass-through request knobs. The thin preset packages
// [github.com/MrWong99/Glyphoxa/pkg/voice/llm/groq] and
// [github.com/MrWong99/Glyphoxa/pkg/voice/llm/gemini] wrap this core with those
// defaults (and, for Gemini, the thinking-cap knobs of ADR-0035); the live NPC
// wiring and observability key off their stable ProviderIDs unchanged.
//
// Adopting the SDK (ADR-0037) replaces the hand-rolled net/http + bufio SSE
// machinery the groq and gemini adapters previously duplicated: the SDK owns the
// request marshalling, SSE framing, and typed streaming, while this package owns
// the translation between the [llm] message vocabulary and the SDK's
// params/stream types. The system prompt rides as a "system"-role message, tool
// results correlate to their call by an opaque id that maps 1:1 onto
// [llm.ToolResult.CallID], and the tool-use loop (ADR-0028) slots in unchanged.
//
// Authentication is BYOK per ADR-0004: the preset packages pass the API key to
// [New] or resolve it from the provider's env var first. [New] never fails so
// that cassette-replay test binaries can link without a key configured — a
// missing key surfaces as an error at [Client.Complete] time (matching the
// anthropic and stt/tts elevenlabs adapters' posture). The SDK is always pinned
// to the configured key and base URL, so it can never silently fall back to
// OPENAI_API_KEY / OPENAI_BASE_URL.
package openaicompat

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// DefaultMaxTokens caps a completion when neither [llm.Request.MaxTokens] nor a
// preset default is set. Preset packages override this with a
// provider-appropriate bound (a thinking model wants more headroom than a
// non-reasoning one).
const DefaultMaxTokens = 1024

// Client is the OpenAI-compatible LLM adapter. Construct with [New]; the zero
// value is not usable. Safe for concurrent use across goroutines (the SDK
// client is).
type Client struct {
	// name is the provider id used in error messages (e.g. "groq.Complete: …").
	name string
	// apiKey is the resolved BYOK key. Empty means [Client.Complete] returns a
	// missing-key error rather than calling the endpoint.
	apiKey string
	// apiKeyEnvHint, when set, is the env var named in the missing-key error so
	// the message stays as actionable as the hand-rolled adapters' was.
	apiKeyEnvHint string
	// model is the default model id used when [llm.Request.Model] is empty.
	model string
	// maxTokens is the default max_tokens used when [llm.Request.MaxTokens] <= 0.
	maxTokens int

	// reasoningEffort, when non-empty, rides as the OpenAI-compat reasoning_effort
	// field (the ADR-0035 thinking cap for Gemini). Empty omits it.
	reasoningEffort string
	// extraFields are non-standard top-level body fields merged onto every request
	// via the SDK's JSON-set escape hatch (e.g. Gemini's
	// extra_body.google.thinking_config). Nil omits them.
	extraFields map[string]any
	// includeUsage, when true, sets stream_options.include_usage so the provider
	// streams a trailing usage chunk for token metering (#127, ADR-0045). It is
	// preset-gated OFF by default: a gateway that rejects stream_options would 400
	// every turn, so a preset only turns it on for endpoints known to honour it
	// (Groq, OpenAI); Gemini leaves it off.
	includeUsage bool

	oai openai.Client
}

// Option configures a [Client] during construction.
type Option func(*settings)

// settings accumulates [Option] state before it is frozen into a [Client]. It is
// kept separate from Client so options can be applied in any order and the SDK
// client built once from the resolved values.
type settings struct {
	name            string
	apiKey          string
	apiKeyEnvHint   string
	baseURL         string
	model           string
	maxTokens       int
	reasoningEffort string
	extraFields     map[string]any
	includeUsage    bool
	httpClient      *http.Client
}

// WithProviderName sets the provider id used in error messages (e.g. "groq").
func WithProviderName(name string) Option { return func(s *settings) { s.name = name } }

// WithAPIKey sets the BYOK key verbatim. An empty key links fine and defers the
// missing-key error to request time; presets resolve the provider env var first.
func WithAPIKey(key string) Option { return func(s *settings) { s.apiKey = key } }

// WithAPIKeyEnv names the env var quoted in the missing-key error message.
func WithAPIKeyEnv(env string) Option { return func(s *settings) { s.apiKeyEnvHint = env } }

// WithBaseURL sets the OpenAI-compatible API root. The SDK appends
// /chat/completions; pass the provider's full prefix (e.g. ".../openai/v1").
func WithBaseURL(u string) Option { return func(s *settings) { s.baseURL = u } }

// WithModel sets the default model used when [llm.Request.Model] is empty.
func WithModel(m string) Option { return func(s *settings) { s.model = m } }

// WithDefaultMaxTokens sets the default max_tokens used when
// [llm.Request.MaxTokens] is zero.
func WithDefaultMaxTokens(n int) Option { return func(s *settings) { s.maxTokens = n } }

// WithHTTPClient supplies a custom http.Client. The default ([defaultHTTPClient])
// has no overall timeout because streaming completions are long-lived — but it
// does bound dialing, TLS, and time-to-first-response-header; the per-call
// deadline for the whole exchange must come from the request context.
func WithHTTPClient(h *http.Client) Option { return func(s *settings) { s.httpClient = h } }

// WithReasoningEffort sets the OpenAI-compat reasoning_effort field sent on every
// request. Empty (the default) omits it. Used by the gemini preset for the
// ADR-0035 thinking cap.
func WithReasoningEffort(effort string) Option {
	return func(s *settings) { s.reasoningEffort = effort }
}

// WithExtraFields sets non-standard top-level request-body fields merged onto
// every completion (e.g. Gemini's extra_body passthrough). Nil omits them.
func WithExtraFields(f map[string]any) Option { return func(s *settings) { s.extraFields = f } }

// WithIncludeUsage enables stream_options.include_usage so the provider streams a
// trailing usage chunk for token metering (#127, ADR-0045). Off by default and
// preset-gated: only turn it on for endpoints known to honour stream_options
// (Groq, OpenAI). A gateway that rejects the field would 400 every turn, so
// Gemini leaves it off until verified.
func WithIncludeUsage(on bool) Option { return func(s *settings) { s.includeUsage = on } }

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

// errStreamBudget is the read error [budgetBody] returns once a response body
// exceeds maxStreamBytes. It surfaces through the SDK's SSE decoder as a stream
// read error, which [Client.streamEvents] reports as a terminal [llm.EventError]
// — the same surface as any other mid-stream failure.
var errStreamBudget = fmt.Errorf("response body exceeded %d bytes", maxStreamBytes)

// budgetTransport wraps an [http.RoundTripper] and caps every response body at
// maxStreamBytes raw bytes. The cap must live BELOW the SDK: openai-go's
// Stream.Next drains everything after a [DONE] sentinel inside its own loop
// without returning to the caller, so a hostile gateway that sends [DONE] first
// and then floods frames forever would bypass any guard in the adapter's chunk
// loop. A transport-level limit cannot be bypassed — it also bounds shapes the
// decoder never surfaces as chunks, such as one endless unterminated data: line.
type budgetTransport struct{ base http.RoundTripper }

func (t budgetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &budgetBody{rc: resp.Body, remaining: maxStreamBytes}
	return resp, nil
}

// budgetBody is an [io.LimitedReader]-style body that returns errStreamBudget
// (instead of io.EOF) once remaining is exhausted, so the SDK's decoder sees a
// hard read error rather than a clean end of stream.
type budgetBody struct {
	rc        io.ReadCloser
	remaining int64
}

func (b *budgetBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, errStreamBudget
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.rc.Read(p)
	b.remaining -= int64(n)
	return n, err
}

func (b *budgetBody) Close() error { return b.rc.Close() }

// New constructs a [Client] from opts. The API key is taken verbatim from
// [WithAPIKey] (the preset resolves it from the provider env var first); an empty
// key links fine and surfaces a missing-key error at [Client.Complete] time. The
// SDK is always given an explicit key and base URL so it never reads
// OPENAI_API_KEY / OPENAI_BASE_URL, and automatic retries are disabled so the
// voice hot path's latency is governed only by the request-context deadline and
// the project's own resilience layer — not hidden SDK backoff that could blow the
// time-to-first-token budget.
func New(opts ...Option) *Client {
	s := &settings{
		name:      "openai-compat",
		maxTokens: DefaultMaxTokens,
	}
	for _, opt := range opts {
		opt(s)
	}

	// The raw-byte stream budget rides on the transport (see budgetTransport) so
	// it cannot be bypassed by SSE shapes the SDK never surfaces as chunks. A
	// caller-supplied client is shallow-copied before its transport is wrapped.
	httpClient := s.httpClient
	if httpClient == nil {
		httpClient = defaultHTTPClient()
	} else {
		clone := *httpClient
		httpClient = &clone
	}
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = budgetTransport{base: base}

	// Both the key and the base URL are pinned unconditionally so the SDK's
	// env-derived defaults (OPENAI_API_KEY / OPENAI_BASE_URL) can never override
	// them — an empty base URL then fails loudly rather than silently retargeting
	// a BYOK key at api.openai.com. Presets always supply a non-empty base URL.
	reqOpts := []option.RequestOption{
		option.WithAPIKey(s.apiKey),
		option.WithBaseURL(s.baseURL),
		option.WithHTTPClient(httpClient),
		option.WithMaxRetries(0),
	}

	return &Client{
		name:            s.name,
		apiKey:          s.apiKey,
		apiKeyEnvHint:   s.apiKeyEnvHint,
		model:           s.model,
		maxTokens:       s.maxTokens,
		reasoningEffort: s.reasoningEffort,
		extraFields:     s.extraFields,
		includeUsage:    s.includeUsage,
		oai:             openai.NewClient(reqOpts...),
	}
}
