package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/ssestream"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
)

// maxStreamBytes caps the raw bytes of one completion's response body. The cap
// is enforced BELOW the SDK, on the HTTP transport (see budgetTransport in
// openaicompat.go): every wire byte counts, whether or not the SDK ever surfaces
// it as a chunk — so a hostile or misbehaving WithBaseURL gateway cannot dodge
// it with zero-content frames, a post-[DONE] flood the SDK drains internally, or
// one endless unterminated data: line. A voice turn's reply is a few KiB; 16 MiB
// is far past any legitimate completion. This restores the raw-byte guard the
// hand-rolled SSE reader had before ADR-0037 handed the framing to the SDK.
// streamEvents additionally counts each decoded chunk's re-serialized JSON
// ([openai.ChatCompletionChunk.RawJSON]) against the same budget as defense in
// depth.
const maxStreamBytes = 16 * 1024 * 1024

// maxStreamChunks caps the number of decoded chunks in one completion so a flood
// of tiny zero-content frames — which would erode the byte budget only slowly —
// terminates promptly with a specific error. A legitimate completion streams
// roughly one chunk per token plus a handful of bookkeeping frames, orders of
// magnitude below this.
const maxStreamChunks = 100_000

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
		// The per-round tool-choice knob (#398/#399). The zero value maps to "auto"
		// exactly as before — the SDK defaults to auto when tools are present, but we
		// set it explicitly so the wire is stable. tool_choice is only sent when tools
		// are declared (the llm.Request contract: ToolChoice is inert with no tools).
		params.ToolChoice = toToolChoice(req.ToolChoice)
	}
	if c.reasoningEffort != "" {
		params.ReasoningEffort = openai.ReasoningEffort(c.reasoningEffort)
	}
	if c.includeUsage {
		// Ask for the trailing usage chunk (token metering, #127). Preset-gated: a
		// gateway that rejects stream_options would 400, so only presets that verified
		// the endpoint honours it turn this on.
		params.StreamOptions = openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)}
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
		return nil, c.startError(err)
	}

	ch := make(chan llm.StreamEvent)
	go c.streamEvents(ctx, stream, ch)
	return ch, nil
}

