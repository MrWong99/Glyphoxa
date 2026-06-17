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
// this interface with no rework (ADR-0004).
var _ llm.Provider = (*groq.Client)(nil)

// sseServer returns an httptest server that replies to /chat/completions with
// the given pre-built SSE chunk lines, captures the request body for assertions,
// and runs without any API key reaching a live endpoint (the cassette-replay
// posture per ADR-0021).
func sseServer(t *testing.T, capture *atomic.Value, events ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := readBody(r)
		if capture != nil {
			capture.Store(body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, ev := range events {
			_, _ = w.Write([]byte(ev))
		}
	}))
}

func readBody(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for {
		n, err := r.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			return buf, nil
		}
	}
}

// collect drains a completion stream into a slice for assertion.
func collect(t *testing.T, ch <-chan llm.StreamEvent) []llm.StreamEvent {
	t.Helper()
	var out []llm.StreamEvent
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

// sse formats one JSON payload as an OpenAI-compatible SSE "data:" frame.
func sse(data string) string {
	return "data: " + data + "\n\n"
}

// TestNew_NoKey_NoEnv_CompleteReturnsMissingKeyError pins the link-time-safety
// property shared with the gemini and elevenlabs adapters: New must not panic
// without an API key (cassette-replay test binaries link this package
// unconditionally); the missing-key error surfaces at the first request.
func TestNew_NoKey_NoEnv_CompleteReturnsMissingKeyError(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "")
	c := groq.New("")
	_, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err == nil {
		t.Fatal("Complete without API key returned nil error")
	}
	if !strings.Contains(err.Error(), "missing API key") {
		t.Errorf("error %q does not mention missing API key", err)
	}
}

