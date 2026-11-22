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
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
)

const (
	// maxLineBytes caps one SSE line (one frame). Anthropic carries an entire
	// input_json_delta on a single data: line, so this must comfortably exceed
	// any legitimate tool-argument delta.
	maxLineBytes = 1024 * 1024

	// maxStreamBytes caps the whole response stream. A voice turn's completion
	// is a few KiB; 16 MiB is far past any legitimate reply and exists so a
	// misbehaving or hostile endpoint (WithBaseURL gateways) cannot stream
	// unboundedly into memory.
	maxStreamBytes = 16 * 1024 * 1024
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

	// Thinking and OutputConfig are the per-model reasoning fields (#702). Both
	// are omitted unless the client's thinking policy names the model, and they
	// sit last so a request without them is byte-identical to the pre-#702 body.
	Thinking     *wireThinking     `json:"thinking,omitempty"`
	OutputConfig *wireOutputConfig `json:"output_config,omitempty"`
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

	// raw, when set, is the whole block as it goes on the wire — a replayed
	// thinking block, which must return exactly as the API sent it (#702).
	raw json.RawMessage
}

// MarshalJSON writes a replayed block verbatim and every other block as its
// tagged fields (the same bytes the plain struct encoding produced).
func (b contentBlock) MarshalJSON() ([]byte, error) {
	if b.raw != nil {
		return b.raw, nil
	}
	type plain contentBlock
	return json.Marshal(plain(b))
}

// wireTool mirrors one entry of the Anthropic tools array.
type wireTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// wireToolChoice mirrors the Anthropic tool_choice object. Type is the choice
// discriminator ("auto"|"none"|"any"|"tool"); Name is set only for the pinned-tool
// choice ({type:"tool",name:X}), so it carries omitempty (#398/#399).
type wireToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
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

	system, messages, replayed := toWireMessages(req.Messages, c.thoughts)
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
		body.ToolChoice = wireToolChoiceFor(req.ToolChoice)
	}
	body.Thinking, body.OutputConfig = c.thinking(model).wireFields()

	resp, err := c.post(ctx, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusBadRequest && len(replayed) > 0 {
		// The API binds a thinking block to the conversation that produced it and
		// answers 400 when one is replayed after anything in front of it changed.
		// The adapter cannot see every edit its callers make (a regenerated turn
		// reuses tool-call ids), so it forgets the refused blocks and retries once
		// without them — the documented recovery; the model simply thinks again.
		herr := readErrorResponse(resp, "Complete")
		resp.Body.Close()
		if !strings.Contains(herr.Error(), "thinking") {
			return nil, herr
		}
		c.thoughts.forget(replayed)
		_, body.Messages, _ = toWireMessages(req.Messages, nil)
		if resp, err = c.post(ctx, body); err != nil {
			return nil, err
		}
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, readErrorResponse(resp, "Complete")
	}

	ch := make(chan llm.StreamEvent)
	go streamEvents(ctx, resp.Body, ch, c.thoughts)
	return ch, nil
}

// post sends one Messages request for body and returns the raw response; the
// caller owns the status check and the body.
func (c *Client) post(ctx context.Context, body messagesBody) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("anthropic.Complete: marshal body: %w", err)
	}

	u := strings.TrimRight(c.baseURL, "/") + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("anthropic.Complete: build request: %w", err)
	}
	setAuth(httpReq.Header, c.apiKey)
	httpReq.Header.Set("anthropic-version", apiVersion)
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "text/event-stream")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic.Complete: %w", err)
	}
	return resp, nil
}

// oauthTokenPrefix marks an Anthropic OAuth access token (the `ant` CLI login),
// as opposed to an API key ("sk-ant-api…").
const oauthTokenPrefix = "sk-ant-oat"

// setAuth sets the credential header for key. An API key travels as x-api-key;
// an OAuth access token is only accepted as "Authorization: Bearer" (sent as
// x-api-key the API answers 401 "invalid x-api-key"). No beta header is needed
// for the token on anthropic-version 2023-06-01 (verified live, #702). Such a
// token expires within hours and only the CLI refreshes it, so it suits a bench
// run or a developer machine, not a deployment's static key.
func setAuth(h http.Header, key string) {
	if strings.HasPrefix(key, oauthTokenPrefix) {
		h.Set("Authorization", "Bearer "+key)
		return
	}
	h.Set("x-api-key", key)
}

