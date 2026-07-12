package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// pseudoRecorder captures OnPseudoCall firings so tests can assert the
// metric/log hook increments once per occurrence with the right recovered flag.
type pseudoRecorder struct {
	names     []string
	recovered []bool
}

func (r *pseudoRecorder) hook(_ context.Context, name string, recovered bool) {
	r.names = append(r.names, name)
	r.recovered = append(r.recovered, recovered)
}

// scriptedStreamingProvider is scriptedProvider plus GenerateStream: it replays
// the same scripted [AssistantMessage]s but also streams each reply's Text to
// onText in small chunks, so the streaming path (and its scrubber) is exercised
// across delta boundaries.
type scriptedStreamingProvider struct {
	scriptedProvider
	chunk int // bytes per streamed delta; 0 → 3
}

func (p *scriptedStreamingProvider) GenerateStream(ctx context.Context, messages []Message, tools []Decl, onText func(string) error) (AssistantMessage, error) {
	// Consume the next scripted step (advancing p.calls), mirroring Generate's
	// bookkeeping, then stream its reply text in chunks.
	p.seenMessages = append(p.seenMessages, messages)
	p.seenDecls = append(p.seenDecls, tools)
	if p.calls >= len(p.steps) {
		p.t.Fatalf("scriptedStreamingProvider: unexpected GenerateStream call %d", p.calls+1)
	}
	step := p.steps[p.calls]
	p.calls++
	if step.err != nil {
		return AssistantMessage{}, step.err
	}
	n := p.chunk
	if n <= 0 {
		n = 3
	}
	if onText != nil {
		txt := step.reply.Text
		for i := 0; i < len(txt); i += n {
			end := i + n
			if end > len(txt) {
				end = len(txt)
			}
			if err := onText(txt[i:end]); err != nil {
				return AssistantMessage{}, err
			}
		}
	}
	return step.reply, nil
}

