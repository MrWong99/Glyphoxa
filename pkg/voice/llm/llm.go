// Package llm defines the v2 LLM provider surface — the "llm" Component per
// CONTEXT.md — consumed by the Agent loop that drives an Agent's spoken turn.
//
// The package splits a small provider contract ([Provider]) from the message
// vocabulary it exchanges ([Message], [ToolCall], [ToolResult], [ToolDef]).
// Real providers (Anthropic, Ollama — see ADR-0004) and the cassette replayer
// (see [github.com/MrWong99/Glyphoxa/pkg/voice/voicecassette]) both implement
// Provider. The Agent loop is provider-agnostic: it assembles Hot Context into
// a [Request] and consumes the streamed [StreamEvent]s.
//
// Per ADR-0021 LLM calls in tests use a VCR-style cassette so unit tests run
// without a live API key. Per ADR-0028 the message vocabulary carries
// tool-call and tool-role messages so the tool-use loop (the orchestrator
// executes a [ToolCall], feeds the [ToolResult] back as a tool-role [Message],
// and re-calls the Provider) can slot in behind the same [Provider] contract
// without reshaping it.
package llm

import (
	"context"
	"encoding/json"
)

// Role is the author of one [Message] in a [Request]'s conversation.
type Role string

const (
	// RoleSystem is the system prompt — the Persona + KG facts + audio-markup
	// instruction the Agent loop assembles. Providers map it to their native
	// system slot, not an in-band message.
	RoleSystem Role = "system"

	// RoleUser is a human utterance (the routed Transcript text) or, per
	// ADR-0028, a tool result fed back to the model.
	RoleUser Role = "user"

	// RoleAssistant is the Agent's own prior turn, including any [ToolCall]s it
	// requested.
	RoleAssistant Role = "assistant"

	// RoleTool carries a [ToolResult] back to the model after the orchestrator
	// executed a tool the assistant requested (ADR-0028). Providers that model
	// tool results as a user-role block (e.g. Anthropic) translate this on the
	// wire; the vocabulary keeps them distinct so the loop reads clearly.
	RoleTool Role = "tool"
)

// Message is one entry in the conversation sent to a [Provider].
//
// Text holds prose for system/user/assistant turns. ToolCalls is set on an
// assistant Message the model emitted to request tools (ADR-0028); ToolResult
// is set on a [RoleTool] Message the orchestrator appends after executing one.
// At most one of {Text-only, ToolCalls, ToolResult} is meaningful per Message,
// per its Role.
type Message struct {
	Role Role

	// Text is the prose content. For an assistant Message that both speaks and
	// requests tools, Text is the spoken preamble and ToolCalls the requests.
	Text string

	// ToolCalls are the tools an assistant Message asked the orchestrator to
	// invoke. Nil on every non-assistant Message and on assistant turns that
	// only spoke. The tool-use loop (ADR-0028) is the consumer.
	ToolCalls []ToolCall

	// ToolResults are the outcomes of the [ToolCall]s executed in response to
	// one assistant turn, set only on a [RoleTool] Message. A slice, not a
	// single result, because one assistant turn may request several tools in
	// parallel and the wire protocols (e.g. Anthropic) require all their
	// results carried in the one following tool-role Message — symmetric with
	// [Message.ToolCalls]. Nil on every non-[RoleTool] Message.
	ToolResults []ToolResult
}

// ToolCall is the model's request to invoke one named Tool, decoded from the
// provider's streamed tool-use block. ID correlates the later [ToolResult]
// back to this call (the wire protocols require the pairing).
type ToolCall struct {
	// ID is the provider-assigned identifier for this invocation; echoed back
	// in the matching [ToolResult.CallID].
	ID string

	// Name is the granted Tool's name (CONTEXT.md "Tool"); the orchestrator
	// resolves it against the registry (ADR-0028).
	Name string

	// Input is the tool's arguments as provider-native JSON, passed opaquely to
	// the Tool handler.
	Input json.RawMessage
}

// ToolResult is the outcome of executing one [ToolCall], appended to the
// conversation as a [RoleTool] [Message] for the next Provider call (ADR-0028).
// The Agent loop in v1.0 does not produce these — the tool-use loop does — but
// the vocabulary carries them so that loop needs no Provider change.
type ToolResult struct {
	// CallID matches the originating [ToolCall.ID].
	CallID string

	// Content is the textual result the model reads on its next turn.
	Content string

	// IsError marks a failed execution so the model can recover rather than
	// treat the content as a successful result.
	IsError bool
}

