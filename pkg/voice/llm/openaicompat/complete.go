package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// maxStreamBytes caps the decoded size of one completion (summed content and
// tool-argument bytes). A voice turn's reply is a few KiB; 16 MiB is far past any
// legitimate completion and bounds memory growth from a misbehaving or hostile
// WithBaseURL gateway — the same guard the hand-rolled SSE reader carried before
// the SDK owned the framing.
const maxStreamBytes = 16 * 1024 * 1024

// Complete implements [llm.Provider]. It opens a streaming chat/completions
// request via the OpenAI SDK and returns a channel of [llm.StreamEvent]s decoded
// from the SDK's typed chunk stream: content deltas become [llm.EventText],
// completed tool_call deltas become [llm.EventToolCall], and the terminating
// chunk's finish_reason becomes [llm.EventDone].
//
// A non-nil error is returned only when the call cannot be started — a missing
// key, an empty request, or a non-2xx/transport failure the SDK surfaces before
// the first chunk. A mid-stream failure emits a terminal [llm.EventError] and
// closes the channel; a ctx cancellation closes it with neither terminal event —
// so a consumer never mistakes a truncated stream for a complete reply. Callers
// must drain the channel to release the streaming goroutine.
func (c *Client) Complete(ctx context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("%s.Complete: missing API key%s", c.name, c.missingKeyHint())
	}

	messages := toMessages(req.Messages)
	if len(messages) == 0 {
		return nil, fmt.Errorf("%s.Complete: request has no messages", c.name)
	}

	model := req.Model
	if model == "" {
		model = c.model
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = c.maxTokens
	}

	params := openai.ChatCompletionNewParams{
		Model:     openai.ChatModel(model),
		Messages:  messages,
		MaxTokens: openai.Int(int64(maxTokens)),
	}
	if tools := toTools(req.Tools); len(tools) > 0 {
		params.Tools = tools
		// auto: the model decides whether to call a tool. The SDK defaults to auto
		// when tools are present, but we set it explicitly so the wire is stable.
		params.ToolChoice = openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: openai.String("auto")}
	}
	if c.reasoningEffort != "" {
		params.ReasoningEffort = openai.ReasoningEffort(c.reasoningEffort)
	}

	// Non-standard body fields (e.g. Gemini's extra_body thinking_config) ride via
	// the SDK's JSON-set escape hatch so nested values serialize verbatim.
	reqOpts := make([]option.RequestOption, 0, len(c.extraFields))
	for k, v := range c.extraFields {
		reqOpts = append(reqOpts, option.WithJSONSet(k, v))
	}

	stream := c.oai.Chat.Completions.NewStreaming(ctx, params, reqOpts...)
	// The SDK surfaces a startup failure (non-2xx, bad request, transport error)
	// on the stream before any chunk is read; return it as the call-cannot-start
	// error rather than a mid-stream EventError.
	if err := stream.Err(); err != nil {
		return nil, fmt.Errorf("%s.Complete: %w", c.name, err)
	}

	ch := make(chan llm.StreamEvent)
	go c.streamEvents(ctx, stream, ch)
	return ch, nil
}

// missingKeyHint renders the actionable tail of the missing-key error, naming the
// provider env var when the preset supplied one.
func (c *Client) missingKeyHint() string {
	if c.apiKeyEnvHint == "" {
		return " (pass it to New)"
	}
	return fmt.Sprintf(" (set %s or pass it to New)", c.apiKeyEnvHint)
}

// toMessages maps the [llm] conversation onto OpenAI chat messages. Unlike
// Anthropic, the system prompt stays in the list as a "system"-role message. An
// assistant turn that requested tools renders its [llm.ToolCall]s as the
// tool_calls array (arguments as a JSON string). Each [llm.RoleTool] message
// expands to one tool-role message PER result, correlated by tool_call_id — the
// OpenAI shape, where parallel results are separate messages rather than blocks
// on one message (ADR-0028).
func toMessages(msgs []llm.Message) []openai.ChatCompletionMessageParamUnion {
	var out []openai.ChatCompletionMessageParamUnion
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleSystem:
			out = append(out, openai.SystemMessage(m.Text))
		case llm.RoleAssistant:
			out = append(out, assistantMessage(m))
		case llm.RoleTool:
			out = append(out, toolResultMessages(m)...)
		default: // RoleUser and any unknown role
			out = append(out, openai.UserMessage(m.Text))
		}
	}
	return out
}

