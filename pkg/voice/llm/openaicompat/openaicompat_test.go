package openaicompat_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/openaicompat"
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
	"github.com/MrWong99/Glyphoxa/pkg/voice/retry"
)

// Compile-time assertion: [openaicompat.Client] satisfies [llm.Provider], the
// only contract the Agent loop depends on — so every preset (groq, gemini, …)
// swaps in behind this interface with no rework (ADR-0004).
var _ llm.Provider = (*openaicompat.Client)(nil)

// newClient builds a keyed adapter pointed at srv with a stable provider name and
// model, plus any extra options the test needs.
func newClient(srv string, opts ...openaicompat.Option) *openaicompat.Client {
	base := []openaicompat.Option{
		openaicompat.WithProviderName("test"),
		openaicompat.WithAPIKey("k"),
		openaicompat.WithBaseURL(srv),
		openaicompat.WithModel("test-model"),
	}
	return openaicompat.New(append(base, opts...)...)
}

// sseServer returns an httptest server that replies to /chat/completions with the
// given pre-built SSE chunk lines, captures the request body for assertions, and
// runs without any API key reaching a live endpoint (the cassette-replay posture
// per ADR-0021).
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
func sse(data string) string { return "data: " + data + "\n\n" }

// captureBody runs one drained completion against a capturing server and returns
// the raw request body for wire assertions.
func captureBody(t *testing.T, req llm.Request, opts ...openaicompat.Option) []byte {
	t.Helper()
	var capture atomic.Value
	srv := sseServer(t, &capture, sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`))
	defer srv.Close()
	c := newClient(srv.URL, opts...)
	ch, err := c.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	collect(t, ch)
	raw, _ := capture.Load().([]byte)
	return raw
}

// TestComplete_IncludeUsage_SetsStreamOptionsOnWire pins the #127 preset gate: with
// WithIncludeUsage the request carries stream_options.include_usage=true (so the
// trailing usage chunk is emitted); without it the field is omitted entirely — a
// gateway that rejects stream_options would otherwise 400 every turn (ADR-0045).
func TestComplete_IncludeUsage_SetsStreamOptionsOnWire(t *testing.T) {
	req := llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}}}

	on := captureBody(t, req, openaicompat.WithIncludeUsage(true))
	var body struct {
		StreamOptions *struct {
			IncludeUsage *bool `json:"include_usage"`
		} `json:"stream_options"`
	}
	if err := json.Unmarshal(on, &body); err != nil {
		t.Fatalf("request body not JSON: %v\n%s", err, on)
	}
	if body.StreamOptions == nil || body.StreamOptions.IncludeUsage == nil || !*body.StreamOptions.IncludeUsage {
		t.Errorf("with WithIncludeUsage the wire must carry stream_options.include_usage=true; got %s", on)
	}

	off := captureBody(t, req) // default: no include-usage
	if strings.Contains(string(off), "stream_options") {
		t.Errorf("without WithIncludeUsage the wire must omit stream_options entirely; got %s", off)
	}
}

// TestComplete_TrailingUsageChunk_EmitsEventUsage is THE hoisted-capture regression
// trap (#127): the final usage chunk arrives with an empty choices array. The
// chunk loop's len(choices)==0 continue guard would silently swallow it, so usage
// must be captured BEFORE that guard. One EventUsage with the reported tokens must
// reach the consumer.
func TestComplete_TrailingUsageChunk_EmitsEventUsage(t *testing.T) {
	srv := sseServer(t, nil,
		sse(`{"choices":[{"delta":{"content":"Hi"},"finish_reason":null}]}`),
		sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`),
		// Trailing usage chunk: empty choices, non-null usage.
		sse(`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}}`),
	)
	defer srv.Close()

	c := newClient(srv.URL, openaicompat.WithIncludeUsage(true))
	ch, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "Greet me."}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	var usageCount int
	for _, ev := range collect(t, ch) {
		if ev.Type == llm.EventUsage {
			usageCount++
			if ev.Usage.InputTokens != 100 || ev.Usage.OutputTokens != 50 {
				t.Errorf("usage = %+v, want {InputTokens:100, OutputTokens:50}", ev.Usage)
			}
		}
	}
	if usageCount != 1 {
		t.Fatalf("EventUsage count = %d, want exactly 1 (the trailing empty-choices chunk must not be swallowed)", usageCount)
	}
}