// toWireMessages splits the [llm] conversation into the Anthropic system string
// and the user/assistant/tool message list. The first [llm.RoleSystem] message
// becomes the system field; subsequent system messages are folded into it
// (newline-joined) so a multi-part Persona survives. Tool-role messages map to
// user-role tool_result blocks per the Anthropic protocol (ADR-0028).
//
// thoughts, when non-nil, supplies the thinking blocks to put back in front of
// an assistant turn's tool calls (see passthrough.go); replayed lists the
// tool_use ids whose blocks went into this request.
func toWireMessages(msgs []llm.Message, thoughts *thinkingStore) (system string, out []wireMessage, replayed []string) {
	var systemParts []string
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem:
			if m.Text != "" {
				systemParts = append(systemParts, m.Text)
			}
		case llm.RoleAssistant:
			blocks := assistantBlocks(m)
			if thinking, id := storedThinking(m, thoughts); len(thinking) > 0 {
				blocks = append(thinking, blocks...)
				replayed = append(replayed, id)
			}
			out = append(out, wireMessage{Role: "assistant", Content: blocks})
		case llm.RoleTool:
			out = append(out, wireMessage{Role: "user", Content: toolResultBlocks(m)})
		default: // RoleUser and any unknown role
			out = append(out, wireMessage{Role: "user", Content: []contentBlock{{Type: "text", Text: m.Text}}})
		}
	}
	return strings.Join(systemParts, "\n\n"), out, replayed
}