// TestComplete_TextStream_AccumulatesDeltas pins the smallest end-to-end loop: a
// text-only completion streams content deltas that decode to ordered
// [llm.EventText] values, terminated by an [llm.EventDone] carrying the
// finish_reason. The terminal "[DONE]" sentinel must not surface as an event.
func TestComplete_TextStream_AccumulatesDeltas(t *testing.T) {
	srv := sseServer(t, nil,
		sse(`{"choices":[{"delta":{"role":"assistant","content":""}}]}`),
		sse(`{"choices":[{"delta":{"content":"Hello"}}]}`),
		sse(`{"choices":[{"delta":{"content":", traveler."}}]}`),
		sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`),
		"data: [DONE]\n\n",
	)
	defer srv.Close()

	c := groq.New("k", groq.WithBaseURL(srv.URL))
	ch, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "Greet me."}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	events := collect(t, ch)
	var text strings.Builder
	var sawDone bool
	for _, ev := range events {
		switch ev.Type {
		case llm.EventText:
			text.WriteString(ev.Text)
		case llm.EventDone:
			sawDone = true
			if ev.StopReason != "stop" {
				t.Errorf("stop reason = %q, want stop", ev.StopReason)
			}
		}
	}
	if got := text.String(); got != "Hello, traveler." {
		t.Errorf("accumulated text = %q, want %q", got, "Hello, traveler.")
	}
	if !sawDone {
		t.Error("no EventDone in stream")
	}
}

// TestComplete_ToolUseStream_DecodesCall pins the tool-use decode (the ADR-0028
// seam): a first delta carrying the tool_calls index/id/name, a run of deltas
// appending function.arguments fragments, and the finish_reason "tool_calls"
// produce exactly one [llm.EventToolCall] with the id, name, and reassembled
// JSON input. This is the most important thing to pin per ADR-0021's LLM
// cassette policy — the id is what correlates the later tool result.
func TestComplete_ToolUseStream_DecodesCall(t *testing.T) {
	srv := sseServer(t, nil,
		sse(`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"dice","arguments":""}}]}}]}`),
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"notation\""}}]}}]}`),
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"1d20\"}"}}]}}]}`),
		sse(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`),
		"data: [DONE]\n\n",
	)
	defer srv.Close()

	c := groq.New("k", groq.WithBaseURL(srv.URL))
	ch, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "roll a d20"}},
		Tools: []llm.ToolDef{{
			Name:        "dice",
			Description: "Roll dice",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"notation":{"type":"string"}}}`),
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	events := collect(t, ch)
	var calls []llm.ToolCall
	var stop string
	for _, ev := range events {
		switch ev.Type {
		case llm.EventToolCall:
			calls = append(calls, ev.ToolCall)
		case llm.EventDone:
			stop = ev.StopReason
		}
	}
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
	tc := calls[0]
	if tc.ID != "call_9" || tc.Name != "dice" {
		t.Errorf("tool call id/name = %q/%q, want call_9/dice", tc.ID, tc.Name)
	}
	// Assert the JSON parses and round-trips the field, not the raw bytes — the
	// reassembled string must be valid JSON.
	var args struct {
		Notation string `json:"notation"`
	}
	if err := json.Unmarshal(tc.Input, &args); err != nil {
		t.Fatalf("tool input %q is not valid JSON: %v", tc.Input, err)
	}
	if args.Notation != "1d20" {
		t.Errorf("tool input notation = %q, want 1d20", args.Notation)
	}
	if stop != "tool_calls" {
		t.Errorf("stop reason = %q, want tool_calls", stop)
	}
}

// TestComplete_RequestShape_PinsBodyAndHeaders is the adapter↔Groq compat API
// contract: the request must carry the BYOK key in an Authorization Bearer
// header, stream=true, the system prompt as a "system"-role message (NOT lifted
// out, unlike Anthropic), the user text as a user message, and the tools array
// as function tools with tool_choice "auto". Pinning this catches accidental
// drift on either side.
func TestComplete_RequestShape_PinsBodyAndHeaders(t *testing.T) {
	var capture atomic.Value
	var seenAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/openai/v1/chat/completions" {
			t.Errorf("path = %q, want /openai/v1/chat/completions", r.URL.Path)
		}
		seenAuth.Store(r.Header.Get("Authorization"))
		body, _ := readBody(r)
		capture.Store(body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)))
	}))
	defer srv.Close()

	// Base URL includes the /openai/v1 prefix the live endpoint carries, so the
	// path assertion above also pins that the adapter appends /chat/completions
	// rather than swallowing the configured prefix.
	c := groq.New("expected-key", groq.WithBaseURL(srv.URL+"/openai/v1"), groq.WithModel("groq-test"))
	ch, err := c.Complete(context.Background(), llm.Request{
		MaxTokens: 256,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Text: "You are Bart the innkeeper."},
			{Role: llm.RoleUser, Text: "Hello Bart."},
		},
		Tools: []llm.ToolDef{{
			Name:        "dice",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	collect(t, ch) // drain

	if got, _ := seenAuth.Load().(string); got != "Bearer expected-key" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer expected-key")
	}

	raw, _ := capture.Load().([]byte)
	var body struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		Stream    bool   `json:"stream"`
		Messages  []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		ToolChoice string `json:"tool_choice"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("request body not JSON: %v\nbody: %s", err, raw)
	}
	if body.Model != "groq-test" {
		t.Errorf("model = %q, want groq-test", body.Model)
	}
	if body.MaxTokens != 256 {
		t.Errorf("max_tokens = %d, want 256", body.MaxTokens)
	}
	if !body.Stream {
		t.Error("stream = false, want true")
	}
	// Unlike Anthropic, the system prompt stays as a message — first one, system
	// role.
	if len(body.Messages) != 2 {
		t.Fatalf("messages = %+v, want two (system, user)", body.Messages)
	}
	if body.Messages[0].Role != "system" || body.Messages[0].Content != "You are Bart the innkeeper." {
		t.Errorf("messages[0] = %+v, want system role with the Persona text", body.Messages[0])
	}
	if body.Messages[1].Role != "user" || body.Messages[1].Content != "Hello Bart." {
		t.Errorf("messages[1] = %+v, want user role 'Hello Bart.'", body.Messages[1])
	}
	if len(body.Tools) != 1 || body.Tools[0].Type != "function" || body.Tools[0].Function.Name != "dice" {
		t.Errorf("tools = %+v, want one function tool named dice", body.Tools)
	}
	if body.ToolChoice != "auto" {
		t.Errorf("tool_choice = %q, want auto when tools present", body.ToolChoice)
	}
}

