package gemini_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/gemini"
)

// Compile-time assertion: [gemini.Client] satisfies [llm.Provider], the only
// contract the Agent loop depends on (ADR-0004). The generic OpenAI-compat
// behaviour is covered once in pkg/voice/llm/openaicompat; these tests pin the
// Gemini preset's own defaults and the ADR-0035 thinking-cap knobs.
var _ llm.Provider = (*gemini.Client)(nil)

// sse formats one JSON payload as an OpenAI-compatible SSE "data:" frame.
func sse(data string) string { return "data: " + data + "\n\n" }

func readBody(r *http.Request) []byte {
	defer r.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf
		}
	}
}

func drain(ch <-chan llm.StreamEvent) {
	for range ch {
	}
}

// captureServer replies with a single stop frame and stores the request body.
func captureServer(t *testing.T, capture *atomic.Value) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			capture.Store(readBody(r))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)))
	}))
}

// TestComplete_GeminiPreset_OmitsStreamOptions pins the #127 preset gate's
// negative half (ADR-0045): the Gemini compat endpoint is not verified to honour
// stream_options, so the preset must NOT send include_usage — a gateway that
// rejects the field would 400 every turn. Token metering for Gemini falls back to
// the ceil(chars/4) estimate instead.
func TestComplete_GeminiPreset_OmitsStreamOptions(t *testing.T) {
	var capture atomic.Value
	srv := captureServer(t, &capture)
	defer srv.Close()

	c := gemini.New("k", gemini.WithBaseURL(srv.URL))
	ch, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	drain(ch)

	raw, _ := capture.Load().([]byte)
	if strings.Contains(string(raw), "stream_options") {
		t.Errorf("Gemini preset must omit stream_options (unverified endpoint); got %s", raw)
	}
}

// TestNew_NoKey_NoEnv_CompleteReturnsMissingKeyError pins the link-time-safety
// property shared with the anthropic and elevenlabs adapters: New must not panic
// without an API key; the missing-key error surfaces at the first request and
// names GEMINI_API_KEY.
func TestNew_NoKey_NoEnv_CompleteReturnsMissingKeyError(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	c := gemini.New("")
	_, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err == nil {
		t.Fatal("Complete without API key returned nil error")
	}
	for _, must := range []string{"gemini.Complete", "missing API key", "GEMINI_API_KEY"} {
		if !strings.Contains(err.Error(), must) {
			t.Errorf("error %q missing required substring %q", err, must)
		}
	}
}

// TestComplete_GeminiDefaults pins the Gemini preset: the BYOK key rides as an
// Authorization Bearer header, the request targets /chat/completions, and an
// unspecified model falls back to gemini-2.5-flash.
func TestComplete_GeminiDefaults(t *testing.T) {
	var capture, seenAuth, seenPath atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath.Store(r.URL.Path)
		seenAuth.Store(r.Header.Get("Authorization"))
		capture.Store(readBody(r))
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)))
	}))
	defer srv.Close()

	c := gemini.New("expected-key", gemini.WithBaseURL(srv.URL))
	ch, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "Hello Bart."}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	drain(ch)

	if got, _ := seenPath.Load().(string); got != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", got)
	}
	if got, _ := seenAuth.Load().(string); got != "Bearer expected-key" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer expected-key")
	}
	if gemini.DefaultModel != "gemini-2.5-flash" {
		t.Errorf("DefaultModel = %q, want gemini-2.5-flash", gemini.DefaultModel)
	}
	raw, _ := capture.Load().([]byte)
	var body struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("request body not JSON: %v", err)
	}
	if body.Model != gemini.DefaultModel {
		t.Errorf("default model = %q, want %q", body.Model, gemini.DefaultModel)
	}
}

// thinkingBody is the subset of the request body the thinking-cap tests inspect.
type thinkingBody struct {
	ReasoningEffort string `json:"reasoning_effort"`
	ExtraBody       *struct {
		Google struct {
			ThinkingConfig struct {
				ThinkingBudget int `json:"thinking_budget"`
			} `json:"thinking_config"`
		} `json:"google"`
	} `json:"extra_body"`
}

