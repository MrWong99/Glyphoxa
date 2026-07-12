package tool

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// DefaultMaxRounds caps how many tool-call rounds [Loop.Run] will execute
// before giving up, guarding against a misbehaving Provider that emits
// tool_calls forever. A round is one Generate + execute cycle; the final
// text-only Generate does not count against it.
const DefaultMaxRounds = 8

// ErrMaxRoundsExceeded is returned by [Loop.Run] when the Provider keeps
// emitting tool_calls past [Loop.MaxRounds] without ever returning final text.
var ErrMaxRoundsExceeded = errors.New("tool: max tool-call rounds exceeded")

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
	// true when the call parsed and named a granted Tool (it will run as a real
	// round); false when it was stripped without executing (ungranted or
	// unparseable). nil is a no-op. It is the observability seam kept OUT of this
	// vendor/metric-agnostic package (ADR-0028): the wiring layer supplies a
	// callback that increments a counter. ctx is the turn's context.Context (the
	// same one Run/RunStream execute under), so a wiring-layer callback can also
	// reach any per-turn value it stashed there — e.g. #399's "dice actually
	// called" recorder must learn about a RECOVERED pseudo-dice call, which never
	// surfaced as a provider-native ToolCall.
	OnPseudoCall func(ctx context.Context, name string, recovered bool)
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

		l.recoverPseudoCalls(ctx, round, &asst)

		if len(asst.ToolCalls) == 0 {
			return asst.Text, nil
		}

		if round >= maxRounds {
			return "", fmt.Errorf("%w (%d rounds)", ErrMaxRoundsExceeded, maxRounds)
		}

		convo = append(convo, Message{
			Role:      RoleAssistant,
			Text:      asst.Text,
			ToolCalls: asst.ToolCalls,
		})

		results := make([]ToolResult, len(asst.ToolCalls))
		for i, call := range asst.ToolCalls {
			results[i] = l.execute(ctx, call)
		}
		convo = append(convo, Message{Role: RoleTool, ToolResults: results})
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
func (l *Loop) Run(ctx context.Context, messages []Message) (string, error) {
	maxRounds := l.MaxRounds
	if maxRounds <= 0 {
		maxRounds = DefaultMaxRounds
	}

	// Copy so we never mutate the caller's slice as we append turns.
	convo := make([]Message, len(messages), len(messages)+2*maxRounds)
	copy(convo, messages)

	decls := l.grants.Declarations()

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
		l.recoverPseudoCalls(ctx, round, &asst)

		if len(asst.ToolCalls) == 0 {
			return asst.Text, nil
		}

		if round >= maxRounds {
			return "", fmt.Errorf("%w (%d rounds)", ErrMaxRoundsExceeded, maxRounds)
		}

		// Record the assistant's tool_call turn, then the tool-role results.
		convo = append(convo, Message{
			Role:      RoleAssistant,
			Text:      asst.Text,
			ToolCalls: asst.ToolCalls,
		})

		results := make([]ToolResult, len(asst.ToolCalls))
		for i, call := range asst.ToolCalls {
			results[i] = l.execute(ctx, call)
		}
		convo = append(convo, Message{Role: RoleTool, ToolResults: results})
	}
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
		// hallucinated name is fed back as an error, not trusted.
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
func (l *Loop) recoverPseudoCalls(ctx context.Context, round int, asst *AssistantMessage) {
	clean, matches := ExtractPseudoCalls(asst.Text)
	if len(matches) == 0 {
		return
	}
	asst.Text = clean
	for i, m := range matches {
		if m.Args != nil {
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
		// Ungranted, unparseable, or ineligible: strip-only. The intent is lost
		// but the leak is contained, and the occurrence is logged + metered.
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
