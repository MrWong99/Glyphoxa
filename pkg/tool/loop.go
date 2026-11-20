package tool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// DefaultMaxRounds caps how many tool-call rounds [Loop.Run] will execute
// before giving up, guarding against a misbehaving Provider that emits
// tool_calls forever. A round is one Generate + execute cycle; the final
// text-only Generate does not count against it.
const DefaultMaxRounds = 8

// ErrMaxRoundsExceeded is returned by [Loop.Run] when the forced final-answer
// round produced no prose — the degrade path's hard stop, not its first resort,
// reached from either trigger: the Provider kept emitting tool_calls past
// [Loop.MaxRounds], or the no-progress short-circuit fired after
// [noProgressRounds] consecutive all-error rounds. The wrapping error names the
// actual trigger ("(N rounds)" vs "(no-progress after N all-error rounds)") so
// logs point at the right failure; errors.Is matches both.
var ErrMaxRoundsExceeded = errors.New("tool: max tool-call rounds exceeded")

// noProgressRounds is the no-progress short-circuit bound: after this many
// CONSECUTIVE rounds whose executed ToolResults were ALL errors (a model
// grinding on an unavailable/failing tool — the live 8-round NPC silence), the
// loop stops burning budget and jumps straight to the final-answer round.
const noProgressRounds = 2

// budgetExhaustedNote is the error-ToolResult content fed back for each tool
// call of the round that exhausts [Loop.MaxRounds]: the calls are refused, and
// the model is told to answer in prose on the final-answer round that follows.
const budgetExhaustedNote = "tool budget exhausted; answer in prose without calling tools"

// finalAnswerRoundKey marks the ctx of the loop's ONE forced final-answer
// generation (see [IsFinalAnswerRound]).
type finalAnswerRoundKey struct{}

// withFinalAnswerRound returns ctx carrying the final-answer-round marker for
// the single generation [Loop.finalAnswer] issues; every regular round runs
// under the unmarked parent ctx.
func withFinalAnswerRound(ctx context.Context) context.Context {
	return context.WithValue(ctx, finalAnswerRoundKey{}, true)
}

// IsFinalAnswerRound reports whether ctx carries the loop's final-answer-round
// marker: the ONE forced tool-less generation [Loop.Run]/[Loop.RunStream] issue
// when the round budget is exhausted (or the no-progress short-circuit fires)
// with the model still emitting tool calls. It is the seam the wiring bridge
// (agenttool) reads to send tool_choice none while keeping the Tools DECLARED —
// the conversation holds prior tool_call/tool messages, and stripping the
// declarations risks a provider 400 on the dangling references (#420/#427).
func IsFinalAnswerRound(ctx context.Context) bool {
	marked, _ := ctx.Value(finalAnswerRoundKey{}).(bool)
	return marked
}

