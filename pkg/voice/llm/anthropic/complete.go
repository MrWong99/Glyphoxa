package anthropic

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

// messagesBody mirrors the Anthropic POST /v1/messages request body for a
// streaming completion. System travels out-of-band per the API (a top-level
// field, not a message), so the adapter lifts the [llm.RoleSystem] message out
// of the conversation into this field.
type messagesBody struct {
	Model      string          `json:"model"`
	MaxTokens  int             `json:"max_tokens"`
	System     string          `json:"system,omitempty"`
	Messages   []wireMessage   `json:"messages"`
	Tools      []wireTool      `json:"tools,omitempty"`
	ToolChoice *wireToolChoice `json:"tool_choice,omitempty"`
	Stream     bool            `json:"stream"`
}

// wireMessage is one Anthropic message: a role plus a list of content blocks.
// Anthropic models tool results as user-role messages carrying tool_result
// blocks, so [llm.RoleTool] maps to role "user" here (see toWireMessages).
type wireMessage struct {
	Role    string         `json:"role"`
	Content []contentBlock `json:"content"`
}

// contentBlock is one Anthropic content block. The shape is a tagged union on
// Type; only the fields valid for a given Type are populated (the rest carry
// omitempty so the wire stays minimal and deterministic).
type contentBlock struct {
	Type string `json:"type"`

	// type=text
	Text string `json:"text,omitempty"`

	// type=tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// type=tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

// wireTool mirrors one entry of the Anthropic tools array.
type wireTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// wireToolChoice mirrors the Anthropic tool_choice object. The adapter sets
// {type: "auto"} whenever tools are present, letting the model decide.
type wireToolChoice struct {
	Type string `json:"type"`
}

// Complete implements [llm.Provider]. It posts a streaming Messages request and
// returns a channel of [llm.StreamEvent]s decoded from the SSE response: text
// deltas become [llm.EventText], completed tool-use blocks become
// [llm.EventToolCall], and the terminating message_delta/message_stop becomes
// [llm.EventDone] carrying the stop reason.
//
// The returned channel closes on stream end, ctx cancellation, or the first
// stream read/parse error. A non-nil error is returned only when the call
// cannot be started (missing key, bad request, non-2xx response); a mid-stream
// failure closes the channel early instead, matching the tts adapter.
func (c *Client) Complete(ctx context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("anthropic.Complete: missing API key (set %s or pass it to New)", APIKeyEnv)
	}

	system, messages := toWireMessages(req.Messages)
	if len(messages) == 0 {
		return nil, fmt.Errorf("anthropic.Complete: request has no user/assistant messages")
	}

	model := req.Model
	if model == "" {
		model = c.model
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}

	body := messagesBody{
		Model:     model,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  messages,
		Tools:     toWireTools(req.Tools),
		Stream:    true,
	}
	if len(body.Tools) > 0 {
		body.ToolChoice = &wireToolChoice{Type: "auto"}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("anthropic.Complete: marshal body: %w", err)
	}

	u := strings.TrimRight(c.baseURL, "/") + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("anthropic.Complete: build request: %w", err)
	}
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", apiVersion)
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "text/event-stream")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic.Complete: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, readErrorResponse(resp, "Complete")
	}

	ch := make(chan llm.StreamEvent)
	go streamEvents(ctx, resp.Body, ch)
	return ch, nil
}

// toWireMessages splits the [llm] conversation into the Anthropic system string
// and the user/assistant/tool message list. The first [llm.RoleSystem] message
// becomes the system field; subsequent system messages are folded into it
// (newline-joined) so a multi-part Persona survives. Tool-role messages map to
// user-role tool_result blocks per the Anthropic protocol (ADR-0028).
func toWireMessages(msgs []llm.Message) (system string, out []wireMessage) {
	var systemParts []string
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem:
			if m.Text != "" {
				systemParts = append(systemParts, m.Text)
			}
		case llm.RoleAssistant:
			out = append(out, wireMessage{Role: "assistant", Content: assistantBlocks(m)})
		case llm.RoleTool:
			out = append(out, wireMessage{Role: "user", Content: toolResultBlocks(m)})
		default: // RoleUser and any unknown role
			out = append(out, wireMessage{Role: "user", Content: []contentBlock{{Type: "text", Text: m.Text}}})
		}
	}
	return strings.Join(systemParts, "\n\n"), out
}

// assistantBlocks renders an assistant [llm.Message] as Anthropic content
// blocks: the spoken text first (if any), then one tool_use block per
// [llm.ToolCall] it requested.
func assistantBlocks(m llm.Message) []contentBlock {
	var blocks []contentBlock
	if m.Text != "" {
		blocks = append(blocks, contentBlock{Type: "text", Text: m.Text})
	}
	for _, tc := range m.ToolCalls {
		blocks = append(blocks, contentBlock{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Name,
			Input: tc.Input,
		})
	}
	return blocks
}

