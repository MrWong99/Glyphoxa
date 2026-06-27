// Package groq adapts the v2 LLM provider surface ([llm.Provider]) to Groq by
// preconfiguring the shared OpenAI-compatible adapter
// ([github.com/MrWong99/Glyphoxa/pkg/voice/llm/openaicompat]) for Groq's
// OpenAI-compatibility endpoint (api.groq.com/openai/v1/chat/completions).
//
// Groq serves open-weight models (Llama, etc.) behind that compat surface, so the
// adapter is a thin preset over the SDK-backed core (ADR-0037): base URL, key,
// default model, and token bound. The system prompt rides as a "system"-role
// message, tool results correlate to their call by an opaque id that maps 1:1
// onto [llm.ToolResult.CallID], and the tool-use loop (ADR-0028) slots in
// unchanged.
//
// Authentication is BYOK per ADR-0004: callers either pass the API key to [New]
// or set GROQ_API_KEY. [New] never fails so that cassette-replay test binaries
// can link this package without an API key configured — missing-key errors
// surface at request time instead, matching the anthropic, gemini, and stt/tts
// elevenlabs adapters' posture.
package groq

import (
	"net/http"
	"os"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/openaicompat"
)

const (
	// DefaultBaseURL is the Groq OpenAI-compatibility API root. The SDK appends
	// /chat/completions at request time.
	DefaultBaseURL = "https://api.groq.com/openai/v1"

	// APIKeyEnv is the environment variable [New] consults when its apiKey
	// argument is empty.
	APIKeyEnv = "GROQ_API_KEY"

	// ProviderID is the stable string identifying this LLM adapter; it matches
	// the Provider Config's provider name (providers.llm.name: "groq").
	ProviderID = "groq"

	// DefaultModel is used when [llm.Request.Model] is empty. Groq's Llama 3.3
	// 70B production id (ADR-0036); override per-client with [WithModel] or
	// per-call with [llm.Request.Model].
	DefaultModel = "llama-3.3-70b-versatile"

	// DefaultMaxTokens caps a completion when [llm.Request.MaxTokens] is zero.
	// Llama 3.3 70B is not a thinking model — every token counts toward the
	// spoken reply — so the ceiling matches the anthropic adapter's tighter bound
	// rather than gemini's thinking-token headroom.
	DefaultMaxTokens = 1024
)

// Models is the curated allowlist of Groq production models the Configuration
// model select offers. Groq exposes no programmatic list-models call we surface
// to the operator (ADR-0039), so the choice is a static, reviewed allowlist
// rather than live data. [DefaultModel] is first so it is the default selection.
// Extend this slice (not the UI) to offer another model.
var Models = []string{
	DefaultModel, // llama-3.3-70b-versatile
	"llama-3.1-8b-instant",
	"llama3-70b-8192",
	"llama3-8b-8192",
}

// Client is the Groq LLM adapter: the shared [openaicompat.Client] preconfigured
// by [New]. Construct with [New]; the zero value is not usable. Safe for
// concurrent use across goroutines.
type Client = openaicompat.Client

// Option customises the Groq [Client]; it mirrors the subset of
// [openaicompat.Option]s meaningful for Groq.
type Option = openaicompat.Option

// WithBaseURL overrides the API base URL. Useful for tests (httptest server) and
// self-hosted gateways.
func WithBaseURL(u string) Option { return openaicompat.WithBaseURL(u) }

// WithModel sets the default model used when [llm.Request.Model] is empty.
func WithModel(m string) Option { return openaicompat.WithModel(m) }

// WithHTTPClient supplies a custom http.Client (see [openaicompat.WithHTTPClient]).
func WithHTTPClient(h *http.Client) Option { return openaicompat.WithHTTPClient(h) }

// New constructs a Groq [Client]. If apiKey is empty it falls back to the
// GROQ_API_KEY environment variable; if that is also empty, the returned client
// still links — calls return a "missing API key" error rather than panicking on
// construction, so cassette-replay test binaries can import this package
// unconditionally.
func New(apiKey string, opts ...Option) *Client {
	if apiKey == "" {
		apiKey = os.Getenv(APIKeyEnv)
	}
	base := []openaicompat.Option{
		openaicompat.WithProviderName(ProviderID),
		openaicompat.WithAPIKey(apiKey),
		openaicompat.WithAPIKeyEnv(APIKeyEnv),
		openaicompat.WithBaseURL(DefaultBaseURL),
		openaicompat.WithModel(DefaultModel),
		openaicompat.WithDefaultMaxTokens(DefaultMaxTokens),
	}
	// Caller options come last so WithBaseURL/WithModel overrides win.
	return openaicompat.New(append(base, opts...)...)
}
