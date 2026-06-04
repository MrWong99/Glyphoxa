package tool

import (
	"context"
	"encoding/json"
)

// This file holds the LLM message / tool_call data shapes — the integration
// seam agreed with the Agent loop (task #2). The framework owns these types so
// the tool-use loop is provider-agnostic; a concrete LLM Provider adapter
// (Anthropic, Ollama) converts its vendor types to and from these at the wiring
// phase. Keeping them here means the loop never imports a vendor SDK.

// Role tags a [Message]'s author in the conversation handed to the LLM.
type Role string

const (
	// RoleSystem is the system prompt (Persona, instructions).
	RoleSystem Role = "system"
	// RoleUser is the human / upstream turn.
	RoleUser Role = "user"
	// RoleAssistant is the model's own prior output, including any tool_calls
	// it emitted.
	RoleAssistant Role = "assistant"
	// RoleTool is a tool-role result the loop feeds back after executing a
	// tool_call (its [Message.ToolResults] carry the payloads).
	RoleTool Role = "tool"
)

// Message is one role-tagged turn in the conversation the [Provider] sees.
// Most messages carry only Text. An assistant message that called tools also
// carries ToolCalls; the loop's tool-role reply carries ToolResults.
type Message struct {
	Role Role

	// Text is the natural-language content. Empty is valid for an assistant
	// message that only emitted tool_calls, or a tool-role message that only
	// carries ToolResults.
	Text string

	// ToolCalls are the tool_calls an assistant message emitted. Set only on
	// RoleAssistant messages.
	ToolCalls []ToolCall

	// ToolResults are the executed tool results carried by a RoleTool message,
	// one per ToolCall the loop ran. Set only on RoleTool messages.
	ToolResults []ToolResult
}

// ToolCall is one tool invocation the LLM emitted: the Tool to call (Name), the
// arguments (Input, raw JSON validated against the Tool's input schema by the
// model), and an ID the provider assigns so the matching [ToolResult] can be
// correlated back. The field is named Input to match the Anthropic-native
// wording and the llm.ToolCall shape on the provider side of the seam (task #2).
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult is the outcome of executing one [ToolCall], fed back to the LLM as
// part of a RoleTool [Message]. CallID echoes the [ToolCall.ID] it answers.
// IsError marks a failed execution so the model can react rather than treat the
// error text as data.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// Decl is a Tool declared to the LLM: the grant-stripped advertisement of one
// callable. It is produced from a [Tool] by [GrantSet.Declarations] and is what
// a [Provider] translates into its vendor tool-spec.
type Decl struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// Provider is the LLM seam the tool-use loop drives. Generate runs one
// generation step: given the conversation so far and the declared (granted)
// Tools, it returns the model's [AssistantMessage] — either final text
// (ToolCalls empty) or one or more tool_calls the loop must execute and feed
// back before calling Generate again.
//
// The Provider is supplied by the Agent loop's LLM adapter; the framework ships
// only a scripted fake for its own tests.
type Provider interface {
	Generate(ctx context.Context, messages []Message, tools []Decl) (AssistantMessage, error)
}

// AssistantMessage is one [Provider.Generate] result: the model's text plus any
// tool_calls it wants run. The loop terminates and returns Text when ToolCalls
// is empty.
type AssistantMessage struct {
	Text      string
	ToolCalls []ToolCall
}
