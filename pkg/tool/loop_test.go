package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// scriptedProvider is a deterministic stand-in for an LLM [Provider]: it
// replays a pre-scripted sequence of [AssistantMessage]s, one per
// [Provider.Generate] call. It is the framework's cassette per ADR-0021's
// intent — pinning the tool-call routing without a live model. The real
// YAML-cassette-backed provider lands at integration with task #2's LLM
// adapter.
//
// Each step optionally asserts on the conversation it was handed (e.g. that the
// prior tool result was fed back) and on the declared tools (grant-stripping).
type scriptedProvider struct {
	t     *testing.T
	steps []scriptStep
	calls int
	// seen captures, per call, the messages and decls the loop passed in, so
	// tests can assert the loop fed results back correctly.
	seenMessages [][]Message
	seenDecls    [][]Decl
}

type scriptStep struct {
	reply AssistantMessage
	err   error
}

func (p *scriptedProvider) Generate(_ context.Context, messages []Message, tools []Decl) (AssistantMessage, error) {
	p.seenMessages = append(p.seenMessages, messages)
	p.seenDecls = append(p.seenDecls, tools)
	if p.calls >= len(p.steps) {
		p.t.Fatalf("scriptedProvider: unexpected Generate call %d (only %d scripted)", p.calls+1, len(p.steps))
	}
	step := p.steps[p.calls]
	p.calls++
	return step.reply, step.err
}

// diceLoop builds a Loop wired to a dice-granting GrantSet and the given
// provider. The registry also holds an ungranted "secret" tool so tests can
// assert it is never declared.
func diceLoop(t *testing.T, p Provider) *Loop {
	t.Helper()
	r := NewRegistry()
	r.MustRegister(NewDiceWithRand(fixedRand()))
	r.MustRegister(stubTool{name: "secret", readOnly: true})
	gs := NewGrantSet(r, Grant{ToolName: "dice"})
	return NewLoop(p, gs)
}

