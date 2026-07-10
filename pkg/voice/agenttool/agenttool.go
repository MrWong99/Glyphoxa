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
	"unicode/utf8"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/retry"
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

	// retry bounds transient-failure retries around the LLM START call (#124,
	// ADR-0044): a single 429/5xx or net.Error is retried with backoff before the
	// stream is drained, a non-retryable error fails fast, and a barge cutting ctx
	// aborts at once — bounded by the Replier's per-turn deadline. ONLY the start is
	// retried; a mid-stream failure is never retried (deltas may already be spoken).
	// Zero value is a valid retries-on policy; [WithRetry] threads the shared one.
	retry retry.Policy
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

	// Retry a transient START failure (429/5xx/net) with backoff before draining
	// any deltas (#124, ADR-0044); a non-retryable error fails fast and a barge
	// cutting ctx aborts at once, bounded by the per-turn deadline. Only the start
	// is retried — a mid-stream failure below is never re-driven (re-speak risk).
	// Metrics below fire on the final outcome only (#125), so a recovered retry
	// records one round span, not one per attempt.
	stream, err := retry.Do(ctx, a.retry, func(ctx context.Context) (<-chan llm.StreamEvent, error) {
		return a.provider.Complete(ctx, llm.Request{
			Model:     a.model,
			MaxTokens: a.maxTokens,
			Messages:  toLLMMessages(messages),
			Tools:     toLLMToolDefs(tools),
		})
	})
	if err != nil {
		// A start failure has no round span (no completion happened); attribute it to
		// the LLM stage via the shared [observe.CallOutcome] rule so LLM agrees with
		// STT/TTS: a barge-in cancel is OutcomeCanceled (NOT a vendor fault), a fired
		// deadline is OutcomeTimeout, anything else is OutcomeError. ProviderError is
		// bumped only on a fault, so a barge before first token does not inflate the
		// error ratio (#239 review).
		outcome := observe.CallOutcome(ctx, err)
		a.rec.ProviderCall(observe.StageLLM, a.provName, outcome)
		if outcome.IsFault() {
			a.rec.ProviderError(observe.StageLLM, a.provName)
		}
		return tool.AssistantMessage{}, err
	}

	var out tool.AssistantMessage
	var text []byte
	var done bool
	var streamErr error
	// usage/haveUsage stash the provider-reported token accounting from the additive
	// EventUsage (#127, ADR-0045). It rides a distinct event, not EventDone, and may
	// arrive before or after done — draining to close (as we do) captures it either
	// way.
	var usage llm.Usage
	var haveUsage bool
	for ev := range stream {
		switch ev.Type {
		case llm.EventText:
			text = append(text, ev.Text...)
			if onText != nil {
				if err := onText(ev.Text); err != nil {
					// Downstream cancel (barge-in): record the round we did and stop. A
					// barge records the provider-reported usage IF it already arrived, never
					// an estimate — a partial turn is not metered by guesswork (ADR-0045).
					out.Text = string(text)
					a.rec.LLMRound(a.provName, round, len(out.ToolCalls) > 0, time.Since(start))
					a.rec.ProviderCall(observe.StageLLM, a.provName, observe.OutcomeOK)
					a.recordReportedUsage(haveUsage, usage)
					return out, err
				}
			}
		case llm.EventToolCall:
			out.ToolCalls = append(out.ToolCalls, tool.ToolCall{
				ID:    ev.ToolCall.ID,
				Name:  ev.ToolCall.Name,
				Input: ev.ToolCall.Input,
			})
		case llm.EventUsage:
			usage, haveUsage = ev.Usage, true
		case llm.EventDone:
			done = true
		case llm.EventError:
			streamErr = errors.New(ev.Err)
		}
	}
	if streamErr != nil {
		// A mid-stream error records reported usage if it arrived, else nothing.
		a.recordReportedUsage(haveUsage, usage)
		return tool.AssistantMessage{}, streamErr
	}
	if !done {
		a.recordReportedUsage(haveUsage, usage)
		if err := ctx.Err(); err != nil {
			return tool.AssistantMessage{}, err
		}
		return tool.AssistantMessage{}, errors.New("agenttool: completion stream ended without done event (truncated response)")
	}
	out.Text = string(text)

	hadToolCall := len(out.ToolCalls) > 0
	a.rec.LLMRound(a.provName, round, hadToolCall, time.Since(start))
	a.rec.ProviderCall(observe.StageLLM, a.provName, observe.OutcomeOK)
	// A completed round meters its tokens: the provider-reported counts, or a
	// documented ceil(chars/4) estimate per direction when none was reported — never
	// zero (AC, ADR-0045). An atomic counter add; it never blocks or fails the turn.
	a.recordUsage(haveUsage, usage, messages, out.Text)
	return out, nil
}