// Loop is the generic tool-use loop (ADR-0028): it drives an LLM [Provider]
// through tool calls — Generate → tool_call → execute → feed the tool-role
// result back → Generate again — until the model returns final text. It is the
// reusable building block, identical for one Tool or fifty and independent of
// any specific Tool or of the voice orchestrator; the Agent loop (task #2)
// assembles the prompt and calls [Loop.Run].
//
// Least-privilege (ADR-0029) and side-effect timing (ADR-0030) are both
// enforced here: only Tools the Agent is granted are declared and executable,
// and only read-only — or [ProposalMediated] (ADR-0052) — Tools run inline. A
// non-read-only Tool that is not proposal-mediated is refused because v1.0 does
// not build the deferred-to-turn-commit path.
type Loop struct {
	provider Provider
	grants   *GrantSet

	// MaxRounds caps tool-call rounds; zero means [DefaultMaxRounds].
	MaxRounds int

	// OnPseudoCall fires once per pseudo-XML tool call (issue #410) found in an
	// assistant message's text — the malformed `<function=…>…</function>` syntax
	// some models emit as plain content instead of a real tool_call. recovered is
	// true when the call parsed, named a granted Tool, AND the round still had
	// budget (it will run as a real round); false when it was stripped without
	// executing — ungranted, unparseable, ineligible, or found on a round whose
	// calls are budget-refused / dropped (the over-budget and final-answer
	// rounds), where nothing can run. nil is a no-op. It is the observability seam kept OUT of this
	// vendor/metric-agnostic package (ADR-0028): the wiring layer supplies a
	// callback that increments a counter. ctx is the turn's context.Context (the
	// same one Run/RunStream execute under), so a wiring-layer callback can also
	// reach any per-turn value it stashed there — e.g. #399's "dice actually
	// called" recorder must learn about a RECOVERED pseudo-dice call, which never
	// surfaced as a provider-native ToolCall.
	OnPseudoCall func(ctx context.Context, name string, recovered bool)

	// OnToolResult fires once per executed tool call with the [ToolResult] fed
	// back to the model: name is the Tool's name, content the result text, isErr
	// whether it is an error result. nil is a no-op. Like OnPseudoCall it is a
	// wiring seam kept OUT of this vendor-agnostic package (ADR-0028): the
	// agenttool bridge uses it to record the dice Tool's ACTUAL result so the
	// invented-roll guard can verify a regenerated narration against what was
	// really rolled (#438).
	OnToolResult func(ctx context.Context, name, content string, isErr bool)
}

// NewLoop builds a Loop over provider and the Agent's grants. Both must be
// non-nil; passing nil for either panics — they are wiring requirements, not
// runtime conditions.
func NewLoop(provider Provider, grants *GrantSet) *Loop {
	if provider == nil {
		panic("tool: NewLoop: nil provider")
	}
	if grants == nil {
		panic("tool: NewLoop: nil grants")
	}
	return &Loop{provider: provider, grants: grants}
}

// StreamingProvider is the optional streaming extension of [Provider]: a
// provider that implements it can forward the assistant's prose deltas to onText
// as they arrive, while still returning the same complete [AssistantMessage]
// Generate would. [Loop.RunStream] uses it to stream the final answer round to
// TTS (B1) without changing the non-streaming [Provider] contract every existing
// caller relies on (ADR-0028).
//
// GenerateStream must call onText in order on the calling goroutine for each
// prose delta; an error onText returns (a downstream barge-in cancel) aborts the
// completion promptly and is returned. Tool-call arguments are NOT forwarded to
// onText — only spoken prose.
type StreamingProvider interface {
	Provider
	GenerateStream(ctx context.Context, messages []Message, tools []Decl, onText func(delta string) error) (AssistantMessage, error)
}