// TestNew_NoKey_CompleteReturnsMissingKeyError pins the link-time-safety property
// shared with the anthropic and elevenlabs adapters: New must not panic without
// an API key; the missing-key error surfaces at the first request and names both
// the provider and the configured env-var hint.
func TestNew_NoKey_CompleteReturnsMissingKeyError(t *testing.T) {
	c := openaicompat.New(
		openaicompat.WithProviderName("test"),
		openaicompat.WithAPIKeyEnv("TEST_API_KEY"),
	)
	_, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err == nil {
		t.Fatal("Complete without API key returned nil error")
	}
	for _, must := range []string{"test.Complete", "missing API key", "TEST_API_KEY"} {
		if !strings.Contains(err.Error(), must) {
			t.Errorf("error %q missing required substring %q", err, must)
		}
	}
}

// TestNew_NoBaseURL_DoesNotInheritEnv pins the base-URL half of the "never falls
// back to OPENAI_*" invariant: a client built without a base URL must NOT inherit
// OPENAI_BASE_URL from the environment (which would misroute a BYOK key to the
// wrong vendor). With the base URL pinned unconditionally an empty value fails
// loudly instead, and the env-named server is never contacted.
func TestNew_NoBaseURL_DoesNotInheritEnv(t *testing.T) {
	var hit atomic.Bool
	envSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)))
	}))
	defer envSrv.Close()
	t.Setenv("OPENAI_BASE_URL", envSrv.URL)

	c := openaicompat.New(openaicompat.WithProviderName("test"), openaicompat.WithAPIKey("k"))
	_, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err == nil {
		t.Error("Complete with no base URL returned nil error, want a loud failure")
	}
	if hit.Load() {
		t.Error("request reached the OPENAI_BASE_URL server — base URL was not pinned")
	}
}