// recordReportedUsage records provider-reported token usage if it arrived, else
// nothing — the error/barge rule (ADR-0045): a partial or failed turn is metered
// only by what the provider actually reported, never by a fabricated estimate.
func (a providerAdapter) recordReportedUsage(have bool, u llm.Usage) {
	if have {
		a.rec.LLMTokens(a.provName, a.model, u.InputTokens, u.OutputTokens)
	}
}

// recordUsage meters a completed round: the provider-reported counts, or a
// documented ceil(chars/4) estimate per direction over the sent conversation and
// the received text when the provider reported none — never zero (AC, ADR-0045).
// model rides only to the spend meter (ADR-0046); Prometheus drops it.
func (a providerAdapter) recordUsage(have bool, u llm.Usage, sent []tool.Message, received string) {
	if have {
		a.rec.LLMTokens(a.provName, a.model, u.InputTokens, u.OutputTokens)
		return
	}
	a.rec.LLMTokens(a.provName, a.model, estimateTokens(sentRunes(sent)), estimateTokens(utf8.RuneCountInString(received)))
}

// estimateTokens is the ceil(chars/4) per-direction token estimate (ADR-0045): the
// classic ~4-characters-per-token heuristic, integer ceil for a non-negative count.
func estimateTokens(runes int) int { return (runes + 3) / 4 }

// sentRunes sums the prose runes across the sent conversation — the input side of
// the estimate. It counts message text only (an approximation, as documented on the
// estimate path); tool-call arguments and results are not separately weighed.
func sentRunes(msgs []tool.Message) int {
	total := 0
	for _, m := range msgs {
		total += utf8.RuneCountInString(m.Text)
	}
	return total
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

	// agentID is this Agent's stable identity, stamped onto the turn ctx once per
	// Generate/GenerateStream via [tool.WithCaller] (S2, #296). A scope-narrowing
	// Tool handler (kg_query own_node) reads it back with [tool.CallerID] to scope
	// the read to THIS Agent's neighbourhood — the caller comes from here, never
	// the LLM's args, so the model cannot widen its scope. "" (a standalone bench
	// Agent, or a Persona with no persisted id) stamps an empty caller, which an
	// own-node handler treats as "no neighbourhood" rather than a wider fallback.
	agentID string

	// language is the gate language the dice keyword set is selected by (#226):
	// the Campaign Language, subtag-normalized via [gateLanguage] once at
	// construction ([WithLanguage]), so an arbitrary campaign string is parsed
	// once and the per-turn gate only does a cheap table lookup on the result.
	// The zero value "" selects the English keyword set ([needsDice] maps it to
	// "en") — the default when no Campaign Language is wired.
	language string

	// rec/provName carry the #125 full-turn instrumentation: one
	// [observe.StageRecorder.LLMTurn] span per Generate/GenerateStream (the whole
	// tool loop — all rounds + tool exec), distinct from the per-round LLMRound the
	// adapter records. Recorded on the success AND error path so a failed turn's
	// latency is still visible. Default no-ops (observe.Discard, empty provider
	// label), so the keyless path stays silent.
	rec      observe.StageRecorder
	provName observe.Provider
}

// loopFor selects the loop for this turn: the full grants when the utterance
// plausibly needs dice, the dice-less grants otherwise.
func (e *Engine) loopFor(messages []llm.Message) *tool.Loop {
	if needsDice(e.language, messages) {
		return e.full
	}
	return e.gated
}

// EngineOption configures a [NewEngine]: [WithMetrics] opts the per-round LLM
// instrumentation in, and [WithLanguage] selects the dice gate's keyword set.
// Without either, the Engine records nothing and gates dice in English (the
// keyless, pre-#226 default).
type EngineOption func(*engineConfig)