// ToolDef describes one Tool the model may call, built by the Agent loop from
// an Agent's granted Tools (ADR-0028: "name, input JSON schema"). It is the
// subset of a Tool the Provider needs to advertise the call to the model — the
// handler and grant scope stay on the orchestrator side.
type ToolDef struct {
	// Name is the Tool's name, matched against [ToolCall.Name] on the way back.
	Name string

	// Description tells the model when to use the Tool.
	Description string

	// InputSchema is the Tool's input JSON Schema, passed to the provider
	// verbatim.
	InputSchema json.RawMessage
}

// Request is one completion call: the assembled conversation plus the Tools the
// Agent may call and generation limits. The system prompt travels as a
// [RoleSystem] [Message] in Messages; providers route it to their native system
// slot.
type Request struct {
	// Model is the provider-specific model identifier (e.g. "claude-opus-4-8").
	// Empty lets the Provider fall back to its configured default.
	Model string

	// Messages is the ordered conversation. The first entry is conventionally
	// the [RoleSystem] prompt; the remainder alternate user/assistant turns
	// with tool-role messages interleaved per ADR-0028.
	Messages []Message

	// Tools are the [ToolDef]s the model may call this turn. Nil disables tool
	// use — the v1.0 Agent loop passes nil; the tool-use loop populates it.
	Tools []ToolDef

	// MaxTokens caps the completion length. Zero lets the Provider choose a
	// sane default.
	MaxTokens int
}

// StreamEventType discriminates a [StreamEvent].
type StreamEventType int

const (
	// EventText carries an incremental chunk of assistant prose in
	// [StreamEvent.Text]. The Agent loop accumulates these into the spoken
	// sentence(s).
	EventText StreamEventType = iota

	// EventToolCall carries one fully-decoded [StreamEvent.ToolCall] the model
	// requested. Emitted once the provider has streamed the call's complete
	// arguments. Consumed by the tool-use loop (ADR-0028).
	EventToolCall

	// EventDone marks the end of the completion. [StreamEvent.StopReason]
	// carries the provider's reason (e.g. "end_turn", "tool_use", "max_tokens")
	// so the tool-use loop can decide whether to continue.
	EventDone

	// EventError marks a mid-stream failure — a transport error, a malformed
	// frame, or a response exceeding the provider's size bounds — with the
	// detail in [StreamEvent.Err]. It is terminal: the provider closes the
	// channel right after. Consumers must treat the accumulated text as
	// truncated and fail the turn rather than present it as a complete reply.
	EventError
)

// StreamEvent is one item in a [Provider] completion stream. The active fields
// depend on Type: Text on [EventText], ToolCall on [EventToolCall], StopReason
// on [EventDone].
type StreamEvent struct {
	Type StreamEventType

	// Text is the prose delta on an [EventText].
	Text string

	// ToolCall is the decoded request on an [EventToolCall].
	ToolCall ToolCall

	// StopReason is the provider's completion reason on an [EventDone].
	StopReason string

	// Err is the failure description on an [EventError]. A string, not an
	// error, so cassette recordings serialize it faithfully (ADR-0021).
	Err string
}

// Provider is the hot-path interface implemented by every LLM provider and by
// the cassette replayer.
//
// Complete starts a streaming completion for req and returns a channel of
// [StreamEvent]s. The implementation closes the channel when the completion is
// done (after emitting an [EventDone]) or ctx is cancelled; callers must drain
// it to release the implementation's goroutines — mirroring
// [github.com/MrWong99/Glyphoxa/pkg/voice/tts.Synthesizer]. A non-nil error is
// returned only when the call cannot be started (missing key, bad request,
// non-2xx response); a mid-stream failure emits an [EventError] and then
// closes the channel. A channel that closes with neither an [EventDone] nor an
// [EventError] was cancelled via ctx; consumers must not treat any of these
// early-closed streams' accumulated text as a complete reply.
type Provider interface {
	Complete(ctx context.Context, req Request) (<-chan StreamEvent, error)
}