// TestComplete_EmptyMessages_ReturnsError pins that a request with no messages
// fails before any network call rather than sending an empty conversation.
func TestComplete_EmptyMessages_ReturnsError(t *testing.T) {
	srv := sseServer(t, nil, sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`))
	defer srv.Close()
	c := newClient(srv.URL)
	if _, err := c.Complete(context.Background(), llm.Request{}); err == nil {
		t.Fatal("Complete with no messages returned nil error")
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

	c := newClient(srv.URL)
	ch, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "Greet me."}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	var text strings.Builder
	var sawDone bool
	for _, ev := range collect(t, ch) {
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
// produce exactly one [llm.EventToolCall] with the id, name, and reassembled JSON
// input. This is the most important thing to pin per ADR-0021's LLM cassette
// policy — the id is what correlates the later tool result.
func TestComplete_ToolUseStream_DecodesCall(t *testing.T) {
	srv := sseServer(t, nil,
		sse(`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"dice","arguments":""}}]}}]}`),
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"notation\""}}]}}]}`),
		sse(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"1d20\"}"}}]}}]}`),
		sse(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`),
		"data: [DONE]\n\n",
	)
	defer srv.Close()

	c := newClient(srv.URL)
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

	var calls []llm.ToolCall
	var stop string
	for _, ev := range collect(t, ch) {
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

// TestComplete_RequestShape_PinsBodyAndHeaders is the adapter↔OpenAI-compat API
// contract: the request must carry the BYOK key in an Authorization Bearer
// header, target /chat/completions, send stream=true, the system prompt as a
// "system"-role message (NOT lifted out, unlike Anthropic), the user text as a
// user message, and the tools array as function tools with tool_choice "auto".
func TestComplete_RequestShape_PinsBodyAndHeaders(t *testing.T) {
	var capture atomic.Value
	var seenAuth atomic.Value
	var seenPath atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		seenPath.Store(r.URL.Path)
		seenAuth.Store(r.Header.Get("Authorization"))
		body, _ := readBody(r)
		capture.Store(body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sse(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`)))
	}))
	defer srv.Close()

	c := openaicompat.New(
		openaicompat.WithProviderName("test"),
		openaicompat.WithAPIKey("expected-key"),
		openaicompat.WithBaseURL(srv.URL),
		openaicompat.WithModel("default-model"),
	)
	ch, err := c.Complete(context.Background(), llm.Request{
		Model:     "override-model",
		MaxTokens: 256,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Text: "You are Bart the innkeeper."},
			{Role: llm.RoleUser, Text: "Hello Bart."},
		},
		Tools: []llm.ToolDef{{Name: "dice", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	collect(t, ch)

	if got, _ := seenPath.Load().(string); got != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", got)
	}
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
	if body.Model != "override-model" {
		t.Errorf("model = %q, want override-model (per-call wins)", body.Model)
	}
	if body.MaxTokens != 256 {
		t.Errorf("max_tokens = %d, want 256", body.MaxTokens)
	}
	if !body.Stream {
		t.Error("stream = false, want true")
	}
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

// TestComplete_ToolChoice_SerializesModes pins the #398/#399 per-round knob on the
// OpenAI-compat wire: the zero value stays "auto" (byte-identical to pre-#398, so
// every existing turn is unchanged), "none"/"required" serialize as bare strings,
// and the pinned-Tool mode serializes as the named-function object. tool_choice is
// only present when tools are declared.
func TestComplete_ToolChoice_SerializesModes(t *testing.T) {
	reqWith := func(tc llm.ToolChoice) llm.Request {
		return llm.Request{
			Messages:   []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
			Tools:      []llm.ToolDef{{Name: "dice", InputSchema: json.RawMessage(`{"type":"object"}`)}},
			ToolChoice: tc,
		}
	}
	// scalar decodes tool_choice when it is a bare string ("auto"/"none"/"required").
	scalar := func(t *testing.T, raw []byte) string {
		t.Helper()
		var body struct {
			ToolChoice string `json:"tool_choice"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("tool_choice not a scalar string: %v\nbody: %s", err, raw)
		}
		return body.ToolChoice
	}

	if got := scalar(t, captureBody(t, reqWith(llm.ToolChoice{}))); got != "auto" {
		t.Errorf("zero ToolChoice → tool_choice = %q, want auto (unchanged wire)", got)
	}
	if got := scalar(t, captureBody(t, reqWith(llm.ToolChoice{Mode: llm.ToolChoiceNone}))); got != "none" {
		t.Errorf("None → tool_choice = %q, want none", got)
	}
	if got := scalar(t, captureBody(t, reqWith(llm.ToolChoice{Mode: llm.ToolChoiceRequired}))); got != "required" {
		t.Errorf("Required → tool_choice = %q, want required", got)
	}

	// Tool mode: the named-function object union.
	var namedBody struct {
		ToolChoice struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tool_choice"`
	}
	raw := captureBody(t, reqWith(llm.ToolChoice{Mode: llm.ToolChoiceTool, Tool: "dice"}))
	if err := json.Unmarshal(raw, &namedBody); err != nil {
		t.Fatalf("named tool_choice not an object: %v\nbody: %s", err, raw)
	}
	if namedBody.ToolChoice.Type != "function" || namedBody.ToolChoice.Function.Name != "dice" {
		t.Errorf("Tool mode → tool_choice = %+v, want {function, dice}", namedBody.ToolChoice)
	}

	// No tools declared: tool_choice must be absent even if a mode is set.
	noTools := captureBody(t, llm.Request{
		Messages:   []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
		ToolChoice: llm.ToolChoice{Mode: llm.ToolChoiceRequired},
	})
	if strings.Contains(string(noTools), "tool_choice") {
		t.Errorf("tool_choice present with no tools declared; got %s", noTools)
	}
}

// TestComplete_ModelSelection pins the configurable-model contract: with no
// per-call override the body carries the constructor default; a non-empty
// [llm.Request.Model] wins over it.
func TestComplete_ModelSelection(t *testing.T) {
	model := func(t *testing.T, reqModel string) string {
		t.Helper()
		raw := captureBody(t, llm.Request{
			Model:    reqModel,
			Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
		})
		var body struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("request body not JSON: %v", err)
		}
		return body.Model
	}
	if got := model(t, ""); got != "test-model" {
		t.Errorf("default model = %q, want test-model", got)
	}
	if got := model(t, "per-call"); got != "per-call" {
		t.Errorf("per-call model = %q, want per-call", got)
	}
}