// engineConfig collects the optional [NewEngine] settings before the adapter is
// built. The zero value is the no-op recorder + empty provider label + the
// English dice gate ("" normalizes to "en" in [needsDice]).
type engineConfig struct {
	rec      observe.StageRecorder
	provName observe.Provider
	language string
	retry    retry.Policy
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

// WithRetry sets the [retry.Policy] wrapping the LLM start call inside the loop
// (#124): a transient 429/5xx or net.Error start-error is retried with backoff,
// a non-retryable error fails fast, and a barge cutting ctx aborts at once. Only
// the start is retried — never a mid-stream delta failure. Its injected Sleep/Rand
// keep cassette runs deterministic (ADR-0021). Without this option the zero-value
// policy applies (retries on with defaults).
func WithRetry(p retry.Policy) EngineOption {
	return func(c *engineConfig) { c.retry = p }
}

// WithLanguage selects the dice gate's keyword set by Campaign Language (#226):
// lang is subtag-normalized here via [gateLanguage] ("de-DE" → "de"; an unknown
// language degrades to "en", ADR-0024), so the arbitrary campaign string is
// parsed once and the per-turn gate only does a cheap table lookup. Without this
// option the gate defaults to the English keyword set.
func WithLanguage(lang string) EngineOption {
	return func(c *engineConfig) {
		c.language = gateLanguage(lang)
	}
}

// NewEngine builds an [Engine] over a streaming [llm.Provider] and the Agent's
// tool grants. model/maxTokens are the per-Agent LLM config used for every
// generation step. provider and grants must be non-nil ([tool.NewLoop] panics
// otherwise) — they are wiring requirements. maxRounds caps tool-call rounds;
// zero uses [tool.DefaultMaxRounds]. Pass [WithMetrics] to enable the A3 per-
// round instrumentation; without it the adapter records nothing.
func NewEngine(provider llm.Provider, grants *tool.GrantSet, agentID, model string, maxTokens, maxRounds int, opts ...EngineOption) *Engine {
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
		retry:     cfg.retry,
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
		full:     newLoop(grants),
		gated:    newLoop(grants.Without(diceToolName)),
		agentID:  agentID,
		language: cfg.language,
		rec:      cfg.rec,
		provName: cfg.provName,
	}
}

// Generate implements [agent.Engine]. It converts the assembled Hot Context to
// [tool.Message]s and runs the loop to its final text. It installs a fresh
// per-turn round counter into ctx so the adapter's LLMRound spans index from 0
// for this turn and never share state with a concurrent turn (barge-in).
func (e *Engine) Generate(ctx context.Context, messages []llm.Message) (string, error) {
	start := time.Now()
	// Stamp the caller identity once per turn (S2): a scope-narrowing Tool handler
	// resolves the Agent from ctx, never the LLM args.
	ctx = tool.WithCaller(ctx, e.agentID)
	out, err := e.loopFor(messages).Run(withRoundCounter(ctx), toToolMessages(messages))
	// #125: one full-turn span per Generate, spanning every round, recorded on the
	// success AND error path so a turn that fails mid-loop is still measured.
	e.rec.LLMTurn(e.provName, time.Since(start))
	return out, err
}

// GenerateStream implements [agent.StreamingEngine] (B1): it runs the same
// tool-use loop but streams the final answer's prose deltas to onText as they
// arrive, so the voice loop can segment and dispatch sentences before the
// completion finishes. It installs the same per-turn round counter as
// [Engine.Generate], so the A3 LLMRound / provider-call metrics are recorded
// identically on the streaming production path. Returns the full final text.
func (e *Engine) GenerateStream(ctx context.Context, messages []llm.Message, onText func(delta string) error) (string, error) {
	start := time.Now()
	// Stamp the caller identity once per turn (S2), identical to Generate.
	ctx = tool.WithCaller(ctx, e.agentID)
	out, err := e.loopFor(messages).RunStream(withRoundCounter(ctx), toToolMessages(messages), onText)
	// #125: the streaming production path records the same one-per-turn LLMTurn span
	// Generate does, on success and error alike.
	e.rec.LLMTurn(e.provName, time.Since(start))
	return out, err
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
