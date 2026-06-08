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
// contract the Agent loop depends on — so the live NPC can swap Anthropic for
// Gemini behind this interface with no rework (ADR-0004).
var _ llm.Provider = (*gemini.Client)(nil)

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

// TestNew_NoKey_NoEnv_CompleteReturnsMissingKeyError pins the link-time-safety
// property shared with the anthropic and elevenlabs adapters: New must not
// panic without an API key (cassette-replay test binaries link this package
// unconditionally); the missing-key error surfaces at the first request.
func TestNew_NoKey_NoEnv_CompleteReturnsMissingKeyError(t *testing.T) {
	t.Setenv("GEMINI_API_KEY", "")
	c := gemini.New("")
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

	c := gemini.New("k", gemini.WithBaseURL(srv.URL))
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

	c := gemini.New("k", gemini.WithBaseURL(srv.URL))
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

// TestComplete_RequestShape_PinsBodyAndHeaders is the adapter↔Gemini compat API
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
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %q, want /chat/completions", r.URL.Path)
		}
		seenAuth.Store(r.Header.Get("Authorization"))
		body, _ := readBody(r)
		capture.Store(body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)))
	}))
	defer srv.Close()

	c := gemini.New("expected-key", gemini.WithBaseURL(srv.URL), gemini.WithModel("gemini-test"))
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
	if body.Model != "gemini-test" {
		t.Errorf("model = %q, want gemini-test", body.Model)
	}
	if body.MaxTokens != 256 {
		t.Errorf("max_tokens = %d, want 256", body.MaxTokens)
	}
	if !body.Stream {
		t.Error("stream = false, want true")
	}
	// Unlike Anthropic, the system prompt stays as a message — first one,
	// system role.
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

	c := gemini.New("k", gemini.WithBaseURL(srv.URL))
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
// the anthropic and elevenlabs adapters: a non-2xx response yields an error
// naming the operation and the HTTP status with a body snippet.
func TestComplete_Non2xx_WrapsOpAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":401,"status":"UNAUTHENTICATED","message":"API key not valid"}}`))
	}))
	defer srv.Close()

	c := gemini.New("bad", gemini.WithBaseURL(srv.URL))
	_, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err == nil {
		t.Fatal("Complete with 401 returned nil error")
	}
	for _, must := range []string{"Complete", "401", "UNAUTHENTICATED"} {
		if !strings.Contains(err.Error(), must) {
			t.Errorf("error %q missing required substring %q", err, must)
		}
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
	srv := sseServer(t, &capture,
		sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`),
	)
	defer srv.Close()

	allOpts := append([]gemini.Option{gemini.WithBaseURL(srv.URL)}, opts...)
	c := gemini.New("k", allOpts...)
	ch, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	collect(t, ch)

	raw, _ := capture.Load().([]byte)
	var body thinkingBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("request body not JSON: %v\nbody: %s", err, raw)
	}
	return body
}

// TestComplete_DefaultCapsThinking pins the B2 default: a client built with no
// thinking Option still sends reasoning_effort "low" to bound 2.5-flash's
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
// budget and SUPPRESSES reasoning_effort, so the endpoint never receives both
// (it rejects that). A budget of 0 (thinking off) must still be emitted — the
// nil/0 distinction is load-bearing.
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

// sse formats one JSON payload as an OpenAI-compatible SSE "data:" frame.
func sse(data string) string {
	return "data: " + data + "\n\n"
}