// TestComplete_ToolResultRoundTrip_MapsToToolRoleMessages pins the ADR-0028 return
// path for the OpenAI-compat wire shape: a [llm.RoleTool] message must serialize
// as one tool-role message PER result, each keyed by its tool_call_id — OpenAI's
// shape, where parallel results are SEPARATE messages (in contrast to Anthropic's
// single message with multiple blocks). The id is the correlation key the live
// tool loop depends on.
func TestComplete_ToolResultRoundTrip_MapsToToolRoleMessages(t *testing.T) {
	raw := captureBody(t, llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleUser, Text: "roll a d20 and a d6"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
				{ID: "call_9", Name: "dice", Input: json.RawMessage(`{"notation":"1d20"}`)},
				{ID: "call_10", Name: "dice", Input: json.RawMessage(`{"notation":"1d6"}`)},
			}},
			{Role: llm.RoleTool, ToolResults: []llm.ToolResult{
				{CallID: "call_9", Content: "17"},
				{CallID: "call_10", Content: "4"},
			}},
		},
	})

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

// TestComplete_ReasoningEffort pins the optional reasoning_effort field (the
// ADR-0035 thinking cap the gemini preset rides on): set when configured,
// omitted otherwise.
func TestComplete_ReasoningEffort(t *testing.T) {
	read := func(raw []byte) (string, bool) {
		var body map[string]json.RawMessage
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("request body not JSON: %v", err)
		}
		v, ok := body["reasoning_effort"]
		return string(v), ok
	}

	raw := captureBody(t,
		llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}}},
		openaicompat.WithReasoningEffort("low"))
	if v, ok := read(raw); !ok || v != `"low"` {
		t.Errorf("reasoning_effort = %s (present=%v), want \"low\"", v, ok)
	}

	raw = captureBody(t, llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}}})
	if v, ok := read(raw); ok {
		t.Errorf("reasoning_effort = %s, want omitted when unset", v)
	}
}

// TestComplete_ExtraFields pins the non-standard-body passthrough the gemini
// preset uses for extra_body.google.thinking_config: a configured extra field
// rides verbatim at the top level of the request body.
func TestComplete_ExtraFields(t *testing.T) {
	raw := captureBody(t,
		llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}}},
		openaicompat.WithExtraFields(map[string]any{
			"extra_body": map[string]any{"google": map[string]any{"flag": 7}},
		}))
	var body struct {
		ExtraBody *struct {
			Google struct {
				Flag int `json:"flag"`
			} `json:"google"`
		} `json:"extra_body"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("request body not JSON: %v\nbody: %s", err, raw)
	}
	if body.ExtraBody == nil || body.ExtraBody.Google.Flag != 7 {
		t.Errorf("extra_body = %+v, want google.flag = 7", body.ExtraBody)
	}
}

// TestComplete_Non2xx_WrapsProviderAndStatus pins the error-surface shape: a
// non-2xx response yields an error naming the operation, the HTTP status, and the
// response body so the cause is diagnosable.
func TestComplete_Non2xx_WrapsProviderAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API Key","code":"invalid_api_key"}}`))
	}))
	defer srv.Close()

	c := newClient(srv.URL)
	_, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err == nil {
		t.Fatal("Complete with 401 returned nil error")
	}
	for _, must := range []string{"test.Complete", "401", "invalid_api_key"} {
		if !strings.Contains(err.Error(), must) {
			t.Errorf("error %q missing required substring %q", err, must)
		}
	}
}

