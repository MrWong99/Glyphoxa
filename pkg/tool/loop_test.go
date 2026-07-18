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
	// tests can assert the loop fed results back correctly. seenFinal records
	// whether each call's ctx carried the final-answer-round marker.
	seenMessages [][]Message
	seenDecls    [][]Decl
	seenFinal    []bool
}

type scriptStep struct {
	reply AssistantMessage
	err   error
}

func (p *scriptedProvider) Generate(ctx context.Context, messages []Message, tools []Decl) (AssistantMessage, error) {
	p.seenMessages = append(p.seenMessages, messages)
	p.seenDecls = append(p.seenDecls, tools)
	p.seenFinal = append(p.seenFinal, IsFinalAnswerRound(ctx))
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
				{ID: "call-1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)},
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
				{ID: "c1", Name: "dice", Input: json.RawMessage(`{"count":0,"sides":6}`)},
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
				{ID: "c1", Name: "secret", Input: json.RawMessage(`{}`)},
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
				{ID: "w1", Name: "writer", Input: json.RawMessage(`{}`)},
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
	// Provider that always calls dice, never stops — not even on the marked
	// final-answer round, where it still tool-calls with no prose. Only that
	// empty final answer keeps ErrMaxRoundsExceeded as the hard stop.
	steps := make([]scriptStep, 20)
	for i := range steps {
		steps[i] = scriptStep{reply: AssistantMessage{ToolCalls: []ToolCall{
			{ID: "c", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":6}`)},
		}}}
	}
	p := &scriptedProvider{t: t, steps: steps}
	loop := diceLoop(t, p)
	loop.MaxRounds = 3
	_, err := loop.Run(context.Background(), []Message{{Role: RoleUser, Text: "x"}})
	if !errors.Is(err, ErrMaxRoundsExceeded) {
		t.Errorf("runaway loop should hit ErrMaxRoundsExceeded, got %v", err)
	}
	// Rounds 0..2 execute, round 3's generate still tool-calls (budget
	// exhausted), then ONE marked final-answer generate: maxRounds+2 in total —
	// one more than the pre-degrade loop, and only the last one marked.
	if p.calls != 5 {
		t.Errorf("provider calls = %d, want 5 (4 armed rounds + 1 marked final answer)", p.calls)
	}
	for i, marked := range p.seenFinal {
		if wantMarked := i == len(p.seenFinal)-1; marked != wantMarked {
			t.Errorf("generate %d final-answer marker = %v, want %v", i, marked, wantMarked)
		}
	}
}

// TestLoopUnknownToolShortCircuitsToFinalAnswer is the live Mehra reproduction
// (unknown-name variant): the model repeatedly calls a tool the GrantSet cannot
// resolve, so every executed round yields ONLY error results. After 2 such
// all-error rounds the loop must jump straight to the marked final-answer round
// instead of grinding the whole budget, and return that round's prose with a
// nil error.
func TestLoopUnknownToolShortCircuitsToFinalAnswer(t *testing.T) {
	p := &scriptedProvider{
		t: t,
		steps: []scriptStep{
			{reply: AssistantMessage{ToolCalls: []ToolCall{
				{ID: "c1", Name: "remember_knowledge", Input: json.RawMessage(`{}`)},
			}}},
			{reply: AssistantMessage{ToolCalls: []ToolCall{
				{ID: "c2", Name: "remember_knowledge", Input: json.RawMessage(`{}`)},
			}}},
			{reply: AssistantMessage{Text: "Let me just tell you plainly."}},
		},
	}
	loop := diceLoop(t, p)

	final, err := loop.Run(context.Background(), []Message{{Role: RoleUser, Text: "remember this"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final != "Let me just tell you plainly." {
		t.Errorf("final = %q, want the final-answer round's prose", final)
	}
	if p.calls != 3 {
		t.Fatalf("provider calls = %d, want 3 (2 all-error rounds + the final-answer round, not 8)", p.calls)
	}
	if p.seenFinal[0] || p.seenFinal[1] {
		t.Error("regular rounds must not carry the final-answer marker")
	}
	if !p.seenFinal[2] {
		t.Error("the post-short-circuit generate must carry the final-answer marker")
	}
}

// TestLoopErrorRoundStreakResetsOnProgress pins the CONSECUTIVE requirement of
// the no-progress short-circuit: a round with at least one successful result
// resets the all-error streak, so mixed progress never triggers the jump early.
func TestLoopErrorRoundStreakResetsOnProgress(t *testing.T) {
	rk := scriptStep{reply: AssistantMessage{ToolCalls: []ToolCall{
		{ID: "e", Name: "secret", Input: json.RawMessage(`{}`)}, // ungranted → error result
	}}}
	dice := scriptStep{reply: AssistantMessage{ToolCalls: []ToolCall{
		{ID: "d", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":6}`)},
	}}}
	p := &scriptedProvider{
		t: t,
		steps: []scriptStep{
			rk,                                      // streak 1
			dice,                                    // progress → reset
			rk,                                      // streak 1
			rk,                                      // streak 2 → short-circuit
			{reply: AssistantMessage{Text: "done"}}, // marked final answer
		},
	}
	loop := diceLoop(t, p)

	final, err := loop.Run(context.Background(), []Message{{Role: RoleUser, Text: "x"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final != "done" || p.calls != 5 {
		t.Errorf("final=%q calls=%d, want \"done\" after 5 generates (streak reset by the dice round)", final, p.calls)
	}
	for i, marked := range p.seenFinal {
		if wantMarked := i == 4; marked != wantMarked {
			t.Errorf("generate %d final-answer marker = %v, want %v", i, marked, wantMarked)
		}
	}
}

// TestLoopMaxRoundsFinalAnswerSpeaks is the granted-tool chain-call variant (the
// live Mehra signature with remember_knowledge GRANTED): dice succeeds every
// round, so the no-progress short-circuit never fires and the round budget runs
// out. The loop must feed back budget-exhausted error results for the
// over-budget calls and force ONE marked tool-less final answer, returning its
// prose instead of ErrMaxRoundsExceeded.
func TestLoopMaxRoundsFinalAnswerSpeaks(t *testing.T) {
	call := func(id string) scriptStep {
		return scriptStep{reply: AssistantMessage{ToolCalls: []ToolCall{
			{ID: id, Name: "dice", Input: json.RawMessage(`{"count":1,"sides":6}`)},
		}}}
	}
	p := &scriptedProvider{
		t: t,
		steps: []scriptStep{
			call("c0"), call("c1"), // rounds 0,1: executed
			call("c2"), // round 2: budget exhausted → budget-error results
			{reply: AssistantMessage{Text: "The dice say plenty; here is my answer."}},
		},
	}
	loop := diceLoop(t, p)
	loop.MaxRounds = 2

	final, err := loop.Run(context.Background(), []Message{{Role: RoleUser, Text: "roll forever"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final != "The dice say plenty; here is my answer." {
		t.Errorf("final = %q, want the final-answer prose", final)
	}
	if p.calls != 4 {
		t.Fatalf("provider calls = %d, want 4 (2 executed + 1 over-budget + 1 marked final)", p.calls)
	}
	if !p.seenFinal[3] {
		t.Error("the last generate must carry the final-answer marker")
	}
	// The marked round's conversation must end with the budget-exhausted error
	// result for the over-budget call — the instruction the model answers under.
	finalConvo := p.seenMessages[3]
	tail := finalConvo[len(finalConvo)-1]
	if tail.Role != RoleTool || len(tail.ToolResults) != 1 {
		t.Fatalf("final round's last message = %+v, want the tool-role budget results", tail)
	}
	res := tail.ToolResults[0]
	if res.CallID != "c2" || !res.IsError || !strings.Contains(res.Content, "tool budget exhausted") {
		t.Errorf("budget result = %+v, want an error result for c2 telling the model to answer in prose", res)
	}
}

// TestLoopFinalAnswerStillToolCalling preserves the hard stop: a final-answer
// round that STILL returns tool calls with no prose has nothing to speak, so
// ErrMaxRoundsExceeded surfaces exactly as before the degrade path existed.
func TestLoopFinalAnswerStillToolCalling(t *testing.T) {
	steps := make([]scriptStep, 4)
	for i := range steps {
		steps[i] = scriptStep{reply: AssistantMessage{ToolCalls: []ToolCall{
			{ID: "c", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":6}`)},
		}}}
	}
	p := &scriptedProvider{t: t, steps: steps}
	loop := diceLoop(t, p)
	loop.MaxRounds = 1

	_, err := loop.Run(context.Background(), []Message{{Role: RoleUser, Text: "x"}})
	if !errors.Is(err, ErrMaxRoundsExceeded) {
		t.Errorf("empty final answer should keep ErrMaxRoundsExceeded, got %v", err)
	}
	if err != nil && (!strings.Contains(err.Error(), "(1 rounds)") || strings.Contains(err.Error(), "no-progress")) {
		t.Errorf("budget-path error = %q, want the round-budget wording, not the short-circuit one", err)
	}
	if p.calls != 3 {
		t.Errorf("provider calls = %d, want 3 (1 executed + 1 over-budget + 1 marked final)", p.calls)
	}
}

// TestLoopNoProgressEmptyFinalAnswerNamesShortCircuit pins the error wording on
// the short-circuit trigger: when the no-progress jump (2 all-error rounds)
// reaches a final-answer round that still returns tool calls with no prose, the
// error must keep matching [ErrMaxRoundsExceeded] for errors.Is compatibility
// BUT name the actual trigger — after only 2 rounds it must not claim the full
// 8-round budget was exhausted, which would point an operator at a
// runaway-round problem instead of the unavailable-tool no-progress stop.
func TestLoopNoProgressEmptyFinalAnswerNamesShortCircuit(t *testing.T) {
	rk := scriptStep{reply: AssistantMessage{ToolCalls: []ToolCall{
		{ID: "e", Name: "remember_knowledge", Input: json.RawMessage(`{}`)},
	}}}
	p := &scriptedProvider{
		t: t,
		steps: []scriptStep{
			rk, rk, // 2 all-error rounds → short-circuit
			// Marked final-answer round: still tool-calling, no prose.
			{reply: AssistantMessage{ToolCalls: []ToolCall{
				{ID: "c", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":6}`)},
			}}},
		},
	}
	loop := diceLoop(t, p)

	_, err := loop.Run(context.Background(), []Message{{Role: RoleUser, Text: "remember this"}})
	if !errors.Is(err, ErrMaxRoundsExceeded) {
		t.Fatalf("err = %v, want ErrMaxRoundsExceeded (errors.Is compatibility)", err)
	}
	if !strings.Contains(err.Error(), "no-progress after 2 all-error rounds") {
		t.Errorf("err = %q, want it to name the no-progress short-circuit trigger", err)
	}
	if strings.Contains(err.Error(), "8 rounds") {
		t.Errorf("err = %q, must not claim the 8-round budget was exhausted after 2 rounds", err)
	}
	if p.calls != 3 {
		t.Errorf("provider calls = %d, want 3 (2 all-error rounds + the final-answer round)", p.calls)
	}
}

// TestLoopRunStreamNoProgressEmptyFinalAnswerNamesShortCircuit is the streaming
// twin: RunStream's short-circuit path must thread the same trigger into the
// empty-final-answer error.
func TestLoopRunStreamNoProgressEmptyFinalAnswerNamesShortCircuit(t *testing.T) {
	rk := scriptStep{reply: AssistantMessage{ToolCalls: []ToolCall{
		{ID: "e", Name: "remember_knowledge", Input: json.RawMessage(`{}`)},
	}}}
	p := &scriptedStreamingProvider{scriptedProvider: scriptedProvider{
		t: t,
		steps: []scriptStep{
			rk, rk,
			{reply: AssistantMessage{ToolCalls: []ToolCall{
				{ID: "c", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":6}`)},
			}}},
		},
	}}
	loop := diceLoop(t, p)

	_, err := loop.RunStream(context.Background(), []Message{{Role: RoleUser, Text: "remember this"}},
		func(string) error { return nil })
	if !errors.Is(err, ErrMaxRoundsExceeded) {
		t.Fatalf("err = %v, want ErrMaxRoundsExceeded (errors.Is compatibility)", err)
	}
	if !strings.Contains(err.Error(), "no-progress after 2 all-error rounds") {
		t.Errorf("err = %q, want the no-progress short-circuit wording on the streaming path", err)
	}
}

// TestLoopFinalAnswerToolCallWithProseUsesProse pins the drop rule: a marked
// final-answer round that returns BOTH prose and tool calls has its calls
// dropped un-executed and its prose used as the answer.
func TestLoopFinalAnswerToolCallWithProseUsesProse(t *testing.T) {
	call := scriptStep{reply: AssistantMessage{ToolCalls: []ToolCall{
		{ID: "c", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":6}`)},
	}}}
	p := &scriptedProvider{
		t: t,
		steps: []scriptStep{
			call, call, // round 0 executed; round 1 over-budget
			{reply: AssistantMessage{
				Text: "I will just answer.",
				ToolCalls: []ToolCall{
					{ID: "cx", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":6}`)},
				},
			}},
		},
	}
	loop := diceLoop(t, p)
	loop.MaxRounds = 1
	var executed int
	loop.OnToolResult = func(context.Context, string, string, bool) { executed++ }

	final, err := loop.Run(context.Background(), []Message{{Role: RoleUser, Text: "x"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final != "I will just answer." {
		t.Errorf("final = %q, want the final round's prose with its tool calls dropped", final)
	}
	if executed != 1 {
		t.Errorf("executed tool results = %d, want 1 (round 0 only; over-budget and final-round calls never run)", executed)
	}
}

// TestLoopRunStreamFinalAnswer is the streaming twin of the budget degrade: the
// marked final answer's prose must reach onText — it is what TTS speaks.
func TestLoopRunStreamFinalAnswer(t *testing.T) {
	call := func(id string) scriptStep {
		return scriptStep{reply: AssistantMessage{ToolCalls: []ToolCall{
			{ID: id, Name: "dice", Input: json.RawMessage(`{"count":1,"sides":6}`)},
		}}}
	}
	p := &scriptedStreamingProvider{scriptedProvider: scriptedProvider{
		t: t,
		steps: []scriptStep{
			call("c0"), // round 0 executed
			call("c1"), // round 1: over-budget
			{reply: AssistantMessage{Text: "Enough rolling. Here is my answer."}},
		},
	}}
	loop := diceLoop(t, p)
	loop.MaxRounds = 1

	var streamed strings.Builder
	final, err := loop.RunStream(context.Background(), []Message{{Role: RoleUser, Text: "roll forever"}},
		func(delta string) error { streamed.WriteString(delta); return nil })
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if final != "Enough rolling. Here is my answer." {
		t.Errorf("final = %q, want the final-answer prose", final)
	}
	if streamed.String() != "Enough rolling. Here is my answer." {
		t.Errorf("streamed = %q, want the final-answer prose forwarded to onText", streamed.String())
	}
	if p.calls != 3 || !p.seenFinal[2] {
		t.Errorf("calls=%d seenFinal=%v, want 3 generates with the last one marked", p.calls, p.seenFinal)
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

// TestLoopPassesGrantConfigToHandler pins ADR-0029's per-grant scoping: the
// Config carried on a Grant reaches the Tool handler as grantConfig at execution
// time (the only place scope is enforced — never the LLM). It is the tool-side of
// #113's DB-hydrated config: a grant loaded from a tool_agent_grant row with a
// jsonb scope must land, unchanged, in Execute — the model cannot widen it.
func TestLoopPassesGrantConfigToHandler(t *testing.T) {
	var gotConfig any
	captor := stubTool{
		name:     "scoped",
		readOnly: true,
		exec: func(_ context.Context, _ json.RawMessage, grant any) (string, error) {
			gotConfig = grant
			return "done", nil
		},
	}
	r := NewRegistry()
	r.MustRegister(captor)
	scope := json.RawMessage(`{"scope":"self"}`)
	gs := NewGrantSet(r, Grant{ToolName: "scoped", Config: scope})

	p := &scriptedProvider{
		t: t,
		steps: []scriptStep{
			{reply: AssistantMessage{ToolCalls: []ToolCall{
				{ID: "c1", Name: "scoped", Input: json.RawMessage(`{}`)},
			}}},
			{reply: AssistantMessage{Text: "ok"}},
		},
	}
	loop := NewLoop(p, gs)
	if _, err := loop.Run(context.Background(), []Message{{Role: RoleUser, Text: "go"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	got, ok := gotConfig.(json.RawMessage)
	if !ok {
		t.Fatalf("handler received grantConfig of type %T, want json.RawMessage", gotConfig)
	}
	if string(got) != string(scope) {
		t.Errorf("handler grantConfig = %q, want %q (scope must reach the handler unchanged)", got, scope)
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
				{ID: "c1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":6}`)},
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
