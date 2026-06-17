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

	httpClient := s.httpClient
	if httpClient == nil {
		httpClient = defaultHTTPClient()
	}

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
		oai:             openai.NewClient(reqOpts...),
	}
}
