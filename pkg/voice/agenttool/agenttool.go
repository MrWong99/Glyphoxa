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
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
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

// forcedChoiceKey is the context key under which [withForcedToolChoice] stores a
// one-shot tool-choice override the adapter applies to the FIRST completion of a
// turn. It is consumed exactly once (later rounds revert to auto), so a caller can
// force e.g. tool_choice "required" on the opening round without pinning every
// subsequent round.
type forcedChoiceKey struct{}

// forcedChoice is the one-shot holder behind [forcedChoiceKey]: the once gate makes
// [consumeForcedChoice] hand the override to the first caller only.
type forcedChoice struct {
	choice llm.ToolChoice
	mu     sync.Mutex
	done   bool
}

// withForcedToolChoice returns ctx carrying a one-shot tool-choice override the
// adapter applies to the FIRST [llm.Provider.Complete] of the turn (#399 consumes
// this to force a tool call on the opening round; #398 ships the seam). Later rounds
// revert to the default auto. Shipped now with adapter consumption wired but unused
// by [Engine.Generate] / [Engine.GenerateStream] themselves.
func withForcedToolChoice(ctx context.Context, choice llm.ToolChoice) context.Context {
	return context.WithValue(ctx, forcedChoiceKey{}, &forcedChoice{choice: choice})
}

