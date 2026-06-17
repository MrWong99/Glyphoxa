package groq

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

const (
	// maxLineBytes caps one SSE line (one chunk). A whole tool-argument or
	// content delta rides on a single data: line, so this must comfortably
	// exceed any legitimate delta.
	maxLineBytes = 1024 * 1024

	// maxStreamBytes caps the whole response stream. A voice turn's completion
	// is a few KiB; 16 MiB is far past any legitimate reply and exists so a
	// misbehaving or hostile endpoint (WithBaseURL gateways) cannot stream
	// unboundedly into memory.
	maxStreamBytes = 16 * 1024 * 1024
)

// chatBody mirrors the OpenAI-compatible POST /chat/completions request body for
// a streaming completion. The system prompt rides as a "system"-role message
// (unlike Anthropic's top-level field), so the adapter keeps it in the message
// list rather than lifting it out.
type chatBody struct {
	Model      string        `json:"model"`
	MaxTokens  int           `json:"max_tokens,omitempty"`
	Messages   []wireMessage `json:"messages"`
	Tools      []wireTool    `json:"tools,omitempty"`
	ToolChoice string        `json:"tool_choice,omitempty"`
	Stream     bool          `json:"stream"`
}

// wireMessage is one OpenAI-compatible chat message. Assistant turns that
// requested tools carry ToolCalls; a tool-role message carries the result of
// one call, correlated to it by ToolCallID. Content is omitted (not sent as
// "") on tool-call assistant turns so the wire stays minimal.
type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

// wireToolCall mirrors one entry of an assistant message's tool_calls array.
type wireToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function wireToolCallFunc `json:"function"`
}

// wireToolCallFunc is the function payload of a tool call: a name and its
// arguments rendered as a JSON string (OpenAI carries the arguments as a string,
// not an object).
type wireToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// wireTool mirrors one entry of the OpenAI-compatible tools array. Every tool is
// a function tool; the schema lives under function.parameters.
type wireTool struct {
	Type     string       `json:"type"`
	Function wireToolDecl `json:"function"`
}

// wireToolDecl is the function declaration inside a wireTool.
type wireToolDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// Complete implements [llm.Provider]. It posts a streaming chat/completions
// request to Groq's OpenAI-compatibility endpoint and returns a channel of
// [llm.StreamEvent]s decoded from the SSE response: content deltas become
// [llm.EventText], completed tool_call deltas become [llm.EventToolCall], and
// the terminating chunk's finish_reason becomes [llm.EventDone].
//
// The returned channel closes on stream end, ctx cancellation, or the first
// stream read/parse error. A non-nil error is returned only when the call
// cannot be started (missing key, bad request, non-2xx response); a mid-stream
// failure closes the channel early instead, matching the gemini and anthropic
// adapters.
func (c *Client) Complete(ctx context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("groq.Complete: missing API key (set %s or pass it to New)", APIKeyEnv)
	}

	messages := toWireMessages(req.Messages)
	if len(messages) == 0 {
		return nil, fmt.Errorf("groq.Complete: request has no messages")
	}

	model := req.Model
	if model == "" {
		model = c.model
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}

	body := chatBody{
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  messages,
		Tools:     toWireTools(req.Tools),
		Stream:    true,
	}
	if len(body.Tools) > 0 {
		body.ToolChoice = "auto"
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("groq.Complete: marshal body: %w", err)
	}

	u := strings.TrimRight(c.baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("groq.Complete: build request: %w", err)
	}
	httpReq.Header.Set("authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "text/event-stream")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("groq.Complete: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, readErrorResponse(resp, "Complete")
	}

	ch := make(chan llm.StreamEvent)
	go streamEvents(ctx, resp.Body, ch)
	return ch, nil
}

// toWireMessages maps the [llm] conversation onto OpenAI-compatible chat
// messages. Unlike Anthropic, the system prompt stays in the list as a
// "system"-role message. An assistant turn that requested tools renders its
// [llm.ToolCall]s as the tool_calls array (arguments as a JSON string). Each
// [llm.RoleTool] message expands to one tool-role message PER result, correlated
// by tool_call_id — the OpenAI shape, where parallel results are separate
// messages rather than blocks on one message (ADR-0028).
func toWireMessages(msgs []llm.Message) []wireMessage {
	var out []wireMessage
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem:
			out = append(out, wireMessage{Role: "system", Content: m.Text})
		case llm.RoleAssistant:
			out = append(out, assistantMessage(m))
		case llm.RoleTool:
			out = append(out, toolResultMessages(m)...)
		default: // RoleUser and any unknown role
			out = append(out, wireMessage{Role: "user", Content: m.Text})
		}
	}
	return out
}

// assistantMessage renders an assistant [llm.Message]: its spoken text (if any)
// as content, plus one tool_call per [llm.ToolCall] it requested. The arguments
// travel as a JSON string per the OpenAI wire shape.
func assistantMessage(m llm.Message) wireMessage {
	wm := wireMessage{Role: "assistant", Content: m.Text}
	for _, tc := range m.ToolCalls {
		args := string(tc.Input)
		if args == "" {
			args = "{}"
		}
		wm.ToolCalls = append(wm.ToolCalls, wireToolCall{
			ID:   tc.ID,
			Type: "function",
			Function: wireToolCallFunc{
				Name:      tc.Name,
				Arguments: args,
			},
		})
	}
	return wm
}

