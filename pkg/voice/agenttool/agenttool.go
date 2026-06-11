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
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// roundCounterKey is the context key under which [Engine.Generate] stores a
// fresh per-turn round counter (an *int starting at 0). The adapter reads and
// increments it per [tool.Provider.Generate] call so round_index is scoped to
// one turn — never a field on the shared [providerAdapter], which would bleed
// across the concurrent turns barge-in (ADR-0027, WithBargeIn(0)) allows.
type roundCounterKey struct{}

// withRoundCounter returns ctx carrying a fresh per-turn round counter.
func withRoundCounter(ctx context.Context) context.Context {
	n := 0
	return context.WithValue(ctx, roundCounterKey{}, &n)
}

// nextRound returns the 0-based round index for this call and advances the
// per-turn counter. A ctx without a counter (the adapter exercised outside an
// Engine.Generate turn) yields 0 every call rather than panicking.
func nextRound(ctx context.Context) int {
	p, _ := ctx.Value(roundCounterKey{}).(*int)
	if p == nil {
		return 0
	}
	i := *p
	*p++
	return i
}

// providerAdapter makes a streaming [llm.Provider] usable as the non-streaming
// [tool.Provider] the tool-use loop drives. model and maxTokens are baked in
// here because [tool.Provider.Generate] carries no generation knobs — the
// per-Agent LLM config lives on the bridge, not in the loop.
//
// rec/provName carry the A3 instrumentation: one [observe.StageRecorder.LLMRound]
// span per [llm.Provider.Complete] (with round_index/had_tool_call, separating
// H1 thinking from H2 extra rounds) plus a provider-call counter per call. Both
// default to no-ops (observe.Discard, empty provider label) so the keyless path
// and any caller that did not opt in stay silent.
type providerAdapter struct {
	provider  llm.Provider
	model     string
	maxTokens int

	rec      observe.StageRecorder
	provName observe.Provider
}

// Generate implements [tool.Provider]. It issues one streaming completion for
// the conversation and declared Tools, drains the stream, and returns the
// accumulated assistant text plus any tool calls the model requested. A
// Provider start error propagates. A stream that ends without an
// [llm.EventDone] — an [llm.EventError], a ctx cancellation, or a silent
// truncation — is an error, never a partial answer presented as complete.
//
// It records one LLMRound span around the Complete+drain (the H1/H2 cut) and a
// ProviderCall/ProviderError counter for the call. round_index comes from the
// per-turn counter in ctx (see [withRoundCounter]); had_tool_call is known once
// the stream is drained.
func (a providerAdapter) Generate(ctx context.Context, messages []tool.Message, tools []tool.Decl) (tool.AssistantMessage, error) {
	return a.complete(ctx, messages, tools, nil)
}

// GenerateStream implements [tool.StreamingProvider]: same as [Generate], but it
// forwards each prose delta to onText as it streams (B1) — tool-call arguments
// are not forwarded. The A3 per-round instrumentation is identical to Generate's
// (see [complete]), so the streaming production path does not lose the LLMRound /
// provider-call metrics.
func (a providerAdapter) GenerateStream(ctx context.Context, messages []tool.Message, tools []tool.Decl, onText func(delta string) error) (tool.AssistantMessage, error) {
	return a.complete(ctx, messages, tools, onText)
}