// RunStream is the streaming counterpart of [Loop.Run] (B1): it drives the same
// tool-use rounds, but when the provider implements [StreamingProvider] it
// forwards the assistant's prose deltas to onText as they stream, so the caller
// can segment and dispatch sentences before the completion finishes. It returns
// the model's final text, identical to [Loop.Run].
//
// onText receives prose deltas from every round in order. Because the loop
// cannot know in advance whether a round will end in a tool call (that is only
// certain at the end of the round), a round's prose is forwarded live; for the
// granted dice Tool the model emits the call with no prose preamble, so in
// practice only the final answer's prose is spoken. A round that emits a
// COMPLETE sentence before its tool call would have that sentence forwarded —
// the caller's sentence splitter only emits on a terminator, so partial
// preambles are never spoken; a fully-terminated preamble is the documented
// residual. If the provider does not implement [StreamingProvider], RunStream
// falls back to [Loop.Run] and forwards the whole final text once.
//
// ctx governs generation and tool execution exactly as [Loop.Run]; cancelling it
// (barge-in) aborts the in-flight generation and the loop.
func (l *Loop) RunStream(ctx context.Context, messages []Message, onText func(delta string) error) (string, error) {
	streamer, ok := l.provider.(StreamingProvider)
	if !ok {
		// Non-streaming provider: run to completion, then forward the whole answer.
		full, err := l.Run(ctx, messages)
		if err != nil {
			return "", err
		}
		if onText != nil && full != "" {
			if err := onText(full); err != nil {
				return "", err
			}
		}
		return full, nil
	}

	maxRounds := l.MaxRounds
	if maxRounds <= 0 {
		maxRounds = DefaultMaxRounds
	}

	convo := make([]Message, len(messages), len(messages)+2*maxRounds)
	copy(convo, messages)

	decls := l.grants.Declarations()

	failedRounds := 0 // consecutive all-error rounds (no-progress tracker)
	for round := 0; ; round++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		// Wrap onText in a streamScrubber so pseudo-XML tool-call syntax (issue
		// #410) never reaches TTS live. Extraction/recovery is driven off the
		// accumulated asst.Text below (single source of truth) — the scrubber only
		// suppresses. A nil onText streams nothing, so no scrubber is needed.
		var sc *streamScrubber
		streamText := onText
		if onText != nil {
			sc = &streamScrubber{out: onText}
			streamText = sc.Write
		}

		asst, err := streamer.GenerateStream(ctx, convo, decls, streamText)
		if err != nil {
			return "", fmt.Errorf("tool: provider generate stream (round %d): %w", round, err)
		}
		if sc != nil {
			if err := sc.Flush(); err != nil {
				return "", err
			}
		}

		l.recoverPseudoCalls(ctx, round, maxRounds, &asst)

		if len(asst.ToolCalls) == 0 {
			return asst.Text, nil
		}

		if round >= maxRounds {
			convo = appendBudgetExhausted(convo, asst)
			return l.finalAnswer(ctx, round+1, maxRounds, 0, convo, decls, streamer, onText)
		}

		convo = append(convo, Message{
			Role:      RoleAssistant,
			Text:      asst.Text,
			ToolCalls: asst.ToolCalls,
		})

		results := make([]ToolResult, len(asst.ToolCalls))
		allErrors := true
		for i, call := range asst.ToolCalls {
			results[i] = l.execute(ctx, call)
			l.fireToolResult(ctx, call.Name, results[i])
			if !results[i].IsError {
				allErrors = false
			}
		}
		convo = append(convo, Message{Role: RoleTool, ToolResults: results})

		if !allErrors {
			failedRounds = 0
			continue
		}
		if failedRounds++; failedRounds >= noProgressRounds {
			l.warnNoProgress(failedRounds)
			return l.finalAnswer(ctx, round+1, maxRounds, failedRounds, convo, decls, streamer, onText)
		}
	}
}