func TestLoopExecutesToolAndFeedsResultBack(t *testing.T) {
	p := &scriptedProvider{
		t: t,
		steps: []scriptStep{
			// Round 1: model calls dice.
			{reply: AssistantMessage{ToolCalls: []ToolCall{
				{ID: "call-1", Name: "dice", Args: json.RawMessage(`{"count":1,"sides":20}`)},
			}}},
			// Round 2 (after result fed back): model speaks final text.
			{reply: AssistantMessage{Text: "You rolled it."}},
		},
	}
	loop := diceLoop(t, p)

	final, err := loop.Run(context.Background(), []Message{
		{Role: RoleSystem, Text: "You are a butler."},
		{Role: RoleUser, Text: "roll a d20"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final != "You rolled it." {
		t.Errorf("final text = %q", final)
	}
	if p.calls != 2 {
		t.Fatalf("provider called %d times, want 2", p.calls)
	}

	// The second Generate must have seen: original 2 + assistant tool_call +
	// tool-role result = 4 messages, with the dice result keyed to call-1.
	second := p.seenMessages[1]
	if len(second) != 4 {
		t.Fatalf("second Generate saw %d messages, want 4: %+v", len(second), second)
	}
	if second[2].Role != RoleAssistant || len(second[2].ToolCalls) != 1 {
		t.Errorf("3rd message should be the assistant tool_call turn: %+v", second[2])
	}
	tr := second[3]
	if tr.Role != RoleTool || len(tr.ToolResults) != 1 {
		t.Fatalf("4th message should be the tool-role result: %+v", tr)
	}
	res := tr.ToolResults[0]
	if res.CallID != "call-1" {
		t.Errorf("tool result CallID = %q, want call-1", res.CallID)
	}
	if res.IsError {
		t.Errorf("dice result marked as error: %q", res.Content)
	}
	if !strings.HasPrefix(res.Content, "Rolled 1d20:") {
		t.Errorf("dice result content = %q", res.Content)
	}
}

func TestLoopOnlyDeclaresGrantedTools(t *testing.T) {
	p := &scriptedProvider{t: t, steps: []scriptStep{{reply: AssistantMessage{Text: "hi"}}}}
	loop := diceLoop(t, p)
	if _, err := loop.Run(context.Background(), []Message{{Role: RoleUser, Text: "hi"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	decls := p.seenDecls[0]
	if len(decls) != 1 || decls[0].Name != "dice" {
		t.Fatalf("declared tools = %+v; only the granted dice should be visible (ADR-0029)", decls)
	}
}

func TestLoopFeedsHandlerErrorBackNotAbort(t *testing.T) {
	p := &scriptedProvider{
		t: t,
		steps: []scriptStep{
			// Bad args → dice handler errors.
			{reply: AssistantMessage{ToolCalls: []ToolCall{
				{ID: "c1", Name: "dice", Args: json.RawMessage(`{"count":0,"sides":6}`)},
			}}},
			{reply: AssistantMessage{Text: "recovered"}},
		},
	}
	loop := diceLoop(t, p)
	final, err := loop.Run(context.Background(), []Message{{Role: RoleUser, Text: "roll"}})
	if err != nil {
		t.Fatalf("a handler error must not abort the loop: %v", err)
	}
	if final != "recovered" {
		t.Errorf("final = %q", final)
	}
	res := p.seenMessages[1][2].ToolResults[0]
	if !res.IsError {
		t.Error("handler error should be fed back as an error result")
	}
}

func TestLoopRejectsUngrantedToolName(t *testing.T) {
	// Model hallucinates a call to a registered-but-ungranted tool.
	p := &scriptedProvider{
		t: t,
		steps: []scriptStep{
			{reply: AssistantMessage{ToolCalls: []ToolCall{
				{ID: "c1", Name: "secret", Args: json.RawMessage(`{}`)},
			}}},
			{reply: AssistantMessage{Text: "ok"}},
		},
	}
	loop := diceLoop(t, p)
	if _, err := loop.Run(context.Background(), []Message{{Role: RoleUser, Text: "x"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	res := p.seenMessages[1][2].ToolResults[0]
	if !res.IsError || !strings.Contains(res.Content, "not available") {
		t.Errorf("ungranted tool call should yield a not-available error result, got %+v", res)
	}
}

func TestLoopRefusesSideEffectingTool(t *testing.T) {
	// A non-read-only tool reaching the inline path must be refused (ADR-0030).
	r := NewRegistry()
	r.MustRegister(stubTool{name: "writer", readOnly: false})
	gs := NewGrantSet(r, Grant{ToolName: "writer"})
	p := &scriptedProvider{
		t: t,
		steps: []scriptStep{
			{reply: AssistantMessage{ToolCalls: []ToolCall{
				{ID: "w1", Name: "writer", Args: json.RawMessage(`{}`)},
			}}},
			{reply: AssistantMessage{Text: "done"}},
		},
	}
	loop := NewLoop(p, gs)
	if _, err := loop.Run(context.Background(), []Message{{Role: RoleUser, Text: "write"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	res := p.seenMessages[1][2].ToolResults[0]
	if !res.IsError || !strings.Contains(res.Content, "read-only") {
		t.Errorf("side-effecting tool should be refused inline, got %+v", res)
	}
}

func TestLoopProviderErrorAborts(t *testing.T) {
	boom := errors.New("provider down")
	p := &scriptedProvider{t: t, steps: []scriptStep{{err: boom}}}
	loop := diceLoop(t, p)
	if _, err := loop.Run(context.Background(), []Message{{Role: RoleUser, Text: "x"}}); !errors.Is(err, boom) {
		t.Errorf("provider error should propagate, got %v", err)
	}
}

func TestLoopMaxRoundsGuard(t *testing.T) {
	// Provider that always calls dice, never stops.
	steps := make([]scriptStep, 20)
	for i := range steps {
		steps[i] = scriptStep{reply: AssistantMessage{ToolCalls: []ToolCall{
			{ID: "c", Name: "dice", Args: json.RawMessage(`{"count":1,"sides":6}`)},
		}}}
	}
	p := &scriptedProvider{t: t, steps: steps}
	loop := diceLoop(t, p)
	loop.MaxRounds = 3
	_, err := loop.Run(context.Background(), []Message{{Role: RoleUser, Text: "x"}})
	if !errors.Is(err, ErrMaxRoundsExceeded) {
		t.Errorf("runaway loop should hit ErrMaxRoundsExceeded, got %v", err)
	}
}

func TestLoopCanceledContextStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &scriptedProvider{t: t, steps: []scriptStep{{reply: AssistantMessage{Text: "unreached"}}}}
	loop := diceLoop(t, p)
	if _, err := loop.Run(ctx, []Message{{Role: RoleUser, Text: "x"}}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled context should stop the loop before Generate, got %v", err)
	}
	if p.calls != 0 {
		t.Errorf("provider should not be called under a pre-canceled context, calls=%d", p.calls)
	}
}

func TestLoopNoToolsImmediateAnswer(t *testing.T) {
	p := &scriptedProvider{t: t, steps: []scriptStep{{reply: AssistantMessage{Text: "just talking"}}}}
	loop := diceLoop(t, p)
	final, err := loop.Run(context.Background(), []Message{{Role: RoleUser, Text: "hi"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final != "just talking" {
		t.Errorf("final = %q", final)
	}
	if p.calls != 1 {
		t.Errorf("a no-tool answer needs exactly 1 Generate, got %d", p.calls)
	}
}

func TestLoopDoesNotMutateCallerMessages(t *testing.T) {
	p := &scriptedProvider{
		t: t,
		steps: []scriptStep{
			{reply: AssistantMessage{ToolCalls: []ToolCall{
				{ID: "c1", Name: "dice", Args: json.RawMessage(`{"count":1,"sides":6}`)},
			}}},
			{reply: AssistantMessage{Text: "done"}},
		},
	}
	loop := diceLoop(t, p)
	in := []Message{{Role: RoleUser, Text: "roll"}}
	if _, err := loop.Run(context.Background(), in); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(in) != 1 {
		t.Errorf("Run mutated the caller's message slice: now %d messages", len(in))
	}
}