// complete issues one streaming completion, drains it, and records the A3
// per-round span + provider-call counter. When onText is non-nil, each
// [llm.EventText] delta is forwarded as it arrives (the streaming path); an error
// onText returns aborts the drain and propagates. The accumulated text and tool
// calls are returned as the [tool.AssistantMessage] the loop expects either way.
func (a providerAdapter) complete(ctx context.Context, messages []tool.Message, tools []tool.Decl, onText func(delta string) error) (tool.AssistantMessage, error) {
	round := nextRound(ctx)
	start := time.Now()

	stream, err := a.provider.Complete(ctx, llm.Request{
		Model:     a.model,
		MaxTokens: a.maxTokens,
		Messages:  toLLMMessages(messages),
		Tools:     toLLMToolDefs(tools),
	})
	if err != nil {
		// A start failure is a provider error with no round span (no completion
		// happened); attribute it to the LLM stage. A cancelled ctx is a timeout-
		// shaped outcome rather than a vendor error.
		outcome := observe.OutcomeError
		if ctx.Err() != nil {
			outcome = observe.OutcomeTimeout
		}
		a.rec.ProviderCall(observe.StageLLM, a.provName, outcome)
		a.rec.ProviderError(observe.StageLLM, a.provName)
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
			if onText != nil {
				if err := onText(ev.Text); err != nil {
					// Downstream cancel (barge-in): record the round we did and stop.
					out.Text = string(text)
					a.rec.LLMRound(a.provName, round, len(out.ToolCalls) > 0, time.Since(start))
					a.rec.ProviderCall(observe.StageLLM, a.provName, observe.OutcomeOK)
					return out, err
				}
			}
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

	hadToolCall := len(out.ToolCalls) > 0
	a.rec.LLMRound(a.provName, round, hadToolCall, time.Since(start))
	a.rec.ProviderCall(observe.StageLLM, a.provName, observe.OutcomeOK)
	return out, nil
}

// Engine is the [agent.Engine] that drives the tool-use loop. Build with
// [NewEngine] and pass as [agent.Config].Engine.
//
// It holds two pre-built loops over the same provider adapter: full (every
// granted Tool declared) and gated (the dice Tool dropped). Per turn it picks
// gated unless the latest user utterance shows dice intent ([needsDice]) — so a
// plain conversational turn never declares dice and is structurally a single LLM
// round, removing the empty tool-call round before first audio (latency
// investigation baseline finding #4). If the grants never included dice the two
// loops are identical and the pick is a no-op.
type Engine struct {
	full  *tool.Loop // every granted Tool declared
	gated *tool.Loop // grants minus the dice Tool
}

// loopFor selects the loop for this turn: the full grants when the utterance
// plausibly needs dice, the dice-less grants otherwise.
func (e *Engine) loopFor(messages []llm.Message) *tool.Loop {
	if needsDice(messages) {
		return e.full
	}
	return e.gated
}

// EngineOption configures a [NewEngine]. The only option today is
// [WithMetrics], which opts the per-round LLM instrumentation in; without it
// the Engine records nothing (the keyless default).
type EngineOption func(*engineConfig)

// engineConfig collects the optional [NewEngine] settings before the adapter is
// built. The zero value is the no-op recorder + empty provider label.
type engineConfig struct {
	rec      observe.StageRecorder
	provName observe.Provider
}

// WithMetrics injects the A3 per-round instrumentation: rec receives one
// [observe.StageRecorder.LLMRound] span per [llm.Provider.Complete] inside the
// loop (round_index/had_tool_call) plus a provider-call counter, labelled with
// provName (the bounded provider enum for the wired [llm.Provider]). A nil rec
// leaves the no-op default in place. Benchmarks inject their own recorder to
// capture per-round numbers (C1); the live binary injects the Prometheus
// adapter (A2).
func WithMetrics(rec observe.StageRecorder, provName observe.Provider) EngineOption {
	return func(c *engineConfig) {
		if rec != nil {
			c.rec = rec
		}
		c.provName = provName
	}
}

// NewEngine builds an [Engine] over a streaming [llm.Provider] and the Agent's
// tool grants. model/maxTokens are the per-Agent LLM config used for every
// generation step. provider and grants must be non-nil ([tool.NewLoop] panics
// otherwise) — they are wiring requirements. maxRounds caps tool-call rounds;
// zero uses [tool.DefaultMaxRounds]. Pass [WithMetrics] to enable the A3 per-
// round instrumentation; without it the adapter records nothing.
func NewEngine(provider llm.Provider, grants *tool.GrantSet, model string, maxTokens, maxRounds int, opts ...EngineOption) *Engine {
	cfg := engineConfig{rec: observe.Discard{}}
	for _, o := range opts {
		o(&cfg)
	}
	adapter := providerAdapter{
		provider:  provider,
		model:     model,
		maxTokens: maxTokens,
		rec:       cfg.rec,
		provName:  cfg.provName,
	}
	newLoop := func(g *tool.GrantSet) *tool.Loop {
		l := tool.NewLoop(adapter, g)
		l.MaxRounds = maxRounds
		return l
	}
	// Two loops over the same adapter: full grants, and grants with dice dropped.
	// Per turn (Generate/GenerateStream) the dice gate picks between them so a
	// non-dice utterance never declares the dice Tool — one round, not two.
	return &Engine{
		full:  newLoop(grants),
		gated: newLoop(grants.Without(diceToolName)),
	}
}

// Generate implements [agent.Engine]. It converts the assembled Hot Context to
// [tool.Message]s and runs the loop to its final text. It installs a fresh
// per-turn round counter into ctx so the adapter's LLMRound spans index from 0
// for this turn and never share state with a concurrent turn (barge-in).
func (e *Engine) Generate(ctx context.Context, messages []llm.Message) (string, error) {
	return e.loopFor(messages).Run(withRoundCounter(ctx), toToolMessages(messages))
}

// GenerateStream implements [agent.StreamingEngine] (B1): it runs the same
// tool-use loop but streams the final answer's prose deltas to onText as they
// arrive, so the voice loop can segment and dispatch sentences before the
// completion finishes. It installs the same per-turn round counter as
// [Engine.Generate], so the A3 LLMRound / provider-call metrics are recorded
// identically on the streaming production path. Returns the full final text.
func (e *Engine) GenerateStream(ctx context.Context, messages []llm.Message, onText func(delta string) error) (string, error) {
	return e.loopFor(messages).RunStream(withRoundCounter(ctx), toToolMessages(messages), onText)
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