// Run drives the conversation to completion and returns the model's final text.
// messages is the prompt the Agent loop assembled (system/user/...); Run
// appends the assistant tool_call turns and the tool-role result turns as it
// goes, leaving the caller's slice untouched.
//
// On each round Run declares only the granted Tools (grant-stripping), calls
// [Provider.Generate], and if the model emitted tool_calls, executes each and
// feeds the results back as one tool-role [Message] before the next Generate.
// When Generate returns no tool_calls, its Text is the answer.
//
// ctx governs Generate and every [Tool.Execute]; cancelling it (barge-in) tears
// down an in-flight call. A Provider error aborts the loop. A tool execution
// error does not abort: it is fed back to the model as an error [ToolResult] so
// the model can recover — the only hard stops are ctx cancellation, a Provider
// error, and [ErrMaxRoundsExceeded].
//
// Degrade path (the tool-budget silence fix): a model that keeps tool-calling
// to the round budget — or that burns [noProgressRounds] consecutive rounds
// whose executed results were ALL errors — no longer fails the turn outright.
// The loop forces ONE extra, marked generation (see [IsFinalAnswerRound]) whose
// prose is the answer; only an EMPTY final answer returns ErrMaxRoundsExceeded.
func (l *Loop) Run(ctx context.Context, messages []Message) (string, error) {
	maxRounds := l.MaxRounds
	if maxRounds <= 0 {
		maxRounds = DefaultMaxRounds
	}

	// Copy so we never mutate the caller's slice as we append turns.
	convo := make([]Message, len(messages), len(messages)+2*maxRounds)
	copy(convo, messages)

	decls := l.grants.Declarations()

	failedRounds := 0 // consecutive all-error rounds (no-progress tracker)
	for round := 0; ; round++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		asst, err := l.provider.Generate(ctx, convo, decls)
		if err != nil {
			return "", fmt.Errorf("tool: provider generate (round %d): %w", round, err)
		}

		// Scrub + recover any pseudo-XML tool calls the model emitted as text
		// (issue #410): clean the spoken text and promote parseable granted calls
		// to real ToolCalls before the tool-round decision below.
		l.recoverPseudoCalls(ctx, round, maxRounds, &asst)

		if len(asst.ToolCalls) == 0 {
			return asst.Text, nil
		}

		if round >= maxRounds {
			convo = appendBudgetExhausted(convo, asst)
			return l.finalAnswer(ctx, round+1, maxRounds, 0, convo, decls, nil, nil)
		}

		// Record the assistant's tool_call turn, then the tool-role results.
		convo = append(convo, Message{
			Role:      RoleAssistant,
			Text:      asst.Text,
			ToolCalls: asst.ToolCalls,
		})

		results := make([]ToolResult, len(asst.ToolCalls))
		allErrors := true
		for i, call := range asst.ToolCalls {
			results[i] = l.execute(ctx, call)
			l.fireToolResult(ctx, call.Name, results[i])
			if !results[i].IsError {
				allErrors = false
			}
		}
		convo = append(convo, Message{Role: RoleTool, ToolResults: results})

		if !allErrors {
			failedRounds = 0
			continue
		}
		if failedRounds++; failedRounds >= noProgressRounds {
			l.warnNoProgress(failedRounds)
			return l.finalAnswer(ctx, round+1, maxRounds, failedRounds, convo, decls, nil, nil)
		}
	}
}

// warnNoProgress logs the no-progress short-circuit: without it the burned
// rounds left ZERO log lines (the live 8-round silent turns).
func (l *Loop) warnNoProgress(failedRounds int) {
	slog.Warn("tool: consecutive all-error tool rounds; forcing a tool-less final answer",
		"rounds", failedRounds)
}

// appendBudgetExhausted records the over-budget assistant tool-call turn plus
// one [budgetExhaustedNote] error result per call — the calls are refused, NOT
// executed, so [Loop.OnToolResult] does not fire for them. The returned convo
// is what the final-answer round generates under.
func appendBudgetExhausted(convo []Message, asst AssistantMessage) []Message {
	convo = append(convo, Message{
		Role:      RoleAssistant,
		Text:      asst.Text,
		ToolCalls: asst.ToolCalls,
	})
	results := make([]ToolResult, len(asst.ToolCalls))
	for i, call := range asst.ToolCalls {
		results[i] = errResult(call.ID, budgetExhaustedNote)
	}
	return append(convo, Message{Role: RoleTool, ToolResults: results})
}

