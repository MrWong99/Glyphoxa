// Package imagegen is the AI image-generation seam (#311, Epic 8, ADR-0004
// amendment): a small [Generator] interface plus a Gemini adapter that turns a
// text prompt into a single generated image. The highlight enrichment job
// (internal/highlight, ADR-0049) drives it post-promotion and lands the result
// on the Highlight through the blob seam (ADR-0048).
//
// The Gemini adapter is a plain net/http client against the native
// generateContent REST surface, deliberately NOT the Google SDK — the same
// posture internal/discordinvite / internal/discordtag take (an SDK's rate
// limiter leaks a cleanup goroutine per call, and the request/response shape here
// is one POST). The base URL, model, and http.Client are seams so unit tests
// drive a httptest fake and the default `go test` makes no vendor call.
package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/blob"
)

// ErrImageTooLarge is returned when a generated image exceeds the blob seam's
// per-blob cap (blob.MaxSize). It is a PERMANENT failure — retrying re-bills the
// same oversize generation — so the enrichment handler treats it as a clean no-op
// (log + leave the Highlight intact) rather than a retryable error.
var ErrImageTooLarge = errors.New("imagegen: generated image exceeds the blob size cap")

const (
	// ProviderID is the stable string identifying this adapter; it matches the
	// Provider Config's provider name and observe.ProviderGemini.
	ProviderID = "gemini"

	// DefaultModel is the Gemini image-generation model used when no model is
	// configured. It is the "flash image" preview model (billed per output image,
	// ~1290 output tokens each — see internal/spend/prices.go).
	DefaultModel = "gemini-2.5-flash-image"

	// DefaultBaseURL is the native Generative Language API root. The request path
	// {base}/v1beta/models/{model}:generateContent is appended per call.
	DefaultBaseURL = "https://generativelanguage.googleapis.com"

	// APIKeyEnv is the environment variable the enrichment factory falls back to
	// when no BYOK key is stored (the hybrid key policy, ADR-0039).
	APIKeyEnv = "GEMINI_API_KEY"

	// apiKeyHeader is Gemini's native-endpoint auth header (NOT a Bearer token —
	// the compat endpoint uses Authorization, this one uses x-goog-api-key).
	apiKeyHeader = "x-goog-api-key"

	// maxResponseBytes bounds the response read: the blob cap (blob.MaxSize)
	// doubled covers a full-cap image base64-encoded (~4/3 inflation) plus JSON
	// envelope, so an at-cap image still parses while an unbounded body cannot
	// exhaust memory. The post-decode blob.MaxSize check owns the actual reject.
	maxResponseBytes = 2 * blob.MaxSize
)

// Result is one generated image plus the token counts the provider metered for
// the request (ADR-0045): PromptTokens is the input, OutputTokens the image's
// billed output tokens (Gemini bills a generated image as output tokens, so it
// prices through the LLM usage sink — no image-specific meter, #311).
type Result struct {
	Data         []byte
	ContentType  string
	PromptTokens int
	OutputTokens int
}

// Generator turns a text prompt into a single generated image. *Gemini is the
// v1 implementation; the enrichment job depends on this interface so tests fake
// it and later providers can slot in behind ADR-0004.
type Generator interface {
	Generate(ctx context.Context, prompt string) (Result, error)
}

// Option customises a [Gemini] adapter.
type Option func(*Gemini)

// WithBaseURL overrides the API base URL (httptest server / self-hosted gateway).
func WithBaseURL(u string) Option { return func(g *Gemini) { g.baseURL = u } }

// WithModel sets the image model requested (defaults to [DefaultModel]).
func WithModel(m string) Option { return func(g *Gemini) { g.model = m } }

// WithHTTPClient supplies a custom http.Client (timeouts, transport). Defaults to
// a client with a generous timeout — image generation is slower than a text turn.
func WithHTTPClient(h *http.Client) Option { return func(g *Gemini) { g.httpClient = h } }

// Gemini is the plain-net/http Gemini image adapter. Construct with [NewGemini];
// the zero value is not usable. Safe for concurrent use.
type Gemini struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
}