// consumeForcedChoice returns the one-shot forced tool choice and true the first
// time it is called on a ctx carrying one, then (zero, false) forever after — so
// only the turn's first completion is forced. A ctx without an override yields
// (zero, false) immediately.
func consumeForcedChoice(ctx context.Context) (llm.ToolChoice, bool) {
	f, _ := ctx.Value(forcedChoiceKey{}).(*forcedChoice)
	if f == nil {
		return llm.ToolChoice{}, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.done {
		return llm.ToolChoice{}, false
	}
	f.done = true
	return f.choice, true
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

// attemptKind classifies how one [providerAdapter.attempt] ended, so [complete]
// records the round's final-outcome metrics (LLMRound / ProviderCall, ADR-0044) and
// meters usage (ADR-0045) in one place rather than duplicating the rules per branch.
type attemptKind int

const (
	kindSuccess   attemptKind = iota // clean EventDone
	kindBarge                        // onText returned an error mid-drain (downstream cancel)
	kindStartErr                     // Complete could not start (retry.Do exhausted / non-retryable)
	kindStreamErr                    // terminal EventError (may carry a tool-syntax class)
	kindTruncated                    // stream ended without EventDone, ctx still live
	kindCtxErr                       // stream ended without EventDone because ctx was cancelled
)

// attemptResult is one [providerAdapter.attempt]'s outcome, drained but not yet
// metered — [complete] inspects kind to record the final-outcome span/counter and
// the reported-or-estimate usage exactly once for the whole round.
type attemptResult struct {
	msg            tool.AssistantMessage
	forwardedDelta bool // a prose delta was forwarded to onText before the outcome
	haveUsage      bool
	usage          llm.Usage
	err            error // nil only on kindSuccess
	kind           attemptKind
}

// complete issues one logical round (one requested choice, with an in-turn retry
// and a tool-less fallback for the tool-syntax failure class) and records the A3
// per-round span + provider-call counter + usage on the FINAL outcome only
// (ADR-0044/0045). When onText is non-nil each [llm.EventText] delta is forwarded as
// it arrives (the streaming path); an error onText returns aborts the drain and
// propagates.
//
// Tool-syntax policy (#398, tool-armed rounds only): a round that fails with the
// provider's tool_use_failed class and has NOT yet forwarded any prose is retried
// once with the same tools+choice (no backoff); a second failure of the same class
// regenerates tool-less (tools stay DECLARED, tool_choice none — the conversation
// may hold prior tool_call/tool messages). A round that already forwarded a delta is
// never retried (ADR-0044 mid-stream rule), and a ctx cancellation between attempts
// aborts. Every malformed generation increments MalformedToolGen so provider flake
// stays visible even when the turn fully recovers.
func (a providerAdapter) complete(ctx context.Context, messages []tool.Message, tools []tool.Decl, onText func(delta string) error) (tool.AssistantMessage, error) {
	round := nextRound(ctx)
	start := time.Now()

	// The requested tool choice: a one-shot forced override on the turn's first
	// completion (#399 seam), else the default auto. Consumed here so later rounds
	// revert to auto.
	choice := a.requestedChoice(ctx)

	res := a.attempt(ctx, messages, tools, choice, onText)

	// Tool-syntax retry + tool-less fallback — tool-armed rounds only (a no-tool round
	// can never emit a tool_use_failed, and never retries per ADR-0044).
	if len(tools) > 0 {
		if a.isToolSyntax(res) {
			// Attempt 2: immediate retry, same tools + choice (no backoff).
			a.rec.MalformedToolGen(a.provName, observe.MalformedStreamError)
			if err := ctx.Err(); err != nil {
				return tool.AssistantMessage{}, err // barge between attempts aborts
			}
			res = a.attempt(ctx, messages, tools, choice, onText)

			if a.isToolSyntax(res) {
				// Attempt 3: regenerate tool-less. Tools stay declared; tool_choice none.
				a.rec.MalformedToolGen(a.provName, observe.MalformedStreamError)
				slog.Warn("agenttool: repeated tool_use_failed; regenerating tool-less",
					"provider", string(a.provName), "round", round)
				if err := ctx.Err(); err != nil {
					return tool.AssistantMessage{}, err
				}
				res = a.attempt(ctx, messages, tools, llm.ToolChoice{Mode: llm.ToolChoiceNone}, onText)
			}
		}
	}

	return a.recordOutcome(ctx, round, start, messages, res)
}

// isToolSyntax reports whether res failed with the provider's tool-syntax class AND
// had not yet forwarded a prose delta — the two conditions the #398 retry / fallback
// requires (a mid-stream failure after audio already went out is never re-driven,
// ADR-0044).
func (a providerAdapter) isToolSyntax(res attemptResult) bool {
	if res.forwardedDelta {
		return false
	}
	var tse *providererr.ToolSyntaxError
	return errors.As(res.err, &tse)
}

// requestedChoice returns the tool choice for this round: the one-shot forced
// override on the turn's first completion (#399 seam), consumed here, else the
// zero-value auto.
func (a providerAdapter) requestedChoice(ctx context.Context) llm.ToolChoice {
	if tc, ok := consumeForcedChoice(ctx); ok {
		return tc
	}
	return llm.ToolChoice{}
}

// attempt issues ONE streaming completion for the round and drains it, classifying
// the outcome without recording any final-outcome metric (its caller [complete]
// owns those, once per round). It still bounds a transient START failure (429/5xx/
// net) with the injected retry backoff (#124, ADR-0044): only the start is retried,
// never a mid-stream delta failure (re-speak risk). A terminal EventError carrying
// [llm.ErrClassToolSyntax] is surfaced as a typed [*providererr.ToolSyntaxError] so
// [complete] can drive the tool-syntax policy.
func (a providerAdapter) attempt(ctx context.Context, messages []tool.Message, tools []tool.Decl, choice llm.ToolChoice, onText func(delta string) error) attemptResult {
	stream, err := retry.Do(ctx, a.retry, func(ctx context.Context) (<-chan llm.StreamEvent, error) {
		return a.provider.Complete(ctx, llm.Request{
			Model:      a.model,
			MaxTokens:  a.maxTokens,
			Messages:   toLLMMessages(messages),
			Tools:      toLLMToolDefs(tools),
			ToolChoice: choice,
		})
	})
	if err != nil {
		return attemptResult{err: err, kind: kindStartErr}
	}

	var out tool.AssistantMessage
	var text []byte
	var done bool
	var streamErr error
	var forwarded bool
	// usage/haveUsage stash the provider-reported token accounting from the additive
	// EventUsage (#127, ADR-0045); draining to close captures it whether it arrives
	// before or after EventDone.
	var usage llm.Usage
	var haveUsage bool
	for ev := range stream {
		switch ev.Type {
		case llm.EventText:
			text = append(text, ev.Text...)
			if onText != nil {
				if err := onText(ev.Text); err != nil {
					out.Text = string(text)
					return attemptResult{msg: out, forwardedDelta: forwarded, haveUsage: haveUsage, usage: usage, err: err, kind: kindBarge}
				}
				forwarded = true
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
			// A tool_use_failed is surfaced as a typed ToolSyntaxError so complete can
			// retry/fallback; any other class stays a plain error (propagates).
			if ev.ErrClass == llm.ErrClassToolSyntax {
				streamErr = &providererr.ToolSyntaxError{Op: "agenttool.complete", Msg: ev.Err}
			} else {
				streamErr = errors.New(ev.Err)
			}
		}
	}
	if streamErr != nil {
		return attemptResult{forwardedDelta: forwarded, haveUsage: haveUsage, usage: usage, err: streamErr, kind: kindStreamErr}
	}
	if !done {
		if err := ctx.Err(); err != nil {
			return attemptResult{forwardedDelta: forwarded, haveUsage: haveUsage, usage: usage, err: err, kind: kindCtxErr}
		}
		return attemptResult{forwardedDelta: forwarded, haveUsage: haveUsage, usage: usage,
			err:  errors.New("agenttool: completion stream ended without done event (truncated response)"),
			kind: kindTruncated}
	}
	out.Text = string(text)
	return attemptResult{msg: out, forwardedDelta: forwarded, haveUsage: haveUsage, usage: usage, kind: kindSuccess}
}

// recordOutcome records the round's FINAL-outcome metrics (ADR-0044) and meters its
// usage (ADR-0045) from the resolved attempt, then returns the message/error the
// loop consumes. The per-kind rules preserve the pre-#398 behaviour exactly: a
// success or barge records one LLMRound + provider_call(ok); a start failure records
// provider_call with the shared [observe.CallOutcome] classification (a barge cancel
// is not a fault); a mid-stream / truncation failure records no provider_call
// (ADR-0044 does not meter mid-stream faults) but meters any reported usage.
func (a providerAdapter) recordOutcome(ctx context.Context, round int, start time.Time, messages []tool.Message, res attemptResult) (tool.AssistantMessage, error) {
	switch res.kind {
	case kindSuccess:
		a.rec.LLMRound(a.provName, round, len(res.msg.ToolCalls) > 0, time.Since(start))
		a.rec.ProviderCall(observe.StageLLM, a.provName, observe.OutcomeOK)
		a.recordUsage(res.haveUsage, res.usage, messages, res.msg.Text)
		return res.msg, nil

	case kindBarge:
		// The round produced output before a downstream cancel: record it, and meter
		// only the provider-reported usage if it already arrived (never an estimate).
		a.rec.LLMRound(a.provName, round, len(res.msg.ToolCalls) > 0, time.Since(start))
		a.rec.ProviderCall(observe.StageLLM, a.provName, observe.OutcomeOK)
		a.recordReportedUsage(res.haveUsage, res.usage)
		return res.msg, res.err

	case kindStartErr:
		// No completion happened; attribute to the LLM stage via the shared outcome
		// rule so a barge cancel is OutcomeCanceled (not a fault) while a vendor start
		// failure is OutcomeError (a fault), matching STT/TTS (#239 review).
		outcome := observe.CallOutcome(ctx, res.err)
		a.rec.ProviderCall(observe.StageLLM, a.provName, outcome)
		if outcome.IsFault() {
			a.rec.ProviderError(observe.StageLLM, a.provName)
		}
		return tool.AssistantMessage{}, res.err

	default: // kindStreamErr, kindTruncated, kindCtxErr
		// A mid-stream / truncation / cancel failure meters reported usage if it
		// arrived, else nothing (ADR-0045), and records no provider_call (ADR-0044).
		a.recordReportedUsage(res.haveUsage, res.usage)
		return tool.AssistantMessage{}, res.err
	}
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
		// #410: every pseudo-XML tool call the model emitted as plain text —
		// recovered or stripped — is a malformed generation. Count it on the same
		// counter family as #398 with the text_leak path so provider flake stays
		// visible. The scrub itself lives in pkg/tool (vendor/metric-agnostic,
		// ADR-0028); this callback is the observability wiring.
		l.OnPseudoCall = func(string, bool) {
			cfg.rec.MalformedToolGen(cfg.provName, observe.MalformedTextLeak)
		}
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