// finalAnswer issues the loop's ONE forced final-answer generation: the ctx is
// marked ([IsFinalAnswerRound]) so the wiring layer can disarm tool_choice while
// keeping the Tools declared, and any tool calls the model STILL emits are
// dropped un-executed in favour of its prose. Only an empty final answer
// returns [ErrMaxRoundsExceeded] — the pre-degrade hard stop, worded by the
// trigger that got here: failedRounds > 0 is the no-progress short-circuit
// (that many consecutive all-error rounds), 0 is round-budget exhaustion —
// so the surfaced error never claims the full budget was burned when the loop
// stopped after 2 rounds. streamer/onText select the streaming path
// ([Loop.RunStream]); both nil is the batch path.
func (l *Loop) finalAnswer(ctx context.Context, round, maxRounds, failedRounds int, convo []Message, decls []Decl, streamer StreamingProvider, onText func(delta string) error) (string, error) {
	ctx = withFinalAnswerRound(ctx)

	var asst AssistantMessage
	var err error
	if streamer != nil {
		// Same scrubber discipline as the regular streaming rounds: pseudo-XML
		// tool-call syntax never reaches TTS live.
		var sc *streamScrubber
		streamText := onText
		if onText != nil {
			sc = &streamScrubber{out: onText}
			streamText = sc.Write
		}
		asst, err = streamer.GenerateStream(ctx, convo, decls, streamText)
		if err == nil && sc != nil {
			err = sc.Flush()
		}
	} else {
		asst, err = l.provider.Generate(ctx, convo, decls)
	}
	if err != nil {
		return "", fmt.Errorf("tool: provider generate (final-answer round %d): %w", round, err)
	}

	// Strip-only pseudo-XML scrub: nothing executes on this round, so a granted
	// pseudo-call is NOT promoted (recovered=false keeps the metric honest —
	// OnPseudoCall's recovered=true promises the call will actually run).
	if clean, matches := ExtractPseudoCalls(asst.Text); len(matches) > 0 {
		asst.Text = clean
		for _, m := range matches {
			l.firePseudoCall(ctx, m.Name, false)
			slog.Warn("tool: stripped pseudo-XML tool call from the final-answer round",
				"tool", m.Name, "recovered", false)
		}
	}

	if len(asst.ToolCalls) > 0 {
		slog.Warn("tool: final-answer round still emitted tool calls; dropping them for its prose",
			"calls", len(asst.ToolCalls))
	}
	if strings.TrimSpace(asst.Text) == "" {
		if failedRounds > 0 {
			return "", fmt.Errorf("%w (no-progress after %d all-error rounds)", ErrMaxRoundsExceeded, failedRounds)
		}
		return "", fmt.Errorf("%w (%d rounds)", ErrMaxRoundsExceeded, maxRounds)
	}
	return asst.Text, nil
}

// execute runs one tool_call under the Agent's grants and returns the
// [ToolResult] to feed back. Every failure mode — ungranted/unknown Tool, a
// non-read-only Tool reaching the inline path, ctx cancellation, or a handler
// error — becomes an error ToolResult keyed to the call's ID rather than
// aborting the loop, except ctx cancellation which the caller also observes via
// ctx.Err() on the next round. The grant config is resolved here and handed to
// [Tool.Execute]; scope narrowing is the handler's job (ADR-0029).
func (l *Loop) execute(ctx context.Context, call ToolCall) ToolResult {
	t, config, ok := l.grants.resolve(call.Name)
	if !ok {
		// The model named a Tool it is not granted (or that is unregistered).
		// It should never happen since we only declare granted Tools, but a
		// hallucinated name is fed back as an error, not trusted — and logged:
		// a model grinding rounds on an unavailable name used to burn the whole
		// budget with ZERO log lines.
		slog.Warn("tool: model called an unavailable tool; feeding back an error result",
			"tool", call.Name)
		return errResult(call.ID, fmt.Sprintf("tool %q is not available", call.Name))
	}

	if !t.ReadOnly() {
		// ADR-0030: side-effecting Tools must defer to turn-commit; that path
		// is not built in v1.0, so refuse rather than mutate state inline from
		// a draft that may be discarded. ADR-0052 carves out ONE exception: a
		// proposal-mediated Tool (remember_knowledge) whose only effect is a
		// GM-reviewed proposal row is safe to run inline, so it falls through.
		if pm, ok := t.(ProposalMediated); !ok || !pm.ProposalMediated() {
			return errResult(call.ID, fmt.Sprintf(
				"tool %q is not read-only; side-effecting tools are not supported in this version", call.Name))
		}
	}

	out, err := t.Execute(ctx, call.Input, config)
	if err != nil {
		return errResult(call.ID, err.Error())
	}
	return ToolResult{CallID: call.ID, Content: out}
}