var _ Generator = (*Gemini)(nil)

// NewGemini builds a Gemini image [Generator]. An empty apiKey falls back to
// GEMINI_API_KEY (the hybrid env path, ADR-0039); if that is also empty, Generate
// returns a missing-key error rather than panicking, so a keyless boot links.
func NewGemini(apiKey string, opts ...Option) *Gemini {
	if apiKey == "" {
		apiKey = os.Getenv(APIKeyEnv)
	}
	g := &Gemini{
		apiKey:     apiKey,
		baseURL:    DefaultBaseURL,
		model:      DefaultModel,
		httpClient: &http.Client{Timeout: 90 * time.Second},
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// generateContentRequest is the native generateContent body. It asks for the
// IMAGE response modality so the model returns inline image bytes.
type generateContentRequest struct {
	Contents         []reqContent     `json:"contents"`
	GenerationConfig generationConfig `json:"generationConfig"`
}

type reqContent struct {
	Parts []reqPart `json:"parts"`
}

type reqPart struct {
	Text string `json:"text"`
}

type generationConfig struct {
	ResponseModalities []string `json:"responseModalities"`
}

// generateContentResponse is the subset of the native response we parse: the
// first candidate's inline image data + the usage token counts.
type generateContentResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				InlineData *struct {
					MimeType string `json:"mimeType"`
					Data     string `json:"data"`
				} `json:"inlineData"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
}

// Generate POSTs the prompt to Gemini's generateContent and returns the first
// inline image with its MIME type and the request's token counts. An empty key,
// a non-2xx status, or a response carrying no inline image data is an error (the
// enrichment job returns it so the runner retries / dead-letters, leaving the
// Highlight intact).
func (g *Gemini) Generate(ctx context.Context, prompt string) (Result, error) {
	if g.apiKey == "" {
		return Result{}, fmt.Errorf("imagegen: missing API key (set %s)", APIKeyEnv)
	}

	body, err := json.Marshal(generateContentRequest{
		Contents: []reqContent{{Parts: []reqPart{{Text: prompt}}}},
		// ["TEXT","IMAGE"], NOT image-only: the canonical Gemini image-generation
		// examples request both modalities, and image-only has 400'd on prior models.
		// The parser skips any leading text part and takes the first inlineData image.
		GenerationConfig: generationConfig{ResponseModalities: []string{"TEXT", "IMAGE"}},
	})
	if err != nil {
		return Result{}, fmt.Errorf("imagegen: marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent", g.baseURL, g.model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("imagegen: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(apiKeyHeader, g.apiKey)

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("imagegen: request: %w", err)
	}
	defer resp.Body.Close()

	// Bound the response read to the blob cap plus base64/JSON headroom (base64
	// inflates ~4/3): a body that decodes past blob.MaxSize can never be stored, so
	// reading beyond this only wastes memory. maxResponseBytes over-reads slightly
	// so an at-cap image still parses and the post-decode check owns the reject.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return Result{}, fmt.Errorf("imagegen: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("imagegen: HTTP %d: %s", resp.StatusCode, truncate(respBody, 256))
	}

	var parsed generateContentResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Result{}, fmt.Errorf("imagegen: decode response: %w", err)
	}

	for _, c := range parsed.Candidates {
		for _, p := range c.Content.Parts {
			if p.InlineData == nil || p.InlineData.Data == "" {
				continue
			}
			data, err := base64.StdEncoding.DecodeString(p.InlineData.Data)
			if err != nil {
				return Result{}, fmt.Errorf("imagegen: decode inline image: %w", err)
			}
			if int64(len(data)) > blob.MaxSize {
				return Result{}, ErrImageTooLarge
			}
			return Result{
				Data:         data,
				ContentType:  p.InlineData.MimeType,
				PromptTokens: parsed.UsageMetadata.PromptTokenCount,
				OutputTokens: parsed.UsageMetadata.CandidatesTokenCount,
			}, nil
		}
	}
	return Result{}, fmt.Errorf("imagegen: response carried no inline image data")
}

// truncate bounds an error's echoed response body so a huge error page cannot
// bloat a log line.
func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