// storedThinking returns the thinking blocks remembered for the assistant turn
// m — looked up by its tool_use ids, the first one that has any — and the id
// they were found under. A turn without tool calls, or one the store never saw
// (another provider's history, a model that did not think), has none.
func storedThinking(m llm.Message, thoughts *thinkingStore) ([]contentBlock, string) {
	for _, tc := range m.ToolCalls {
		raws := thoughts.get(tc.ID)
		if len(raws) == 0 {
			continue
		}
		blocks := make([]contentBlock, len(raws))
		for i, raw := range raws {
			blocks[i] = contentBlock{raw: raw}
		}
		return blocks, tc.ID
	}
	return nil, ""
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

// wireToolChoiceFor maps the [llm.ToolChoice] knob onto the Anthropic tool_choice
// object (#398/#399). Auto (the zero value) → {type:auto}, byte-identical to the
// pre-#398 wire; None → {type:none}; Required → {type:any} (Anthropic's spelling of
// "must call some tool"); Tool → {type:tool,name:X} pinning the model to one tool.
func wireToolChoiceFor(tc llm.ToolChoice) *wireToolChoice {
	switch tc.Mode {
	case llm.ToolChoiceNone:
		return &wireToolChoice{Type: "none"}
	case llm.ToolChoiceRequired:
		return &wireToolChoice{Type: "any"}
	case llm.ToolChoiceTool:
		return &wireToolChoice{Type: "tool", Name: tc.Tool}
	default: // ToolChoiceAuto / zero
		return &wireToolChoice{Type: "auto"}
	}
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
// diagnostic context and returns it as a typed [*providererr.HTTPError] so the
// retry helper can classify the call by status code via errors.As (#124),
// matching the elevenlabs adapters' error shape. Its Error() text is
// byte-identical to the former fmt.Errorf literal, so grep-based harnesses and
// logs are unchanged.
func readErrorResponse(resp *http.Response, op string) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return &providererr.HTTPError{
		Op:         "anthropic." + op,
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Body:       strings.TrimSpace(string(snippet)),
	}
}

// streamEvents reads the Anthropic SSE response from r, decodes each event, and
// emits the corresponding [llm.StreamEvent] on ch. It closes ch on stream end
// (after an [llm.EventDone]) or ctx cancellation; any read/parse failure, a
// server error frame, or a response exceeding maxStreamBytes emits a terminal
// [llm.EventError] first so consumers never mistake a truncated stream for a
// complete reply. r is closed before return so the connection is released to
// the pool.
//
// SSE framing: each event is a "data: <json>\n" line (the "event:" line is
// redundant with the JSON "type" field, so it is ignored). Decoding tracks
// per-index content blocks: a content_block_start for a tool_use block records
// its id/name; the input_json_delta lines accumulate the arguments string; the
// content_block_stop flushes a completed tool_use as one [llm.EventToolCall].
func streamEvents(ctx context.Context, r io.ReadCloser, ch chan<- llm.StreamEvent, thoughts *thinkingStore) {
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

	// Thinking blocks of this assistant turn, in stream order: the open ones
	// accumulate their text and signature by stream index, and each finished
	// block is kept so the turn's tool_use ids can be stored against them
	// (passthrough.go).
	type pendingThought struct {
		text      strings.Builder
		signature strings.Builder
	}
	thinking := map[int]*pendingThought{}
	var thought []json.RawMessage

	// inputTokens is stashed from message_start's usage (Anthropic reports the
	// prompt tokens there) and combined with message_delta's output_tokens into the
	// one EventUsage emitted before EventDone (#127, ADR-0045).
	var inputTokens int

	send := func(ev llm.StreamEvent) bool {
		select {
		case <-ctx.Done():
			return false
		case ch <- ev:
			return true
		}
	}

	fail := func(msg string) {
		send(llm.StreamEvent{Type: llm.EventError, Err: msg})
	}

	sc := bufio.NewScanner(r)
	// SSE lines are small, but a tool_use input or a long text delta can exceed
	// bufio's default 64KiB token cap; raise the ceiling so a big block does
	// not truncate the stream.
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
			fail(fmt.Sprintf("anthropic: response stream exceeded %d bytes", maxStreamBytes))
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

		var ev sseEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			fail("anthropic: malformed SSE frame: " + err.Error())
			return
		}

		switch ev.Type {
		case "message_start":
			// Stash the prompt-token count; message_delta later carries output_tokens.
			if ev.Message != nil && ev.Message.Usage != nil {
				inputTokens = ev.Message.Usage.InputTokens
			}
		case "content_block_start":
			switch ev.ContentBlock.Type {
			case "tool_use":
				tools[ev.Index] = &pendingTool{id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
			case "thinking":
				pt := &pendingThought{}
				pt.text.WriteString(ev.ContentBlock.Thinking)
				pt.signature.WriteString(ev.ContentBlock.Signature)
				thinking[ev.Index] = pt
			case "redacted_thinking":
				// Arrives whole; there are no deltas for it.
				thought = append(thought, redactedThinkingBlock(ev.ContentBlock.Data))
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
			case "thinking_delta":
				if pt := thinking[ev.Index]; pt != nil {
					pt.text.WriteString(ev.Delta.Thinking)
				}
			case "signature_delta":
				if pt := thinking[ev.Index]; pt != nil {
					pt.signature.WriteString(ev.Delta.Signature)
				}
			}
		case "content_block_stop":
			if pt := thinking[ev.Index]; pt != nil {
				delete(thinking, ev.Index)
				thought = append(thought, thinkingBlock(pt.text.String(), pt.signature.String()))
			}
			if pt := tools[ev.Index]; pt != nil {
				delete(tools, ev.Index)
				// Remember this turn's thinking under the tool_use id BEFORE the
				// call is handed out: the consumer may send the next round as soon
				// as it has the call.
				thoughts.put(pt.id, thought)
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
			// Usage rides message_delta (output_tokens); emit ONE EventUsage carrying
			// the stashed input plus this output, BEFORE EventDone (#127, ADR-0045). A
			// stream that omits usage (an old cassette) sends nothing here, so the
			// consumer estimates rather than recording zero.
			if ev.Usage != nil {
				if !send(llm.StreamEvent{Type: llm.EventUsage, Usage: llm.Usage{
					InputTokens:  inputTokens,
					OutputTokens: ev.Usage.OutputTokens,
				}}) {
					return
				}
			}
			if ev.Delta.StopReason != "" {
				if !send(llm.StreamEvent{Type: llm.EventDone, StopReason: ev.Delta.StopReason}) {
					return
				}
			}
		case "error":
			fail("anthropic: server error event: " + data)
			return
		}
	}
	if err := sc.Err(); err != nil {
		if ctx.Err() != nil {
			return // cancellation closes cleanly; not a stream failure (the llm.Provider contract)
		}
		fail("anthropic: read stream: " + err.Error())
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
		// thinking / redacted_thinking blocks (replayed on the next tool round).
		Thinking  string `json:"thinking"`
		Signature string `json:"signature"`
		Data      string `json:"data"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
	} `json:"delta"`
	// Message carries the prompt-token usage on message_start; Usage carries the
	// output-token usage on message_delta (#127). Both are pointers so an absent
	// usage object (an old cassette, a gateway that omits it) is distinguishable
	// from a zero count — nil means "no usage reported", not "zero tokens".
	Message *struct {
		Usage *usageWire `json:"usage"`
	} `json:"message"`
	Usage *usageWire `json:"usage"`
}

// usageWire is the Anthropic usage object as it appears on message_start
// (input_tokens) and message_delta (output_tokens).
type usageWire struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}