// TestLoopRecoversEmbeddedPseudoCall — AC "parseable + granted pseudo-call → the
// tool actually executes and the follow-up round delivers" and "embedded → clean
// speech text, no leak". Prose with an embedded granted dice pseudo-call: the
// spoken/persisted assistant text is scrubbed clean, the dice tool EXECUTES via
// the real loop, and the follow-up round's text is the answer.
func TestLoopRecoversEmbeddedPseudoCall(t *testing.T) {
	rec := &pseudoRecorder{}
	p := &scriptedProvider{
		t: t,
		steps: []scriptStep{
			{reply: AssistantMessage{Text: `Rolling now. <function=dice {"count":1,"sides":20}</function>`}},
			{reply: AssistantMessage{Text: "You rolled a 14."}},
		},
	}
	loop := diceLoop(t, p)
	loop.OnPseudoCall = rec.hook

	final, err := loop.Run(context.Background(), []Message{{Role: RoleUser, Text: "roll d20"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final != "You rolled a 14." {
		t.Errorf("final = %q, want follow-up answer", final)
	}
	if p.calls != 2 {
		t.Fatalf("provider calls = %d, want 2 (recovered call forces a real round)", p.calls)
	}

	// The recorded assistant turn must carry CLEAN text + a synthetic tool call.
	asstTurn := p.seenMessages[1][1]
	if asstTurn.Role != RoleAssistant {
		t.Fatalf("expected assistant turn, got %+v", asstTurn)
	}
	if strings.Contains(asstTurn.Text, "<function") {
		t.Errorf("assistant turn text leaked the pseudo-call: %q", asstTurn.Text)
	}
	if asstTurn.Text != "Rolling now." {
		t.Errorf("assistant clean text = %q, want %q", asstTurn.Text, "Rolling now.")
	}
	if len(asstTurn.ToolCalls) != 1 || asstTurn.ToolCalls[0].Name != "dice" {
		t.Fatalf("recovered call missing: %+v", asstTurn.ToolCalls)
	}
	if asstTurn.ToolCalls[0].ID != "pseudo-0-0" {
		t.Errorf("synthetic ID = %q, want pseudo-0-0", asstTurn.ToolCalls[0].ID)
	}
	// dice actually ran → tool-role result present and not an error.
	res := p.seenMessages[1][2].ToolResults[0]
	if res.IsError || !strings.HasPrefix(res.Content, "Rolled 1d20:") {
		t.Errorf("dice did not execute cleanly: %+v", res)
	}
	if len(rec.names) != 1 || rec.names[0] != "dice" || !rec.recovered[0] {
		t.Errorf("OnPseudoCall = %+v/%+v, want one (dice,true)", rec.names, rec.recovered)
	}
}

// TestLoopWholeMessagePseudoCall — AC "whole-message pseudo-call → treated as
// tool round, nothing spoken". The entire assistant message is the pseudo-call:
// clean text is empty, so nothing is spoken; the recovered tool round runs and
// the follow-up delivers.
func TestLoopWholeMessagePseudoCall(t *testing.T) {
	rec := &pseudoRecorder{}
	p := &scriptedProvider{
		t: t,
		steps: []scriptStep{
			{reply: AssistantMessage{Text: `<function=dice {"count":1,"sides":6}</function>`}},
			{reply: AssistantMessage{Text: "It landed on 3."}},
		},
	}
	loop := diceLoop(t, p)
	loop.OnPseudoCall = rec.hook

	final, err := loop.Run(context.Background(), []Message{{Role: RoleUser, Text: "roll"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final != "It landed on 3." {
		t.Errorf("final = %q", final)
	}
	asstTurn := p.seenMessages[1][1]
	if asstTurn.Text != "" {
		t.Errorf("whole-message call should leave empty spoken text, got %q", asstTurn.Text)
	}
	if len(asstTurn.ToolCalls) != 1 {
		t.Fatalf("expected recovered call, got %+v", asstTurn.ToolCalls)
	}
	if len(rec.names) != 1 || !rec.recovered[0] {
		t.Errorf("OnPseudoCall = %+v, want (dice,true)", rec.recovered)
	}
}

// TestLoopStripsUngrantedPseudoCall — AC "ungranted → stripped, logged, turn
// still delivers remaining text". An ungranted tool name is scrubbed but NOT
// executed; the turn delivers the remaining prose in a single round.
func TestLoopStripsUngrantedPseudoCall(t *testing.T) {
	rec := &pseudoRecorder{}
	p := &scriptedProvider{
		t: t,
		steps: []scriptStep{
			{reply: AssistantMessage{Text: `Nope. <function=wipe_db {"all":true}</function>`}},
		},
	}
	loop := diceLoop(t, p)
	loop.OnPseudoCall = rec.hook

	final, err := loop.Run(context.Background(), []Message{{Role: RoleUser, Text: "hi"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final != "Nope." {
		t.Errorf("final = %q, want scrubbed remaining prose", final)
	}
	if p.calls != 1 {
		t.Errorf("ungranted call must not force a tool round, calls = %d", p.calls)
	}
	if len(rec.names) != 1 || rec.names[0] != "wipe_db" || rec.recovered[0] {
		t.Errorf("OnPseudoCall = %+v/%+v, want one (wipe_db,false)", rec.names, rec.recovered)
	}
}

// TestLoopStripsUnparseablePseudoCall — AC "unparseable → stripped, logged, turn
// still delivers". A granted tool name with junk args cannot be executed; it is
// scrubbed and metered as non-recovered.
func TestLoopStripsUnparseablePseudoCall(t *testing.T) {
	rec := &pseudoRecorder{}
	p := &scriptedProvider{
		t: t,
		steps: []scriptStep{
			{reply: AssistantMessage{Text: `Hmm <function=dice {bad json ]</function> ok`}},
		},
	}
	loop := diceLoop(t, p)
	loop.OnPseudoCall = rec.hook

	final, err := loop.Run(context.Background(), []Message{{Role: RoleUser, Text: "roll"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(final, "function") {
		t.Errorf("final leaked the junk call: %q", final)
	}
	if p.calls != 1 {
		t.Errorf("unparseable call must not execute, calls = %d", p.calls)
	}
	if len(rec.names) != 1 || rec.recovered[0] {
		t.Errorf("OnPseudoCall = %+v, want (dice,false)", rec.recovered)
	}
}

// TestLoopIneligiblePseudoCallNotRecovered — the metric must not overcount: a
// granted but side-effecting, non-proposal-mediated Tool (which loop.execute
// would refuse, ADR-0030) is stripped, NOT executed, and metered recovered=false
// even though it resolves to a granted Tool.
func TestLoopIneligiblePseudoCallNotRecovered(t *testing.T) {
	rec := &pseudoRecorder{}
	r := NewRegistry()
	r.MustRegister(stubTool{name: "writer", readOnly: false})
	gs := NewGrantSet(r, Grant{ToolName: "writer"})
	p := &scriptedProvider{
		t: t,
		steps: []scriptStep{
			{reply: AssistantMessage{Text: `Saving. <function=writer {"data":1}</function>`}},
		},
	}
	loop := NewLoop(p, gs)
	loop.OnPseudoCall = rec.hook

	final, err := loop.Run(context.Background(), []Message{{Role: RoleUser, Text: "save"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if final != "Saving." {
		t.Errorf("final = %q, want scrubbed prose", final)
	}
	if p.calls != 1 {
		t.Errorf("ineligible call must not force a tool round, calls = %d", p.calls)
	}
	if len(rec.names) != 1 || rec.names[0] != "writer" || rec.recovered[0] {
		t.Errorf("OnPseudoCall = %+v/%+v, want one (writer,false)", rec.names, rec.recovered)
	}
}

// TestRunStreamRecoversPseudoCall — AC on the streaming path: split deltas leak
// no marker to onText, the embedded granted call is recovered + executed, and the
// follow-up round streams the answer.
func TestRunStreamRecoversPseudoCall(t *testing.T) {
	rec := &pseudoRecorder{}
	p := &scriptedStreamingProvider{
		scriptedProvider: scriptedProvider{
			t: t,
			steps: []scriptStep{
				{reply: AssistantMessage{Text: `Rolling. <function=dice {"count":1,"sides":20}</function>`}},
				{reply: AssistantMessage{Text: "Result is 9."}},
			},
		},
		chunk: 2,
	}
	loop := diceLoop(t, p)
	loop.OnPseudoCall = rec.hook

	var streamed strings.Builder
	final, err := loop.RunStream(context.Background(), []Message{{Role: RoleUser, Text: "roll d20"}},
		func(delta string) error { streamed.WriteString(delta); return nil })
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if final != "Result is 9." {
		t.Errorf("final = %q", final)
	}
	got := streamed.String()
	if strings.Contains(got, "<function") || strings.Contains(got, "function=") {
		t.Errorf("streamed text leaked the pseudo-call: %q", got)
	}
	if !strings.Contains(got, "Rolling.") || !strings.Contains(got, "Result is 9.") {
		t.Errorf("streamed text missing prose: %q", got)
	}
	if len(rec.names) != 1 || rec.names[0] != "dice" || !rec.recovered[0] {
		t.Errorf("OnPseudoCall = %+v/%+v, want (dice,true)", rec.names, rec.recovered)
	}
}

// sanity: the recovered synthetic Input round-trips as valid JSON to the handler.
func TestRecoveredInputIsValidJSON(t *testing.T) {
	_, calls := ExtractPseudoCalls(`<function=dice {"count":1,"sides":20}</function>`)
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	var v map[string]any
	if err := json.Unmarshal(calls[0].Args, &v); err != nil {
		t.Fatalf("recovered args not valid JSON: %v", err)
	}
}