func errResult(callID, msg string) ToolResult {
	return ToolResult{CallID: callID, Content: msg, IsError: true}
}

// recoverPseudoCalls scrubs pseudo-XML tool-call syntax (issue #410) out of an
// assistant message's Text and, for each occurrence, either promotes it to a
// real [ToolCall] (parseable args AND a granted Tool) or drops it. It mutates
// asst in place: Text becomes the clean speech/transcript text, and any recovered
// call is appended to ToolCalls so the existing loop runs it as a REAL round —
// grant + ADR-0030 side-effect enforcement in [Loop.execute] apply unchanged. It
// fires [Loop.OnPseudoCall] and logs once per occurrence so provider flake stays
// visible (same observability family as #398).
//
// round seeds the synthetic ToolCall IDs ("pseudo-<round>-<i>") so a recovered
// call correlates to its tool-role result exactly like a provider-issued call.
//
// A round at or past maxRounds is strip-only: that round's tool calls are about
// to be budget-refused ([appendBudgetExhausted]), never executed, so promoting a
// pseudo-call there would break OnPseudoCall's recovered=true promise that the
// call will actually run — and would let a never-run Tool look "called" to the
// wiring layer's per-turn recorder (the #399 invented-roll leak). Same
// discipline as [Loop.finalAnswer]'s own strip-only scrub.
func (l *Loop) recoverPseudoCalls(ctx context.Context, round, maxRounds int, asst *AssistantMessage) {
	clean, matches := ExtractPseudoCalls(asst.Text)
	if len(matches) == 0 {
		return
	}
	asst.Text = clean
	for i, m := range matches {
		if round < maxRounds && m.Args != nil {
			// Recover only if the call will ACTUALLY execute: granted, registered,
			// and eligible for inline execution (read-only or proposal-mediated,
			// ADR-0030/0052). A granted-but-refused call is metered as NOT
			// recovered so the metric never overcounts recoveries execute rejects.
			if t, _, ok := l.grants.resolve(m.Name); ok && inlineEligible(t) {
				asst.ToolCalls = append(asst.ToolCalls, ToolCall{
					ID:    fmt.Sprintf("pseudo-%d-%d", round, i),
					Name:  m.Name,
					Input: m.Args,
				})
				l.firePseudoCall(ctx, m.Name, true)
				continue
			}
		}
		// Ungranted, unparseable, ineligible, or over-budget: strip-only. The
		// intent is lost but the leak is contained, and the occurrence is
		// logged + metered.
		l.firePseudoCall(ctx, m.Name, false)
		slog.Warn("tool: stripped un-recoverable pseudo-XML tool call from assistant text",
			"tool", m.Name, "recovered", false)
	}
}

// inlineEligible reports whether t may run inline during generation (ADR-0030):
// a read-only Tool, or a [ProposalMediated] one (ADR-0052). It mirrors the gate
// in [Loop.execute] so recovery and the OnPseudoCall recovered=true signal agree
// on which pseudo-calls will genuinely execute.
func inlineEligible(t Tool) bool {
	if t.ReadOnly() {
		return true
	}
	pm, ok := t.(ProposalMediated)
	return ok && pm.ProposalMediated()
}

func (l *Loop) firePseudoCall(ctx context.Context, name string, recovered bool) {
	if l.OnPseudoCall != nil {
		l.OnPseudoCall(ctx, name, recovered)
	}
}

func (l *Loop) fireToolResult(ctx context.Context, name string, r ToolResult) {
	if l.OnToolResult != nil {
		l.OnToolResult(ctx, name, r.Content, r.IsError)
	}
}
