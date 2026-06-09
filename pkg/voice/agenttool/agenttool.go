// Package agenttool bridges the streaming LLM layer (pkg/voice/llm) to the
// vendor-agnostic tool-use loop (pkg/tool), giving an Agent Tool use without
// either side learning about the other.
//
// It is the one place that imports both pkg/voice/llm (+ a concrete provider
// like anthropic) and pkg/tool. Two pieces:
//
//   - [providerAdapter] satisfies [tool.Provider] over a streaming
//     [llm.Provider]: it drains one completion stream into the single
//     [tool.AssistantMessage] the loop's [tool.Provider.Generate] expects
//     (accumulate text deltas, collect tool calls), so the non-streaming loop
//     drives the streaming adapter (ADR-0028).
//   - [Engine] satisfies [agent.Engine]: it converts the Hot Context the Agent
//     loop assembled ([]llm.Message) into [tool.Message]s, runs [tool.Loop.Run]
//     to completion (Generate → execute granted Tools → feed results back →
//     Generate again), and returns the model's final text.
//
// Wiring it into an Agent: build the [Engine] and pass it as [agent.Config].Engine.
// With empty grants the loop simply does one Generate and returns text, so the
// same path covers the no-tool case; the agent package never imports pkg/tool.
package agenttool

import (
	"context"
	"errors"

	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// providerAdapter makes a streaming [llm.Provider] usable as the non-streaming
// [tool.Provider] the tool-use loop drives. model and maxTokens are baked in
// here because [tool.Provider.Generate] carries no generation knobs — the
// per-Agent LLM config lives on the bridge, not in the loop.
type providerAdapter struct {
	provider  llm.Provider
	model     string
	maxTokens int
}

// Generate implements [tool.Provider]. It issues one streaming completion for
// the conversation and declared Tools, drains the stream, and returns the
// accumulated assistant text plus any tool calls the model requested. A
// Provider start error propagates. A stream that ends without an
// [llm.EventDone] — an [llm.EventError], a ctx cancellation, or a silent
// truncation — is an error, never a partial answer presented as complete.
func (a providerAdapter) Generate(ctx context.Context, messages []tool.Message, tools []tool.Decl) (tool.AssistantMessage, error) {
	stream, err := a.provider.Complete(ctx, llm.Request{
		Model:     a.model,
		MaxTokens: a.maxTokens,
		Messages:  toLLMMessages(messages),
		Tools:     toLLMToolDefs(tools),
	})
	if err != nil {
		return tool.AssistantMessage{}, err
	}

	var out tool.AssistantMessage
	var text []byte
	var done bool
	var streamErr error
	for ev := range stream {
		switch ev.Type {
		case llm.EventText:
			text = append(text, ev.Text...)
		case llm.EventToolCall:
			out.ToolCalls = append(out.ToolCalls, tool.ToolCall{
				ID:    ev.ToolCall.ID,
				Name:  ev.ToolCall.Name,
				Input: ev.ToolCall.Input,
			})
		case llm.EventDone:
			done = true
		case llm.EventError:
			streamErr = errors.New(ev.Err)
		}
	}
	if streamErr != nil {
		return tool.AssistantMessage{}, streamErr
	}
	if !done {
		if err := ctx.Err(); err != nil {
			return tool.AssistantMessage{}, err
		}
		return tool.AssistantMessage{}, errors.New("agenttool: completion stream ended without done event (truncated response)")
	}
	out.Text = string(text)
	return out, nil
}

// Engine is the [agent.Engine] that drives the tool-use loop. Build with
// [NewEngine] and pass as [agent.Config].Engine.
type Engine struct {
	loop *tool.Loop
}

// NewEngine builds an [Engine] over a streaming [llm.Provider] and the Agent's
// tool grants. model/maxTokens are the per-Agent LLM config used for every
// generation step. provider and grants must be non-nil ([tool.NewLoop] panics
// otherwise) — they are wiring requirements. maxRounds caps tool-call rounds;
// zero uses [tool.DefaultMaxRounds].
func NewEngine(provider llm.Provider, grants *tool.GrantSet, model string, maxTokens, maxRounds int) *Engine {
	loop := tool.NewLoop(providerAdapter{provider: provider, model: model, maxTokens: maxTokens}, grants)
	loop.MaxRounds = maxRounds
	return &Engine{loop: loop}
}

// Generate implements [agent.Engine]. It converts the assembled Hot Context to
// [tool.Message]s and runs the loop to its final text.
func (e *Engine) Generate(ctx context.Context, messages []llm.Message) (string, error) {
	return e.loop.Run(ctx, toToolMessages(messages))
}

// toToolMessages converts the agent loop's [llm.Message]s into [tool.Message]s.
// The two vocabularies are field-identical by design (the seam agreed in tasks
// #2/#3), so this is a mechanical copy; it lives here so neither package
// depends on the other.
func toToolMessages(msgs []llm.Message) []tool.Message {
	out := make([]tool.Message, len(msgs))
	for i, m := range msgs {
		tm := tool.Message{Role: tool.Role(m.Role), Text: m.Text}
		if len(m.ToolCalls) > 0 {
			tm.ToolCalls = make([]tool.ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				tm.ToolCalls[j] = tool.ToolCall{ID: tc.ID, Name: tc.Name, Input: tc.Input}
			}
		}
		if len(m.ToolResults) > 0 {
			tm.ToolResults = make([]tool.ToolResult, len(m.ToolResults))
			for j, tr := range m.ToolResults {
				tm.ToolResults[j] = tool.ToolResult{CallID: tr.CallID, Content: tr.Content, IsError: tr.IsError}
			}
		}
		out[i] = tm
	}
	return out
}

// toLLMMessages is the reverse of [toToolMessages]: the loop hands the adapter
// [tool.Message]s (the conversation it has grown with tool turns), which the
// adapter sends to the streaming provider as [llm.Message]s.
func toLLMMessages(msgs []tool.Message) []llm.Message {
	out := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		lm := llm.Message{Role: llm.Role(m.Role), Text: m.Text}
		if len(m.ToolCalls) > 0 {
			lm.ToolCalls = make([]llm.ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				lm.ToolCalls[j] = llm.ToolCall{ID: tc.ID, Name: tc.Name, Input: tc.Input}
			}
		}
		if len(m.ToolResults) > 0 {
			lm.ToolResults = make([]llm.ToolResult, len(m.ToolResults))
			for j, tr := range m.ToolResults {
				lm.ToolResults[j] = llm.ToolResult{CallID: tr.CallID, Content: tr.Content, IsError: tr.IsError}
			}
		}
		out[i] = lm
	}
	return out
}

// toLLMToolDefs converts the loop's grant-stripped [tool.Decl]s into the
// [llm.ToolDef]s the provider advertises to the model. Same fields, different
// package — the only non-mechanical part of the seam.
func toLLMToolDefs(decls []tool.Decl) []llm.ToolDef {
	if len(decls) == 0 {
		return nil
	}
	out := make([]llm.ToolDef, len(decls))
	for i, d := range decls {
		out[i] = llm.ToolDef{Name: d.Name, Description: d.Description, InputSchema: d.InputSchema}
	}
	return out
}