// toolResultMessages expands a [llm.RoleTool] message into one tool-role message
// per result. OpenAI carries each parallel tool result on its own message keyed
// by tool_call_id, in contrast to Anthropic's single message with multiple
// blocks.
func toolResultMessages(m llm.Message) []wireMessage {
	out := make([]wireMessage, 0, len(m.ToolResults))
	for _, tr := range m.ToolResults {
		out = append(out, wireMessage{
			Role:       "tool",
			ToolCallID: tr.CallID,
			Content:    tr.Content,
		})
	}
	return out
}

// toWireTools maps the [llm.ToolDef]s onto the OpenAI-compatible tools array.
// Returns nil for an empty input so the body omits the field entirely.
func toWireTools(defs []llm.ToolDef) []wireTool {
	if len(defs) == 0 {
		return nil
	}
	tools := make([]wireTool, len(defs))
	for i, d := range defs {
		tools[i] = wireTool{
			Type: "function",
			Function: wireToolDecl{
				Name:        d.Name,
				Description: d.Description,
				Parameters:  d.InputSchema,
			},
		}
	}
	return tools
}

// readErrorResponse reads up to 512 bytes of a non-2xx response body for
// diagnostic context and wraps it as an error naming the operation, matching the
// gemini and elevenlabs adapters' error shape.
func readErrorResponse(resp *http.Response, op string) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("groq.%s: HTTP %d %s: %s",
		op, resp.StatusCode, resp.Status, strings.TrimSpace(string(snippet)))
}

// streamEvents reads the OpenAI-compatible SSE response from r, decodes each
// chunk, and emits the corresponding [llm.StreamEvent] on ch. It closes ch on
// stream end (after an [llm.EventDone]) or ctx cancellation; any read/parse
// failure or a response exceeding maxStreamBytes emits a terminal
// [llm.EventError] first so consumers never mistake a truncated stream for a
// complete reply. r is closed before return so the connection is released.
//
// SSE framing: each event is a "data: <json>\n" line. A terminal "data: [DONE]"
// sentinel marks the end. Tool calls stream incrementally: the first delta for a
// given tool_calls index carries the id and function.name; later deltas with the
// same index append function.arguments fragments. A completed call is flushed as
// one [llm.EventToolCall] when finish_reason arrives (OpenAI does not emit a
// per-call stop, so the finish chunk closes out all pending calls).
func streamEvents(ctx context.Context, r io.ReadCloser, ch chan<- llm.StreamEvent) {
	defer close(ch)
	defer r.Close()

	// Pending tool calls keyed by their stream index, accumulating arguments
	// across delta chunks until finish_reason flushes them. Order is preserved so
	// parallel calls flush in the index order the model emitted them.
	type pendingTool struct {
		id   string
		name string
		args strings.Builder
	}
	tools := map[int]*pendingTool{}
	var order []int

	send := func(ev llm.StreamEvent) bool {
		select {
		case <-ctx.Done():
			return false
		case ch <- ev:
			return true
		}
	}

	// flushTools emits an EventToolCall for every accumulated call, in arrival
	// order. Returns false if the context was cancelled mid-flush.
	flushTools := func() bool {
		for _, idx := range order {
			pt := tools[idx]
			if pt == nil {
				continue
			}
			args := pt.args.String()
			if args == "" {
				args = "{}" // a no-argument tool streams no argument fragments
			}
			if !send(llm.StreamEvent{
				Type: llm.EventToolCall,
				ToolCall: llm.ToolCall{
					ID:    pt.id,
					Name:  pt.name,
					Input: json.RawMessage(args),
				},
			}) {
				return false
			}
		}
		return true
	}

	fail := func(msg string) {
		send(llm.StreamEvent{Type: llm.EventError, Err: msg})
	}

	sc := bufio.NewScanner(r)
	// SSE lines are small, but a long content delta or tool-argument chunk can
	// exceed bufio's default 64KiB token cap; raise the ceiling so a big chunk
	// does not truncate the stream.
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	// total bounds the whole response body so a misbehaving (or hostile)
	// endpoint cannot grow memory without limit: each line is capped by the
	// scanner, this caps the number of lines.
	var total int

	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return
		}
		line := sc.Text()
		if total += len(line); total > maxStreamBytes {
			fail(fmt.Sprintf("groq: response stream exceeded %d bytes", maxStreamBytes))
			return
		}
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue // event:, id:, blank separator, or retry: lines
		}
		data = strings.TrimSpace(data)
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			return // terminal sentinel; finish_reason already emitted EventDone
		}

		var chunk sseChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			fail("groq: malformed SSE frame: " + err.Error())
			return
		}
		if len(chunk.Choices) == 0 {
			continue // e.g. a usage-only trailing chunk
		}
		choice := chunk.Choices[0]

		if choice.Delta.Content != "" {
			if !send(llm.StreamEvent{Type: llm.EventText, Text: choice.Delta.Content}) {
				return
			}
		}

		for _, tc := range choice.Delta.ToolCalls {
			pt := tools[tc.Index]
			if pt == nil {
				pt = &pendingTool{}
				tools[tc.Index] = pt
				order = append(order, tc.Index)
			}
			if tc.ID != "" {
				pt.id = tc.ID
			}
			if tc.Function.Name != "" {
				pt.name = tc.Function.Name
			}
			pt.args.WriteString(tc.Function.Arguments)
		}

		if choice.FinishReason != "" {
			if !flushTools() {
				return
			}
			if !send(llm.StreamEvent{Type: llm.EventDone, StopReason: choice.FinishReason}) {
				return
			}
		}
	}
	if err := sc.Err(); err != nil {
		fail("groq: read stream: " + err.Error())
	}
}

// sseChunk is the decoded JSON payload of one OpenAI-compatible SSE "data:"
// chunk. Only the fields the adapter consumes are modelled.
type sseChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}