// captureThinking runs one drained completion against a capturing server built
// from opts and returns the decoded thinking-cap fields of the request body.
func captureThinking(t *testing.T, opts ...gemini.Option) thinkingBody {
	t.Helper()
	var capture atomic.Value
	srv := captureServer(t, &capture)
	defer srv.Close()

	c := gemini.New("k", append([]gemini.Option{gemini.WithBaseURL(srv.URL)}, opts...)...)
	ch, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	drain(ch)

	raw, _ := capture.Load().([]byte)
	var body thinkingBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("request body not JSON: %v\nbody: %s", err, raw)
	}
	return body
}

// TestComplete_DefaultCapsThinking pins the ADR-0035 default: a client built with
// no thinking Option still sends reasoning_effort "low" to bound 2.5-flash's
// dynamic thinking wall-time — so prod gets the cap without opting in — and does
// NOT send an extra_body thinking_budget (the two are mutually exclusive on the
// wire). This is the regression guard for "the cap silently stops shipping".
func TestComplete_DefaultCapsThinking(t *testing.T) {
	body := captureThinking(t)
	if body.ReasoningEffort != gemini.DefaultReasoningEffort {
		t.Errorf("reasoning_effort = %q, want default %q", body.ReasoningEffort, gemini.DefaultReasoningEffort)
	}
	if body.ReasoningEffort != "low" {
		t.Errorf("default reasoning_effort = %q, want low", body.ReasoningEffort)
	}
	if body.ExtraBody != nil {
		t.Errorf("extra_body = %+v, want omitted when reasoning_effort drives the cap", body.ExtraBody)
	}
}

// TestComplete_WithReasoningEffort pins the override: a non-empty effort rides as
// the top-level reasoning_effort field, empty sends neither field (model's old
// time-unbounded default), and either way no extra_body is emitted.
func TestComplete_WithReasoningEffort(t *testing.T) {
	for _, effort := range []string{"none", "high"} {
		body := captureThinking(t, gemini.WithReasoningEffort(effort))
		if body.ReasoningEffort != effort {
			t.Errorf("reasoning_effort = %q, want %q", body.ReasoningEffort, effort)
		}
		if body.ExtraBody != nil {
			t.Errorf("effort %q: extra_body = %+v, want omitted", effort, body.ExtraBody)
		}
	}

	// Empty effort disables the cap: neither field is sent.
	body := captureThinking(t, gemini.WithReasoningEffort(""))
	if body.ReasoningEffort != "" {
		t.Errorf("empty effort sent reasoning_effort = %q, want omitted", body.ReasoningEffort)
	}
	if body.ExtraBody != nil {
		t.Errorf("empty effort sent extra_body = %+v, want omitted", body.ExtraBody)
	}
}

// TestComplete_WithThinkingBudget pins the explicit-budget path and its mutual
// exclusivity: a budget rides under extra_body.google.thinking_config.thinking_
// budget and SUPPRESSES reasoning_effort, so the endpoint never receives both (it
// rejects that). A budget of 0 (thinking off) must still be emitted — the nil/0
// distinction is load-bearing.
func TestComplete_WithThinkingBudget(t *testing.T) {
	for _, budget := range []int{0, 512, -1} {
		body := captureThinking(t, gemini.WithThinkingBudget(budget))
		if body.ReasoningEffort != "" {
			t.Errorf("budget %d: reasoning_effort = %q, want omitted (mutually exclusive)", budget, body.ReasoningEffort)
		}
		if body.ExtraBody == nil {
			t.Fatalf("budget %d: extra_body omitted, want thinking_config", budget)
		}
		if got := body.ExtraBody.Google.ThinkingConfig.ThinkingBudget; got != budget {
			t.Errorf("thinking_budget = %d, want %d", got, budget)
		}
	}
}