// toolResultBlocks renders a [llm.RoleTool] message as one Anthropic
// tool_result block per result, all carried on the single user-role message
// the API expects after a parallel-tool-call assistant turn (ADR-0028).
func toolResultBlocks(m llm.Message) []contentBlock {
	if len(m.ToolResults) == 0 {
		return nil
	}
	blocks := make([]contentBlock, len(m.ToolResults))
	for i, tr := range m.ToolResults {
		blocks[i] = contentBlock{
			Type:      "tool_result",
			ToolUseID: tr.CallID,
			Content:   tr.Content,
			IsError:   tr.IsError,
		}
	}
	return blocks
}

// toWireTools maps the [llm.ToolDef]s onto the Anthropic tools array. Returns
// nil for an empty input so the body omits the field entirely.
func toWireTools(defs []llm.ToolDef) []wireTool {
	if len(defs) == 0 {
		return nil
	}
	tools := make([]wireTool, len(defs))
	for i, d := range defs {
		tools[i] = wireTool{
			Name:        d.Name,
			Description: d.Description,
			InputSchema: d.InputSchema,
		}
	}
	return tools
}

// readErrorResponse reads up to 512 bytes of a non-2xx response body for
// diagnostic context and wraps it as an error naming the operation, matching
// the elevenlabs adapters' error shape.
func readErrorResponse(resp *http.Response, op string) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("anthropic.%s: HTTP %d %s: %s",
		op, resp.StatusCode, resp.Status, strings.TrimSpace(string(snippet)))
}

// streamEvents reads the Anthropic SSE response from r, decodes each event, and
// emits the corresponding [llm.StreamEvent] on ch. It closes ch on stream end
// (after an [llm.EventDone]), ctx cancellation, or the first read/parse error;
// r is closed before return so the connection is released to the pool.
//
// SSE framing: each event is a "data: <json>\n" line (the "event:" line is
// redundant with the JSON "type" field, so it is ignored). Decoding tracks
// per-index content blocks: a content_block_start for a tool_use block records
// its id/name; the input_json_delta lines accumulate the arguments string; the
// content_block_stop flushes a completed tool_use as one [llm.EventToolCall].
func streamEvents(ctx context.Context, r io.ReadCloser, ch chan<- llm.StreamEvent) {
	defer close(ch)
	defer r.Close()

	// Pending tool-use blocks keyed by their stream index, accumulating their
	// JSON arguments across input_json_delta lines until content_block_stop.
	type pendingTool struct {
		id    string
		name  string
		input strings.Builder
	}
	tools := map[int]*pendingTool{}

	send := func(ev llm.StreamEvent) bool {
		select {
		case <-ctx.Done():
			return false
		case ch <- ev:
			return true
		}
	}

	sc := bufio.NewScanner(r)
	// SSE lines are small, but a tool_use input or a long text delta can exceed
	// bufio's default 64KiB token cap; raise the ceiling so a big block does
	// not truncate the stream.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return
		}
		line := sc.Text()
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue // event:, id:, blank separator, or retry: lines
		}
		data = strings.TrimSpace(data)
		if data == "" {
			continue
		}

		var ev sseEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return // a malformed frame ends the stream early, like the tts read path
		}

		switch ev.Type {
		case "content_block_start":
			if ev.ContentBlock.Type == "tool_use" {
				tools[ev.Index] = &pendingTool{id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
			}
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					if !send(llm.StreamEvent{Type: llm.EventText, Text: ev.Delta.Text}) {
						return
					}
				}
			case "input_json_delta":
				if pt := tools[ev.Index]; pt != nil {
					pt.input.WriteString(ev.Delta.PartialJSON)
				}
			}
		case "content_block_stop":
			if pt := tools[ev.Index]; pt != nil {
				delete(tools, ev.Index)
				input := pt.input.String()
				if input == "" {
					input = "{}" // a no-argument tool streams no input_json_delta
				}
				if !send(llm.StreamEvent{
					Type: llm.EventToolCall,
					ToolCall: llm.ToolCall{
						ID:    pt.id,
						Name:  pt.name,
						Input: json.RawMessage(input),
					},
				}) {
					return
				}
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				if !send(llm.StreamEvent{Type: llm.EventDone, StopReason: ev.Delta.StopReason}) {
					return
				}
			}
		case "error":
			return // a mid-stream error event closes the channel early
		}
	}
}

// sseEvent is the decoded JSON payload of one SSE "data:" line. It is a
// superset across every Anthropic event type; only the fields relevant to the
// event's Type are populated.
type sseEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
}
