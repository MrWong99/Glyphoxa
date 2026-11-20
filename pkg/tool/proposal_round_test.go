package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// proposalStub is a proposal-mediated (ADR-0052) non-read-only Tool: its only
// effect is a GM-reviewed proposal, the remember_knowledge shape.
type proposalStub struct {
	stubTool
	ran *int
}

func (p proposalStub) ProposalMediated() bool { return true }
func (p proposalStub) Execute(context.Context, json.RawMessage, any) (string, error) {
	*p.ran++
	return "proposal recorded", nil
}

func proposalLoop(t *testing.T, p Provider, ran *int) *Loop {
	t.Helper()
	r := NewRegistry()
	r.MustRegister(proposalStub{stubTool: stubTool{name: "remember_knowledge"}, ran: ran})
	r.MustRegister(NewDiceWithRand(fixedRand()))
	return NewLoop(p, NewGrantSet(r, Grant{ToolName: "remember_knowledge"}, Grant{ToolName: "dice"}))
}

// TestRunStream_ProposalOnlyRoundAfterSpokenAnswerEndsTurn is O11: a model that
// streams its full spoken answer AND calls remember_knowledge in the same round
// used to have that prose spoken, the call executed, and then a SECOND
// generation spoken after it — the NPC answered twice. A round whose calls are
// all proposal-mediated (fire-and-forget: nothing the reply needs) after prose
// was streamed now executes the calls and ends the turn on that prose.
func TestRunStream_ProposalOnlyRoundAfterSpokenAnswerEndsTurn(t *testing.T) {
	ran := 0
	p := &scriptedStreamingProvider{scriptedProvider: scriptedProvider{
		t: t,
		steps: []scriptStep{
			{reply: AssistantMessage{
				Text: "The Duke owes you a favour, friend.",
				ToolCalls: []ToolCall{{ID: "c1", Name: "remember_knowledge",
					Input: json.RawMessage(`{"kind":"fact"}`)}},
			}},
			{reply: AssistantMessage{Text: "As I said, the Duke owes you."}},
		},
	}}
	loop := proposalLoop(t, p, &ran)
	var results []string
	loop.OnToolResult = func(_ context.Context, name, _ string, _ bool) { results = append(results, name) }

	var spoken strings.Builder
	final, err := loop.RunStream(context.Background(),
		[]Message{{Role: RoleUser, Text: "Do you remember the Duke?"}},
		func(d string) error { spoken.WriteString(d); return nil })
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if p.calls != 1 {
		t.Fatalf("provider calls = %d, want 1 — a second generation is a second spoken answer", p.calls)
	}
	if spoken.String() != "The Duke owes you a favour, friend." || final != spoken.String() {
		t.Errorf("spoken = %q, final = %q; want the one answer", spoken.String(), final)
	}
	if ran != 1 || len(results) != 1 || results[0] != "remember_knowledge" {
		t.Errorf("proposal tool ran %d times, results %v; want it executed once", ran, results)
	}
}

// TestRunStream_ToolRoundStillFollowsUp pins the boundary: a round that needs a
// result for the reply (dice), or that streamed no prose, keeps the follow-up
// generation that speaks the answer.
func TestRunStream_ToolRoundStillFollowsUp(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first AssistantMessage
	}{
		{"dice result needed", AssistantMessage{Text: "Let me roll.", ToolCalls: []ToolCall{
			{ID: "c1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)},
			{ID: "c2", Name: "remember_knowledge", Input: json.RawMessage(`{}`)},
		}}},
		{"no prose streamed", AssistantMessage{ToolCalls: []ToolCall{
			{ID: "c1", Name: "remember_knowledge", Input: json.RawMessage(`{}`)},
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ran := 0
			p := &scriptedStreamingProvider{scriptedProvider: scriptedProvider{
				t:     t,
				steps: []scriptStep{{reply: tc.first}, {reply: AssistantMessage{Text: "Done."}}},
			}}
			final, err := proposalLoop(t, p, &ran).RunStream(context.Background(),
				[]Message{{Role: RoleUser, Text: "hi"}}, func(string) error { return nil })
			if err != nil {
				t.Fatalf("RunStream: %v", err)
			}
			if p.calls != 2 || final != "Done." {
				t.Errorf("calls = %d, final = %q; want the follow-up answer", p.calls, final)
			}
		})
	}
}