// assistantMessage renders an assistant [llm.Message]. A turn that only spoke
// uses the SDK's plain helper; a turn that requested tools carries its spoken
// text (if any) plus one tool_call per [llm.ToolCall], the arguments travelling
// as a JSON string per the OpenAI wire shape.
func assistantMessage(m llm.Message) openai.ChatCompletionMessageParamUnion {
	if len(m.ToolCalls) == 0 {
		return openai.AssistantMessage(m.Text)
	}
	am := openai.ChatCompletionAssistantMessageParam{}
	if m.Text != "" {
		am.Content.OfString = openai.String(m.Text)
	}
	am.ToolCalls = make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(m.ToolCalls))
	for _, tc := range m.ToolCalls {
		args := string(tc.Input)
		if args == "" {
			args = "{}" // a no-argument call still needs valid JSON arguments
		}
		am.ToolCalls = append(am.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
			OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
				ID: tc.ID,
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
					Name:      tc.Name,
					Arguments: args,
				},
			},
		})
	}
	return openai.ChatCompletionMessageParamUnion{OfAssistant: &am}
}

// toolResultMessages expands a [llm.RoleTool] message into one tool-role message
// per result, each keyed by its tool_call_id — OpenAI's shape, where parallel
// results are separate messages (ADR-0028).
func toolResultMessages(m llm.Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(m.ToolResults))
	for _, tr := range m.ToolResults {
		out = append(out, openai.ToolMessage(tr.Content, tr.CallID))
	}
	return out
}

// toTools maps the [llm.ToolDef]s onto the OpenAI function-tools array. Returns
// nil for an empty input so the request omits the field. The input JSON Schema is
// decoded into the SDK's parameters map; a malformed schema (it never is in
// practice — the registry builds it) sends the tool without parameters rather
// than failing the turn.
func toTools(defs []llm.ToolDef) []openai.ChatCompletionToolUnionParam {
	if len(defs) == 0 {
		return nil
	}
	tools := make([]openai.ChatCompletionToolUnionParam, 0, len(defs))
	for _, d := range defs {
		fn := openai.FunctionDefinitionParam{Name: d.Name}
		if d.Description != "" {
			fn.Description = openai.String(d.Description)
		}
		if len(d.InputSchema) > 0 {
			var params openai.FunctionParameters
			if err := json.Unmarshal(d.InputSchema, &params); err == nil {
				fn.Parameters = params
			}
		}
		tools = append(tools, openai.ChatCompletionFunctionTool(fn))
	}
	return tools
}

// streamEvents drains the SDK chunk stream and emits the corresponding
// [llm.StreamEvent]s on ch. It closes ch on stream end (after an [llm.EventDone]),
// ctx cancellation, or the first read/parse error. A parse/transport failure or a
// response exceeding maxStreamBytes emits a terminal [llm.EventError] first so
// consumers never mistake a truncated stream for a complete reply; a ctx
// cancellation closes with neither terminal event. The stream is closed before
// return so the connection is released.
//
// Tool calls stream incrementally: the first delta for a given tool_calls index
// carries the id and function.name; later deltas with the same index append
// function.arguments fragments. A completed call is flushed as one
// [llm.EventToolCall] when finish_reason arrives (OpenAI emits no per-call stop,
// so the finish chunk closes out all pending calls in arrival order).
func (c *Client) streamEvents(ctx context.Context, stream *ssestream.Stream[openai.ChatCompletionChunk], ch chan<- llm.StreamEvent) {
	defer close(ch)
	defer stream.Close()

	type pendingTool struct {
		id   string
		name string
		args strings.Builder
	}
	tools := map[int64]*pendingTool{}
	var order []int64

	send := func(ev llm.StreamEvent) bool {
		select {
		case <-ctx.Done():
			return false
		case ch <- ev:
			return true
		}
	}
	fail := func(msg string) { send(llm.StreamEvent{Type: llm.EventError, Err: msg}) }

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
				Type:     llm.EventToolCall,
				ToolCall: llm.ToolCall{ID: pt.id, Name: pt.name, Input: json.RawMessage(args)},
			}) {
				return false
			}
		}
		return true
	}

	// total bounds the summed decoded content + tool-argument bytes so a hostile
	// endpoint cannot grow memory without limit.
	var total int
	overBudget := func(n int) bool {
		if total += n; total > maxStreamBytes {
			fail(fmt.Sprintf("%s: response stream exceeded %d bytes", c.name, maxStreamBytes))
			return true
		}
		return false
	}

	for stream.Next() {
		if ctx.Err() != nil {
			return
		}
		chunk := stream.Current()
		if len(chunk.Choices) == 0 {
			continue // usage-only or otherwise choice-less trailing chunk
		}
		choice := chunk.Choices[0]

		if delta := choice.Delta.Content; delta != "" {
			if overBudget(len(delta)) {
				return
			}
			if !send(llm.StreamEvent{Type: llm.EventText, Text: delta}) {
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
			if args := tc.Function.Arguments; args != "" {
				if overBudget(len(args)) {
					return
				}
				pt.args.WriteString(args)
			}
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

	if err := stream.Err(); err != nil {
		if ctx.Err() != nil {
			return // cancellation closes cleanly; not a stream failure
		}
		fail(fmt.Sprintf("%s: read stream: %s", c.name, err.Error()))
	}
}