// startError converts a stream-start failure into the call-cannot-start error.
// A vendor HTTP status error — the SDK's *openai.Error, which carries the status
// code — becomes a typed [*providererr.HTTPError] so the retry helper can
// classify it (a 429/5xx is retryable) via errors.As, exactly like the
// hand-rolled adapters (#124, ADR-0044). The Op/status/body preserve the
// operation name, HTTP status, and the SDK's full message (which carries the
// response body) so the surface stays diagnosable. Any non-HTTP startup failure
// (transport, empty request) keeps the plain prose wrap — it is not retryable and
// needs no status.
func (c *Client) startError(err error) error {
	// A tool_use_failed 400 is a per-round policy signal, not a transient HTTP
	// fault: surface it as a distinct [*providererr.ToolSyntaxError] so the agenttool
	// bridge retries the round tool-less rather than the generic retry helper failing
	// fast on a 4xx (#398). Byte-preserve the SDK message for diagnosability.
	if isToolSyntaxErr(err) {
		return &providererr.ToolSyntaxError{Op: c.name + ".Complete", Msg: err.Error()}
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode != 0 {
		return &providererr.HTTPError{
			Op:         c.name + ".Complete",
			StatusCode: apiErr.StatusCode,
			Status:     http.StatusText(apiErr.StatusCode),
			Body:       err.Error(),
		}
	}
	return fmt.Errorf("%s.Complete: %w", c.name, err)
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

// toToolChoice maps the [llm.ToolChoice] knob onto the OpenAI tool_choice union
// (#398/#399): Auto (the zero value) and None/Required serialize as the bare
// strings "auto"/"none"/"required"; Tool serializes as the named-function object
// pinning the model to one tool. The Auto default keeps the pre-#398 wire
// byte-identical for every turn that never sets a choice.
func toToolChoice(tc llm.ToolChoice) openai.ChatCompletionToolChoiceOptionUnionParam {
	switch tc.Mode {
	case llm.ToolChoiceNone:
		return openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: openai.String("none")}
	case llm.ToolChoiceRequired:
		return openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: openai.String("required")}
	case llm.ToolChoiceTool:
		return openai.ChatCompletionToolChoiceOptionUnionParam{
			OfFunctionToolChoice: &openai.ChatCompletionNamedToolChoiceParam{
				Function: openai.ChatCompletionNamedToolChoiceFunctionParam{Name: tc.Tool},
			},
		}
	default: // ToolChoiceAuto / zero
		return openai.ChatCompletionToolChoiceOptionUnionParam{OfAuto: openai.String("auto")}
	}
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
// response exceeding the stream budget (maxStreamBytes raw chunk-JSON bytes or
// maxStreamChunks chunks) emits a terminal [llm.EventError] first so
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

	// Defense in depth behind the transport-level raw-byte cap (budgetTransport,
	// which bounds even streams the SDK drains without surfacing chunks): every
	// decoded chunk also counts — its re-serialized JSON against maxStreamBytes
	// and its count against maxStreamChunks — so a zero-content chunk flood trips
	// a specific budget error here long before the transport cap.
	var totalBytes, totalChunks int
	// sawUsage guards against emitting more than one EventUsage (#127): only the
	// trailing chunk carries a non-null usage, but the guard keeps the contract
	// explicit even if a gateway repeats it.
	var sawUsage bool

	for stream.Next() {
		if ctx.Err() != nil {
			return
		}
		chunk := stream.Current()
		if totalChunks++; totalChunks > maxStreamChunks {
			fail(fmt.Sprintf("%s: response stream exceeded %d chunks", c.name, maxStreamChunks))
			return
		}
		if totalBytes += len(chunk.RawJSON()); totalBytes > maxStreamBytes {
			fail(fmt.Sprintf("%s: response stream exceeded %d bytes", c.name, maxStreamBytes))
			return
		}
		// Capture usage BEFORE the empty-choices skip below: the trailing usage chunk
		// (include_usage) arrives with an empty choices array, so the continue guard
		// would otherwise swallow it silently (#127, ADR-0045). All non-final chunks
		// carry a null (zero) usage, so a non-zero prompt/completion count is the
		// signal; emit exactly one EventUsage for it.
		if u := chunk.Usage; !sawUsage && (u.PromptTokens != 0 || u.CompletionTokens != 0) {
			sawUsage = true
			if !send(llm.StreamEvent{Type: llm.EventUsage, Usage: llm.Usage{
				InputTokens:  int(u.PromptTokens),
				OutputTokens: int(u.CompletionTokens),
			}}) {
				return
			}
		}
		if len(chunk.Choices) == 0 {
			continue // usage-only or otherwise choice-less trailing chunk
		}
		choice := chunk.Choices[0]

		if delta := choice.Delta.Content; delta != "" {
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
		// A provider tool_use_failed surfaces here as an in-stream error frame
		// (the SDK sets stream.Err() from a data: {"error":{…}} chunk). Classify it
		// so the agenttool bridge retries the round rather than abandon the turn
		// (#398); any other stream failure stays the default unclassified error.
		msg := fmt.Sprintf("%s: read stream: %s", c.name, err.Error())
		if isToolSyntaxErr(err) {
			send(llm.StreamEvent{Type: llm.EventError, Err: msg, ErrClass: llm.ErrClassToolSyntax})
			return
		}
		fail(msg)
	}
}

// toolUseFailedCode is the provider error code Groq (and other OpenAI-compat
// gateways) return when the model emitted malformed pseudo-XML instead of a native
// tool call. Detecting it is what lets the agenttool bridge retry the round and, on
// a repeat, regenerate tool-less rather than abandon the voice turn (#398).
const toolUseFailedCode = "tool_use_failed"

// isToolSyntaxErr reports whether err carries the provider's tool_use_failed code,
// on either detection path: the SDK's typed [*openai.Error] (a 400 start error,
// whose Code / RawJSON body carries it) or the in-stream [ssestream.StreamError]
// (whose message embeds the error-object JSON). It reads the typed Code first, then
// falls back to extracting the first embedded JSON object and reading its "code" —
// so neither path relies on brittle whole-string substring matching.
func isToolSyntaxErr(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		if apiErr.Code == toolUseFailedCode {
			return true
		}
		if codeInJSON(apiErr.RawJSON()) == toolUseFailedCode {
			return true
		}
	}
	return codeInJSON(err.Error()) == toolUseFailedCode
}

// codeInJSON extracts the first '{'-delimited JSON object embedded in s and returns
// its error code — either a top-level "code" or a nested "error":{"code"} — or ""
// if nothing parses. A [json.Decoder] is used so trailing text after the object
// (the StreamError's prefix leaves none, but a defensive parse costs nothing) does
// not defeat the decode.
func codeInJSON(s string) string {
	i := strings.IndexByte(s, '{')
	if i < 0 {
		return ""
	}
	var probe struct {
		Code  string `json:"code"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(strings.NewReader(s[i:])).Decode(&probe); err != nil {
		return ""
	}
	if probe.Code != "" {
		return probe.Code
	}
	return probe.Error.Code
}