// TestComplete_429_TypedHTTPError pins that the adapter converts the SDK's
// *openai.Error into an errors.As-able [*providererr.HTTPError] carrying the
// status code, so the retry helper classifies a 429 as retryable — the plumbing
// #124 relies on for the Groq LLM start-call (ADR-0044). The message still names
// the operation, status, and body for diagnosability.
func TestComplete_429_TypedHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limit","code":"rate_limit_exceeded"}}`))
	}))
	defer srv.Close()

	c := newClient(srv.URL)
	_, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	var he *providererr.HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("error %v is not a *providererr.HTTPError", err)
	}
	if he.StatusCode != 429 {
		t.Errorf("StatusCode = %d, want 429", he.StatusCode)
	}
	if !retry.Retryable(err) {
		t.Error("a 429 must be retryable")
	}
	for _, must := range []string{"test.Complete", "429", "rate_limit_exceeded"} {
		if !strings.Contains(err.Error(), must) {
			t.Errorf("error %q missing required substring %q", err, must)
		}
	}
}

// TestComplete_StartToolUseFailed_TypedToolSyntaxError pins #398 detection site
// two: a 400 whose body carries "code":"tool_use_failed" surfaces as a
// [*providererr.ToolSyntaxError] (NOT the generic [*providererr.HTTPError]), so the
// agenttool bridge routes it into the retry / tool-less-fallback path instead of
// treating it as a hard 4xx. A generic 400 with no tool_use_failed code stays a
// plain HTTPError — the regression guard that the new path does not swallow every
// bad request.
func TestComplete_StartToolUseFailed_TypedToolSyntaxError(t *testing.T) {
	t.Run("400 tool_use_failed → ToolSyntaxError", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Failed to call a function.","type":"invalid_request_error","code":"tool_use_failed","failed_generation":"<function=dice></function>"}}`))
		}))
		defer srv.Close()
		c := newClient(srv.URL)
		_, err := c.Complete(context.Background(), llm.Request{
			Messages: []llm.Message{{Role: llm.RoleUser, Text: "Roll a d20."}},
			Tools:    []llm.ToolDef{{Name: "dice", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		})
		var tse *providererr.ToolSyntaxError
		if !errors.As(err, &tse) {
			t.Fatalf("error %v is not a *providererr.ToolSyntaxError", err)
		}
		var he *providererr.HTTPError
		if errors.As(err, &he) {
			t.Errorf("a tool_use_failed 400 must NOT also be an HTTPError; got %v", he)
		}
		if retry.Retryable(err) {
			t.Error("a tool_use_failed error must not be retryable by the generic retry helper")
		}
		if !strings.Contains(tse.Error(), "tool_use_failed") {
			t.Errorf("ToolSyntaxError message %q does not preserve the provider body", tse.Error())
		}
	})

	t.Run("generic 400 stays HTTPError", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"missing model","type":"invalid_request_error","code":"invalid_request"}}`))
		}))
		defer srv.Close()
		c := newClient(srv.URL)
		_, err := c.Complete(context.Background(), llm.Request{
			Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
		})
		var he *providererr.HTTPError
		if !errors.As(err, &he) {
			t.Fatalf("generic 400 error %v is not a *providererr.HTTPError", err)
		}
		if he.StatusCode != 400 {
			t.Errorf("StatusCode = %d, want 400", he.StatusCode)
		}
		var tse *providererr.ToolSyntaxError
		if errors.As(err, &tse) {
			t.Errorf("a generic 400 must NOT be a ToolSyntaxError; got %v", tse)
		}
	})
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

	c := newClient(srv.URL)
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

// TestComplete_HostileChunkFlood_EmitsBudgetError pins the stream budget against
// the zero-content flood (issue #152): a hostile WithBaseURL gateway streaming
// choice-less / empty-delta chunks in a tight loop — never a finish_reason, never
// [DONE] — must terminate with a terminal [llm.EventError] in bounded time, not
// spin for as long as the caller's context lives. Every received chunk must count
// against the budget, not only decoded content bytes (which such chunks lack).
func TestComplete_HostileChunkFlood_EmitsBudgetError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Alternate the two hostile shapes: no choices at all, and a choice whose
		// delta decodes to zero content bytes. Loop until the client hangs up.
		frames := []byte(sse(`{"choices":[]}`) + sse(`{"choices":[{"delta":{}}]}`))
		for {
			if _, err := w.Write(frames); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // unblocks the streaming goroutine if the budget never trips

	c := newClient(srv.URL)
	ch, err := c.Complete(ctx, llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	deadline := time.After(15 * time.Second)
	var last llm.StreamEvent
	var n int
	for open := true; open; {
		select {
		case ev, ok := <-ch:
			if !ok {
				open = false
				break
			}
			last = ev
			n++
		case <-deadline:
			t.Fatal("stream did not terminate within 15s — chunk flood never tripped the budget")
		}
	}
	if last.Type != llm.EventError {
		t.Fatalf("last event = %+v (of %d), want EventError from the stream budget", last, n)
	}
	if !strings.Contains(last.Err, "exceeded") {
		t.Errorf("EventError %q does not name the exceeded budget", last.Err)
	}
}

// TestComplete_DoneThenFlood_EmitsBudgetError pins the budget below the SDK: a
// hostile gateway sends the [DONE] sentinel FIRST and then floods choice-less
// frames forever without closing. openai-go's Stream.Next sets done=true on the
// sentinel and silently drains every later event inside its own loop — it never
// returns to the adapter, so any guard in the adapter's chunk loop is bypassed.
// The raw-byte cap on the transport's response body must still terminate the
// stream with a terminal [llm.EventError] in bounded time.
func TestComplete_DoneThenFlood_EmitsBudgetError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
			return
		}
		frame := []byte(sse(`{"choices":[]}`))
		for {
			if _, err := w.Write(frame); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // unblocks the streaming goroutine if the budget never trips

	c := newClient(srv.URL)
	ch, err := c.Complete(ctx, llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	deadline := time.After(15 * time.Second)
	var last llm.StreamEvent
	for open := true; open; {
		select {
		case ev, ok := <-ch:
			if !ok {
				open = false
				break
			}
			last = ev
		case <-deadline:
			t.Fatal("stream did not terminate within 15s — post-[DONE] flood bypassed the budget")
		}
	}
	if last.Type != llm.EventError {
		t.Fatalf("last event = %+v, want EventError from the stream budget", last)
	}
	if !strings.Contains(last.Err, "exceeded") {
		t.Errorf("EventError %q does not name the exceeded budget", last.Err)
	}
}

// TestComplete_OversizedContentStream_EmitsBudgetError pins the other half of the
// stream budget: a stream whose decoded content exceeds the byte cap (here 17 MiB
// of content against the 16 MiB budget) terminates with a terminal
// [llm.EventError] and never an [llm.EventDone] — the pre-#152 guard, still live.
func TestComplete_OversizedContentStream_EmitsBudgetError(t *testing.T) {
	content := strings.Repeat("a", 1<<20) // 1 MiB of content per chunk
	frame := []byte(sse(`{"choices":[{"delta":{"content":"` + content + `"}}]}`))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for i := 0; i < 17; i++ { // 17 MiB decoded content > the 16 MiB budget
			if _, err := w.Write(frame); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	c := newClient(srv.URL)
	ch, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	events := collect(t, ch)
	last := events[len(events)-1]
	if last.Type != llm.EventError {
		t.Fatalf("last event type = %v, want EventError from the stream budget", last.Type)
	}
	if !strings.Contains(last.Err, "exceeded") {
		t.Errorf("EventError %q does not name the exceeded budget", last.Err)
	}
	for _, ev := range events {
		if ev.Type == llm.EventDone {
			t.Error("stream emitted EventDone despite blowing the budget")
		}
	}
}

// TestComplete_ContextCanceled_NoTerminalEvent pins the cancellation contract: a
// ctx cancelled mid-stream closes the channel with NEITHER an EventDone nor an
// EventError — the signal that the accumulated text is truncated, not complete
// (the agent loop treats a missing EventDone as a failed turn).
func TestComplete_ContextCanceled_NoTerminalEvent(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		_, _ = w.Write([]byte(sse(`{"choices":[{"delta":{"content":"Hel"}}]}`)))
		if fl != nil {
			fl.Flush()
		}
		<-release // hold the stream open so only the ctx cancel can close it
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	c := newClient(srv.URL)
	ch, err := c.Complete(ctx, llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "Greet me."}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	first, ok := <-ch
	if !ok || first.Type != llm.EventText {
		t.Fatalf("first event = %+v (ok=%v), want EventText", first, ok)
	}
	cancel()
	for ev := range ch {
		if ev.Type == llm.EventError {
			t.Errorf("got EventError %q on ctx cancel, want clean close", ev.Err)
		}
		if ev.Type == llm.EventDone {
			t.Error("got EventDone on ctx cancel, want clean close")
		}
	}
}

// TestComplete_InStreamToolUseFailed_ClassifiesToolSyntax pins #398 detection site
// one: Groq surfaces a malformed pseudo-XML tool call as an in-stream error frame
// carrying "code":"tool_use_failed". The adapter must terminate with an
// [llm.EventError] whose ErrClass is [llm.ErrClassToolSyntax] (never an
// [llm.EventDone]), so the agenttool bridge can retry the round rather than
// abandon the turn. A generic in-stream error frame stays [llm.ErrClassNone].
func TestComplete_InStreamToolUseFailed_ClassifiesToolSyntax(t *testing.T) {
	toolFail := sse(`{"error":{"message":"Failed to call a function. Please adjust your prompt.","type":"invalid_request_error","code":"tool_use_failed","failed_generation":"<function=dice{\"sides\":20}</function>"}}`)

	t.Run("tool_use_failed → ErrClassToolSyntax", func(t *testing.T) {
		srv := sseServer(t, nil, toolFail)
		defer srv.Close()
		c := newClient(srv.URL)
		ch, err := c.Complete(context.Background(), llm.Request{
			Messages: []llm.Message{{Role: llm.RoleUser, Text: "Roll a d20."}},
			Tools:    []llm.ToolDef{{Name: "dice", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		events := collect(t, ch)
		last := events[len(events)-1]
		if last.Type != llm.EventError {
			t.Fatalf("last event = %+v, want EventError", last)
		}
		if last.ErrClass != llm.ErrClassToolSyntax {
			t.Errorf("ErrClass = %q, want %q", last.ErrClass, llm.ErrClassToolSyntax)
		}
		for _, ev := range events {
			if ev.Type == llm.EventDone {
				t.Error("stream emitted EventDone despite the tool_use_failed error")
			}
		}
	})

	t.Run("generic in-stream error stays ErrClassNone", func(t *testing.T) {
		srv := sseServer(t, nil, sse(`{"error":{"message":"upstream boom","type":"server_error","code":"internal"}}`))
		defer srv.Close()
		c := newClient(srv.URL)
		ch, err := c.Complete(context.Background(), llm.Request{
			Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
			Tools:    []llm.ToolDef{{Name: "dice", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		events := collect(t, ch)
		last := events[len(events)-1]
		if last.Type != llm.EventError {
			t.Fatalf("last event = %+v, want EventError", last)
		}
		if last.ErrClass != llm.ErrClassNone {
			t.Errorf("ErrClass = %q, want none for a non-tool-syntax stream error", last.ErrClass)
		}
	})
}
