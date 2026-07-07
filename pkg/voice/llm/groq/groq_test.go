package groq_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/groq"
)

// Compile-time assertion: [groq.Client] satisfies [llm.Provider], the only
// contract the Agent loop depends on — so the live NPC can swap in Groq behind
// this interface with no rework (ADR-0004). The generic adapter behaviour is
// covered once in pkg/voice/llm/openaicompat; these tests pin only the Groq
// preset's own defaults.
var _ llm.Provider = (*groq.Client)(nil)

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

// TestNew_NoKey_NoEnv_CompleteReturnsMissingKeyError pins the link-time-safety
// property shared with the gemini and elevenlabs adapters: New must not panic
// without an API key (cassette-replay test binaries link this package
// unconditionally); the missing-key error surfaces at the first request and names
// GROQ_API_KEY.
func TestNew_NoKey_NoEnv_CompleteReturnsMissingKeyError(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "")
	c := groq.New("")
	_, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err == nil {
		t.Fatal("Complete without API key returned nil error")
	}
	for _, must := range []string{"groq.Complete", "missing API key", "GROQ_API_KEY"} {
		if !strings.Contains(err.Error(), must) {
			t.Errorf("error %q missing required substring %q", err, must)
		}
	}
}

// TestComplete_GroqDefaults_PinsPathKeyAndModel pins the Groq preset: the BYOK key
// rides as an Authorization Bearer header, the base URL's /openai/v1 prefix is
// preserved (the adapter appends /chat/completions rather than swallowing it), and
// an unspecified model falls back to the Llama 3.3 70B default (ADR-0036).
func TestComplete_GroqDefaults_PinsPathKeyAndModel(t *testing.T) {
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

	// Base URL includes the /openai/v1 prefix the live endpoint carries.
	c := groq.New("expected-key", groq.WithBaseURL(srv.URL+"/openai/v1"))
	ch, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "Hello Bart."}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	drain(ch)

	if got, _ := seenPath.Load().(string); got != "/openai/v1/chat/completions" {
		t.Errorf("path = %q, want /openai/v1/chat/completions", got)
	}
	if got, _ := seenAuth.Load().(string); got != "Bearer expected-key" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer expected-key")
	}
	if groq.DefaultModel != "llama-3.3-70b-versatile" {
		t.Errorf("DefaultModel = %q, want llama-3.3-70b-versatile", groq.DefaultModel)
	}
	if got := bodyModel(t, capture); got != groq.DefaultModel {
		t.Errorf("default model = %q, want %q", got, groq.DefaultModel)
	}
}

// TestComplete_ModelOverride pins that [groq.WithModel] overrides the default and
// a per-call [llm.Request.Model] wins over both.
func TestComplete_ModelOverride(t *testing.T) {
	capture := func(t *testing.T, reqModel string, opts ...groq.Option) string {
		t.Helper()
		var cap atomic.Value
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cap.Store(readBody(r))
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)))
		}))
		defer srv.Close()
		c := groq.New("k", append([]groq.Option{groq.WithBaseURL(srv.URL)}, opts...)...)
		ch, err := c.Complete(context.Background(), llm.Request{
			Model:    reqModel,
			Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
		})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		drain(ch)
		return bodyModel(t, cap)
	}

	if got := capture(t, "", groq.WithModel("llama-3.1-8b-instant")); got != "llama-3.1-8b-instant" {
		t.Errorf("WithModel default = %q, want llama-3.1-8b-instant", got)
	}
	if got := capture(t, "openai/gpt-oss-120b", groq.WithModel("llama-3.1-8b-instant")); got != "openai/gpt-oss-120b" {
		t.Errorf("per-call model = %q, want openai/gpt-oss-120b", got)
	}
}

// TestComplete_GroqPreset_RequestsUsage pins the #127 preset gate: Groq honours
// stream_options, so the preset asks for the trailing usage chunk
// (include_usage=true on the wire) to meter tokens (ADR-0045).
func TestComplete_GroqPreset_RequestsUsage(t *testing.T) {
	var capture atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capture.Store(readBody(r))
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)))
	}))
	defer srv.Close()

	c := groq.New("k", groq.WithBaseURL(srv.URL))
	ch, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	drain(ch)

	raw, _ := capture.Load().([]byte)
	var body struct {
		StreamOptions *struct {
			IncludeUsage *bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("request body not JSON: %v\n%s", err, raw)
	}
	if body.StreamOptions == nil || body.StreamOptions.IncludeUsage == nil || !*body.StreamOptions.IncludeUsage {
		t.Errorf("Groq preset must set stream_options.include_usage=true; got %s", raw)
	}
}

// bodyModel extracts the "model" field from a captured request body.
func bodyModel(t *testing.T, capture atomic.Value) string {
	t.Helper()
	raw, _ := capture.Load().([]byte)
	var body struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("request body not JSON: %v\nbody: %s", err, raw)
	}
	return body.Model
}
