package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
	"github.com/MrWong99/Glyphoxa/pkg/voice/retry"
)

// Compile-time assertion: [anthropic.Client] satisfies [llm.Provider], the only
// contract the Agent loop depends on.
var _ llm.Provider = (*anthropic.Client)(nil)

// sseServer returns an httptest server that replies to /v1/messages with the
// given pre-built SSE event lines, captures the request body for assertions,
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
// property shared with the elevenlabs adapters: New must not panic without an
// API key (cassette-replay test binaries link this package unconditionally);
// the missing-key error surfaces at the first request.
func TestNew_NoKey_NoEnv_CompleteReturnsMissingKeyError(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	c := anthropic.New("")
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

// TestComplete_TextStream_AccumulatesDeltas pins the smallest end-to-end loop:
// a text-only completion streams text_delta events that decode to ordered
// [llm.EventText] values, terminated by an [llm.EventDone] carrying the stop
// reason from message_delta.
func TestComplete_TextStream_AccumulatesDeltas(t *testing.T) {
	srv := sseServer(t, nil,
		sse(`{"type":"message_start","message":{"id":"msg_1"}}`),
		sse(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		sse(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`),
		sse(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":", traveler."}}`),
		sse(`{"type":"content_block_stop","index":0}`),
		sse(`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`),
		sse(`{"type":"message_stop"}`),
	)
	defer srv.Close()

	c := anthropic.New("k", anthropic.WithBaseURL(srv.URL))
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
			if ev.StopReason != "end_turn" {
				t.Errorf("stop reason = %q, want end_turn", ev.StopReason)
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

// TestComplete_Usage_EmitsOneEventUsageBeforeDone pins the #127 usage decode
// (ADR-0045): input_tokens is stashed from message_start, output_tokens read from
// message_delta, and exactly one EventUsage carrying both is emitted BEFORE the
// terminating EventDone.
func TestComplete_Usage_EmitsOneEventUsageBeforeDone(t *testing.T) {
	srv := sseServer(t, nil,
		sse(`{"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":25,"output_tokens":1}}}`),
		sse(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		sse(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`),
		sse(`{"type":"content_block_stop","index":0}`),
		sse(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":40}}`),
		sse(`{"type":"message_stop"}`),
	)
	defer srv.Close()

	c := anthropic.New("k", anthropic.WithBaseURL(srv.URL))
	ch, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "Greet me."}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	events := collect(t, ch)
	var usageIdx, doneIdx = -1, -1
	var usageCount int
	for i, ev := range events {
		switch ev.Type {
		case llm.EventUsage:
			usageCount++
			usageIdx = i
			if ev.Usage.InputTokens != 25 || ev.Usage.OutputTokens != 40 {
				t.Errorf("usage = %+v, want {InputTokens:25, OutputTokens:40}", ev.Usage)
			}
		case llm.EventDone:
			doneIdx = i
		}
	}
	if usageCount != 1 {
		t.Fatalf("EventUsage count = %d, want exactly 1", usageCount)
	}
	if usageIdx > doneIdx {
		t.Errorf("EventUsage index %d is after EventDone index %d; want usage before done", usageIdx, doneIdx)
	}
}

// TestComplete_NoUsage_EmitsNoEventUsage pins that a stream without usage fields
// (an old cassette, or a gateway that omits them) emits NO EventUsage — the
// consumer then estimates rather than recording a spurious zero.
func TestComplete_NoUsage_EmitsNoEventUsage(t *testing.T) {
	srv := sseServer(t, nil,
		sse(`{"type":"message_start","message":{"id":"msg_1"}}`),
		sse(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`),
		sse(`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`),
		sse(`{"type":"message_stop"}`),
	)
	defer srv.Close()

	c := anthropic.New("k", anthropic.WithBaseURL(srv.URL))
	ch, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "Greet me."}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	for _, ev := range collect(t, ch) {
		if ev.Type == llm.EventUsage {
			t.Errorf("emitted an EventUsage for a usage-free stream: %+v", ev.Usage)
		}
	}
}

// TestComplete_ToolUseStream_DecodesCall pins the tool-use decode (the ADR-0028
// seam): a content_block_start naming the tool, a run of input_json_delta lines
// accumulating the arguments, and a content_block_stop produce exactly one
// [llm.EventToolCall] with the id, name, and reassembled JSON input. This is
// the most important thing to pin per ADR-0021's LLM cassette policy.
func TestComplete_ToolUseStream_DecodesCall(t *testing.T) {
	srv := sseServer(t, nil,
		sse(`{"type":"message_start","message":{"id":"msg_1"}}`),
		sse(`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_9","name":"dice"}}`),
		sse(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"notation\""}}`),
		sse(`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":":\"1d20\"}"}}`),
		sse(`{"type":"content_block_stop","index":0}`),
		sse(`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`),
		sse(`{"type":"message_stop"}`),
	)
	defer srv.Close()

	c := anthropic.New("k", anthropic.WithBaseURL(srv.URL))
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
	if tc.ID != "toolu_9" || tc.Name != "dice" {
		t.Errorf("tool call id/name = %q/%q, want toolu_9/dice", tc.ID, tc.Name)
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
	if stop != "tool_use" {
		t.Errorf("stop reason = %q, want tool_use", stop)
	}
}

// TestComplete_RequestShape_PinsBodyAndHeaders is the adapter↔Anthropic API
// contract: the request must carry the BYOK key in x-api-key and the version
// header, stream=true, the system prompt lifted out of the message list into
// the top-level system field, the user text as a user-role text block, and the
// tools array with tool_choice auto. Pinning this catches accidental drift on
// either side.
func TestComplete_RequestShape_PinsBodyAndHeaders(t *testing.T) {
	var capture atomic.Value
	var seenKey, seenVersion atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %q, want /v1/messages", r.URL.Path)
		}
		seenKey.Store(r.Header.Get("x-api-key"))
		seenVersion.Store(r.Header.Get("anthropic-version"))
		body, _ := readBody(r)
		capture.Store(body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sse(`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`)))
	}))
	defer srv.Close()

	c := anthropic.New("expected-key", anthropic.WithBaseURL(srv.URL), anthropic.WithModel("claude-test"))
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

	if got, _ := seenKey.Load().(string); got != "expected-key" {
		t.Errorf("x-api-key = %q, want expected-key", got)
	}
	if got, _ := seenVersion.Load().(string); got == "" {
		t.Error("anthropic-version header is empty")
	}

	raw, _ := capture.Load().([]byte)
	var body struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		System    string `json:"system"`
		Stream    bool   `json:"stream"`
		Messages  []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		ToolChoice *struct {
			Type string `json:"type"`
		} `json:"tool_choice"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("request body not JSON: %v\nbody: %s", err, raw)
	}
	if body.Model != "claude-test" {
		t.Errorf("model = %q, want claude-test", body.Model)
	}
	if body.MaxTokens != 256 {
		t.Errorf("max_tokens = %d, want 256", body.MaxTokens)
	}
	if !body.Stream {
		t.Error("stream = false, want true")
	}
	if body.System != "You are Bart the innkeeper." {
		t.Errorf("system = %q, want the Persona text (must be lifted out of messages)", body.System)
	}
	// System must NOT appear as a message; exactly one user message remains.
	if len(body.Messages) != 1 || body.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v, want exactly one user message", body.Messages)
	}
	if len(body.Messages[0].Content) != 1 || body.Messages[0].Content[0].Type != "text" || body.Messages[0].Content[0].Text != "Hello Bart." {
		t.Errorf("user content = %+v, want one text block 'Hello Bart.'", body.Messages[0].Content)
	}
	if len(body.Tools) != 1 || body.Tools[0].Name != "dice" {
		t.Errorf("tools = %+v, want one tool named dice", body.Tools)
	}
	if body.ToolChoice == nil || body.ToolChoice.Type != "auto" {
		t.Errorf("tool_choice = %+v, want {auto} when tools present", body.ToolChoice)
	}
}

// TestComplete_ToolChoice_MapsModesToWire pins the #398/#399 per-round knob on the
// Anthropic wire: the zero value stays {type:auto} (byte-identical to pre-#398), the
// tool-less fallback maps to {type:none}, Required maps to Anthropic's {type:any},
// and the pinned-Tool mode maps to {type:tool,name:X}. tool_choice is only present
// when tools are declared.
func TestComplete_ToolChoice_MapsModesToWire(t *testing.T) {
	capttc := func(t *testing.T, tc llm.ToolChoice) (typ, name string, present bool) {
		t.Helper()
		var capture atomic.Value
		srv := sseServer(t, &capture, sse(`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`))
		defer srv.Close()
		c := anthropic.New("k", anthropic.WithBaseURL(srv.URL), anthropic.WithModel("claude-test"))
		ch, err := c.Complete(context.Background(), llm.Request{
			MaxTokens:  128,
			Messages:   []llm.Message{{Role: llm.RoleUser, Text: "Roll a d20."}},
			Tools:      []llm.ToolDef{{Name: "dice", InputSchema: json.RawMessage(`{"type":"object"}`)}},
			ToolChoice: tc,
		})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		collect(t, ch)
		raw, _ := capture.Load().([]byte)
		var body struct {
			ToolChoice *struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"tool_choice"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("request body not JSON: %v\nbody: %s", err, raw)
		}
		if body.ToolChoice == nil {
			return "", "", false
		}
		return body.ToolChoice.Type, body.ToolChoice.Name, true
	}

	if typ, _, ok := capttc(t, llm.ToolChoice{}); !ok || typ != "auto" {
		t.Errorf("zero ToolChoice → tool_choice type = %q (present=%v), want auto", typ, ok)
	}
	if typ, _, ok := capttc(t, llm.ToolChoice{Mode: llm.ToolChoiceNone}); !ok || typ != "none" {
		t.Errorf("None → tool_choice type = %q (present=%v), want none", typ, ok)
	}
	if typ, _, ok := capttc(t, llm.ToolChoice{Mode: llm.ToolChoiceRequired}); !ok || typ != "any" {
		t.Errorf("Required → tool_choice type = %q (present=%v), want any (Anthropic's required)", typ, ok)
	}
	if typ, name, ok := capttc(t, llm.ToolChoice{Mode: llm.ToolChoiceTool, Tool: "dice"}); !ok || typ != "tool" || name != "dice" {
		t.Errorf("Tool → tool_choice = {%q,%q} (present=%v), want {tool,dice}", typ, name, ok)
	}

	// No tools declared: tool_choice absent even with a mode set.
	var capture atomic.Value
	srv := sseServer(t, &capture, sse(`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`))
	defer srv.Close()
	c := anthropic.New("k", anthropic.WithBaseURL(srv.URL), anthropic.WithModel("claude-test"))
	ch, err := c.Complete(context.Background(), llm.Request{
		MaxTokens:  128,
		Messages:   []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
		ToolChoice: llm.ToolChoice{Mode: llm.ToolChoiceRequired},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	collect(t, ch)
	raw, _ := capture.Load().([]byte)
	if strings.Contains(string(raw), "tool_choice") {
		t.Errorf("tool_choice present with no tools declared; got %s", raw)
	}
}

// TestComplete_ToolResultRoundTrip_MapsToUserToolResultBlocks pins the ADR-0028
// return path: a [llm.RoleTool] message (the results the tool-use loop appends
// after executing an assistant turn's calls) must serialize as one user-role
// message carrying a tool_result block PER result, each with the matching
// tool_use_id — Anthropic's protocol shape for parallel tool calls. The slice
// (not a single result) is the seam agreed with tool-framework so a multi-call
// turn feeds all results back in the one following message the API expects.
func TestComplete_ToolResultRoundTrip_MapsToUserToolResultBlocks(t *testing.T) {
	var capture atomic.Value
	srv := sseServer(t, &capture,
		sse(`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`),
	)
	defer srv.Close()

	c := anthropic.New("k", anthropic.WithBaseURL(srv.URL))
	ch, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleUser, Text: "roll a d20 and a d6"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{
				{ID: "toolu_9", Name: "dice", Input: json.RawMessage(`{"notation":"1d20"}`)},
				{ID: "toolu_10", Name: "dice", Input: json.RawMessage(`{"notation":"1d6"}`)},
			}},
			// Two parallel calls → both results in ONE tool-role message.
			{Role: llm.RoleTool, ToolResults: []llm.ToolResult{
				{CallID: "toolu_9", Content: "17"},
				{CallID: "toolu_10", Content: "4"},
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
			Role    string `json:"role"`
			Content []struct {
				Type      string `json:"type"`
				ToolUseID string `json:"tool_use_id"`
				Content   string `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("request body not JSON: %v", err)
	}
	if len(body.Messages) != 3 {
		t.Fatalf("got %d messages, want 3 (user, assistant tool_use, user tool_result)", len(body.Messages))
	}
	last := body.Messages[2]
	if last.Role != "user" {
		t.Errorf("tool-result message role = %q, want user (Anthropic carries tool_result on a user turn)", last.Role)
	}
	// Both parallel results must ride the single tool-role message as two blocks.
	if len(last.Content) != 2 {
		t.Fatalf("tool-result content = %+v, want two tool_result blocks in one message", last.Content)
	}
	got := map[string]string{}
	for _, b := range last.Content {
		if b.Type != "tool_result" {
			t.Errorf("block type = %q, want tool_result", b.Type)
		}
		got[b.ToolUseID] = b.Content
	}
	if got["toolu_9"] != "17" || got["toolu_10"] != "4" {
		t.Errorf("tool_result blocks = %v, want {toolu_9:17, toolu_10:4}", got)
	}
}

// TestComplete_Non2xx_WrapsOpAndStatus pins the error-surface shape shared with
// the elevenlabs adapters: a non-2xx response yields an error naming the
// operation and the HTTP status with a body snippet.
func TestComplete_Non2xx_WrapsOpAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
	}))
	defer srv.Close()

	c := anthropic.New("bad", anthropic.WithBaseURL(srv.URL))
	_, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "hi"}},
	})
	if err == nil {
		t.Fatal("Complete with 401 returned nil error")
	}
	for _, must := range []string{"Complete", "401", "authentication_error"} {
		if !strings.Contains(err.Error(), must) {
			t.Errorf("error %q missing required substring %q", err, must)
		}
	}
}

// TestComplete_429_TypedHTTPError pins that a non-2xx start response surfaces as
// an errors.As-able [*providererr.HTTPError] the retry helper classifies (a 429
// is retryable), with the error text byte-identical to the adapter's pre-typed
// readErrorResponse literal (#124, ADR-0044).
func TestComplete_429_TypedHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("slow down"))
	}))
	defer srv.Close()

	c := anthropic.New("k", anthropic.WithBaseURL(srv.URL))
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
	const want = "anthropic.Complete: HTTP 429 429 Too Many Requests: slow down"
	if err.Error() != want {
		t.Errorf("error text = %q, want %q (byte-identical)", err.Error(), want)
	}
}

// sse formats one JSON payload as an Anthropic SSE "data:" frame (the "event:"
// line is redundant with the JSON type field and the adapter ignores it).
func sse(data string) string {
	return "event: x\ndata: " + data + "\n\n"
}

// TestComplete_MalformedFrame_EmitsEventError pins the truncation contract: a
// frame that fails to decode must surface as a terminal [llm.EventError], not a
// silent channel close — consumers would otherwise speak the partial text as a
// complete reply (and `-tags=record` would bake it into a cassette).
func TestComplete_MalformedFrame_EmitsEventError(t *testing.T) {
	srv := sseServer(t, nil,
		sse(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Half a sen"}}`),
		sse(`{not json`),
	)
	defer srv.Close()

	c := anthropic.New("k", anthropic.WithBaseURL(srv.URL))
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

// TestComplete_ServerErrorEvent_EmitsEventError pins that an in-band Anthropic
// error frame terminates the stream with an [llm.EventError] (not a silent
// close), carrying the server's payload for diagnosis.
func TestComplete_ServerErrorEvent_EmitsEventError(t *testing.T) {
	srv := sseServer(t, nil,
		sse(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`),
	)
	defer srv.Close()

	c := anthropic.New("k", anthropic.WithBaseURL(srv.URL))
	ch, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "Greet me."}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	events := collect(t, ch)
	if len(events) != 1 || events[0].Type != llm.EventError {
		t.Fatalf("events = %+v, want exactly one EventError", events)
	}
	if !strings.Contains(events[0].Err, "overloaded_error") {
		t.Errorf("EventError %q does not carry the server payload", events[0].Err)
	}
}