// TestComplete_ModelSelection pins the configurable-model contract: with no
// per-call or constructor override the body carries the Llama 3.3 70B default;
// [groq.WithModel] overrides that default; and a non-empty [llm.Request.Model]
// wins over both. This is the "default model, but configurable" requirement.
func TestComplete_ModelSelection(t *testing.T) {
	captureModel := func(t *testing.T, reqModel string, opts ...groq.Option) string {
		t.Helper()
		var capture atomic.Value
		srv := sseServer(t, &capture,
			sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`),
		)
		defer srv.Close()

		allOpts := append([]groq.Option{groq.WithBaseURL(srv.URL)}, opts...)
		c := groq.New("k", allOpts...)
		ch, err := c.Complete(context.Background(), llm.Request{
			Model:    reqModel,
			Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
		})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		collect(t, ch)

		raw, _ := capture.Load().([]byte)
		var body struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("request body not JSON: %v", err)
		}
		return body.Model
	}

	if got := captureModel(t, ""); got != groq.DefaultModel {
		t.Errorf("default model = %q, want %q", got, groq.DefaultModel)
	}
	if groq.DefaultModel != "llama-3.3-70b-versatile" {
		t.Errorf("DefaultModel = %q, want llama-3.3-70b-versatile", groq.DefaultModel)
	}
	if got := captureModel(t, "", groq.WithModel("llama-3.1-8b-instant")); got != "llama-3.1-8b-instant" {
		t.Errorf("WithModel default = %q, want llama-3.1-8b-instant", got)
	}
	// A per-call model wins over the constructor default.
	if got := captureModel(t, "openai/gpt-oss-120b", groq.WithModel("llama-3.1-8b-instant")); got != "openai/gpt-oss-120b" {
		t.Errorf("per-call model = %q, want openai/gpt-oss-120b", got)
	}
}

// TestComplete_ToolResultRoundTrip_MapsToToolRoleMessages pins the ADR-0028
// return path for the OpenAI-compat wire shape: a [llm.RoleTool] message (the
// results the tool-use loop appends after executing an assistant turn's calls)
// must serialize as one tool-role message PER result, each keyed by its
// tool_call_id — OpenAI's shape, where parallel results are SEPARATE messages
// (in contrast to Anthropic's single message with multiple blocks). The slice
// seam (not a single result) feeds all parallel results back, and the id is the
// correlation key the live tool loop depends on.
func TestComplete_ToolResultRoundTrip_MapsToToolRoleMessages(t *testing.T) {
	var capture atomic.Value
	srv := sseServer(t, &capture,
		sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`),
	)
	defer srv.Close()

	c := groq.New("k", groq.WithBaseURL(srv.URL))
	ch, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleUser, Text: "roll a d20 and a d6"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
				{ID: "call_9", Name: "dice", Input: json.RawMessage(`{"notation":"1d20"}`)},
				{ID: "call_10", Name: "dice", Input: json.RawMessage(`{"notation":"1d6"}`)},
			}},
			// Two parallel calls → two separate tool-role messages.
			{Role: llm.RoleTool, ToolResults: []llm.ToolResult{
				{CallID: "call_9", Content: "17"},
				{CallID: "call_10", Content: "4"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	collect(t, ch)

	raw, _ := capture.Load().([]byte)
	var body struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("request body not JSON: %v", err)
	}
	// user, assistant(tool_calls), tool(call_9), tool(call_10) = 4 messages.
	if len(body.Messages) != 4 {
		t.Fatalf("got %d messages, want 4 (user, assistant, tool, tool)", len(body.Messages))
	}

	// The assistant turn must carry both tool_calls with their ids and the
	// arguments as a JSON string.
	asst := body.Messages[1]
	if asst.Role != "assistant" || len(asst.ToolCalls) != 2 {
		t.Fatalf("messages[1] = %+v, want assistant with two tool_calls", asst)
	}
	if asst.ToolCalls[0].ID != "call_9" || asst.ToolCalls[0].Type != "function" || asst.ToolCalls[0].Function.Name != "dice" {
		t.Errorf("assistant tool_calls[0] = %+v, want call_9/function/dice", asst.ToolCalls[0])
	}
	if asst.ToolCalls[0].Function.Arguments != `{"notation":"1d20"}` {
		t.Errorf("assistant tool_calls[0] arguments = %q, want the JSON string", asst.ToolCalls[0].Function.Arguments)
	}

	// Each result is its own tool-role message keyed by tool_call_id.
	got := map[string]string{}
	for _, m := range body.Messages[2:] {
		if m.Role != "tool" {
			t.Errorf("message %+v role = %q, want tool", m, m.Role)
		}
		got[m.ToolCallID] = m.Content
	}
	if got["call_9"] != "17" || got["call_10"] != "4" {
		t.Errorf("tool-role messages = %v, want {call_9:17, call_10:4}", got)
	}
}

// TestComplete_Non2xx_WrapsOpAndStatus pins the error-surface shape shared with
// the gemini and elevenlabs adapters: a non-2xx response yields an error naming
// the operation and the HTTP status with a body snippet.
func TestComplete_Non2xx_WrapsOpAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API Key","type":"invalid_request_error","code":"invalid_api_key"}}`))
	}))
	defer srv.Close()

	c := groq.New("bad", groq.WithBaseURL(srv.URL))
	_, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err == nil {
		t.Fatal("Complete with 401 returned nil error")
	}
	for _, must := range []string{"Complete", "401", "invalid_api_key"} {
		if !strings.Contains(err.Error(), must) {
			t.Errorf("error %q missing required substring %q", err, must)
		}
	}
}

// TestComplete_MalformedFrame_EmitsEventError pins the truncation contract: a
// frame that fails to decode must surface as a terminal [llm.EventError], not a
// silent channel close — consumers would otherwise speak the partial text as a
// complete reply (and `-tags=record` would bake it into a cassette).
func TestComplete_MalformedFrame_EmitsEventError(t *testing.T) {
	srv := sseServer(t, nil,
		sse(`{"choices":[{"delta":{"content":"Half a sen"}}]}`),
		sse(`{not json`),
	)
	defer srv.Close()

	c := groq.New("k", groq.WithBaseURL(srv.URL))
	ch, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "Greet me."}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	events := collect(t, ch)
	last := events[len(events)-1]
	if last.Type != llm.EventError {
		t.Fatalf("last event type = %v, want EventError", last.Type)
	}
	if last.Err == "" {
		t.Error("EventError carries no message")
	}
	for _, ev := range events {
		if ev.Type == llm.EventDone {
			t.Error("stream emitted EventDone despite the malformed frame")
		}
	}
}
