package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// fakeKGWriter records what remember_knowledge asked the KG write seam to do so
// tests can assert the handler's scope enforcement without a real DB.
type fakeKGWriter struct {
	ownRef     KGNodeRef
	ownOK      bool
	ownErr     error
	ownCalled  bool
	ownAgentID string

	created       []ProposedWrite
	createAgentID string
	createCtx     context.Context
	createErr     error
	onCreate      func()
}

func (f *fakeKGWriter) OwnNode(_ context.Context, agentID string) (KGNodeRef, bool, error) {
	f.ownCalled = true
	f.ownAgentID = agentID
	return f.ownRef, f.ownOK, f.ownErr
}

func (f *fakeKGWriter) CreateProposal(ctx context.Context, agentID string, w ProposedWrite) error {
	if f.onCreate != nil {
		f.onCreate()
	}
	if f.createErr != nil {
		return f.createErr
	}
	f.createAgentID = agentID
	f.createCtx = ctx
	f.created = append(f.created, w)
	return nil
}

var (
	cfgOwnNode  = json.RawMessage(`{"scope":"own_node"}`)
	cfgCampaign = json.RawMessage(`{"scope":"campaign"}`)
)

// Test 1: per-kind argument validation refuses bad calls and NEVER reaches the
// write seam — no OwnNode lookup, no proposal row.
func TestRememberKnowledge_ArgValidation(t *testing.T) {
	long := strings.Repeat("x", MaxProposalTextRunes+1)
	longName := strings.Repeat("x", MaxKGNameRunes+1) // entity names cap at MaxKGNameRunes
	cases := []struct {
		name string
		args string
	}{
		{"unknown kind", `{"kind":"blah"}`},
		{"empty kind", `{"kind":""}`},
		{"fact missing text", `{"kind":"fact","subject":"The Duke"}`},
		{"fact too long", `{"kind":"fact","subject":"X","fact":"` + long + `"}`},
		{"fact subject too long", `{"kind":"fact","subject":"` + longName + `","fact":"x"}`},
		{"edge bad relation", `{"kind":"edge","subject":"X","relation":"loves","target":"Y"}`},
		{"edge missing target", `{"kind":"edge","subject":"X","relation":"knows"}`},
		{"edge subject too long", `{"kind":"edge","subject":"` + longName + `","relation":"knows","target":"Y"}`},
		{"edge target too long", `{"kind":"edge","subject":"X","relation":"knows","target":"` + longName + `"}`},
		{"node missing name", `{"kind":"node","node_type":"npc"}`},
		{"node bad type", `{"kind":"node","name":"X","node_type":"dragon"}`},
		{"node name too long", `{"kind":"node","node_type":"npc","name":"` + long + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &fakeKGWriter{ownRef: KGNodeRef{ID: "n1", Name: "Bart"}, ownOK: true}
			rk := NewRememberKnowledge(w)
			ctx := WithCaller(context.Background(), "agent-1")
			_, err := rk.Execute(ctx, json.RawMessage(tc.args), cfgCampaign)
			if err == nil {
				t.Fatalf("want validation error, got nil")
			}
			if len(w.created) != 0 {
				t.Errorf("writer.CreateProposal was called on a bad arg: %+v", w.created)
			}
			if w.ownCalled {
				t.Errorf("writer.OwnNode was called on a bad arg")
			}
		})
	}
}

// Test 2: an own_node fact/edge anchors on the CALLER's own Node — the subject is
// overwritten with the own Node's name and the anchor node_id is the own Node's
// id, resolved from the ctx caller (never the LLM's subject arg).
func TestRememberKnowledge_OwnNodeAnchoring(t *testing.T) {
	t.Run("fact overwrites subject", func(t *testing.T) {
		w := &fakeKGWriter{ownRef: KGNodeRef{ID: "node-1", Name: "Bartholomew"}, ownOK: true}
		rk := NewRememberKnowledge(w)
		ctx := WithCaller(context.Background(), "agent-9")
		out, err := rk.Execute(ctx, json.RawMessage(
			`{"kind":"fact","subject":"Someone Else","fact":"I brew the best ale"}`), cfgOwnNode)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if !strings.Contains(out, "GM") {
			t.Errorf("success text = %q", out)
		}
		if w.ownAgentID != "agent-9" {
			t.Errorf("OwnNode resolved agent %q, want agent-9", w.ownAgentID)
		}
		if w.createAgentID != "agent-9" {
			t.Errorf("CreateProposal agent %q, want agent-9", w.createAgentID)
		}
		got := w.created[0]
		want := ProposedWrite{V: 1, Kind: "fact", NodeID: "node-1", Subject: "Bartholomew", Fact: "I brew the best ale"}
		if got != want {
			t.Errorf("proposed write = %+v, want %+v", got, want)
		}
	})

	t.Run("edge is FROM own node", func(t *testing.T) {
		w := &fakeKGWriter{ownRef: KGNodeRef{ID: "node-1", Name: "Bartholomew"}, ownOK: true}
		rk := NewRememberKnowledge(w)
		ctx := WithCaller(context.Background(), "agent-9")
		_, err := rk.Execute(ctx, json.RawMessage(
			`{"kind":"edge","subject":"ignored","relation":"knows","target":"The Duke"}`), cfgOwnNode)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		got := w.created[0]
		want := ProposedWrite{V: 1, Kind: "edge", NodeID: "node-1", Subject: "Bartholomew", Relation: "knows", Target: "The Duke"}
		if got != want {
			t.Errorf("proposed write = %+v, want %+v", got, want)
		}
	})
}

// Test 3: an own_node-scoped Agent may NOT create a new entry (kind=node); the
// write seam is never touched.
func TestRememberKnowledge_OwnNodeRefusesNewEntry(t *testing.T) {
	w := &fakeKGWriter{ownRef: KGNodeRef{ID: "node-1", Name: "Bart"}, ownOK: true}
	rk := NewRememberKnowledge(w)
	ctx := WithCaller(context.Background(), "agent-9")
	_, err := rk.Execute(ctx, json.RawMessage(
		`{"kind":"node","name":"New Town","node_type":"location"}`), cfgOwnNode)
	if err == nil {
		t.Fatal("want refusal for kind=node under own_node, got nil")
	}
	if w.ownCalled {
		t.Error("OwnNode was called on a refused new-entry proposal")
	}
	if len(w.created) != 0 {
		t.Errorf("a proposal was created: %+v", w.created)
	}
}

// Test 4: an Agent with no linked wiki entry — or an unstamped ctx — cannot
// propose own_node facts; the handler refuses with zero proposals.
func TestRememberKnowledge_OwnNodeUnlinked(t *testing.T) {
	t.Run("unstamped ctx", func(t *testing.T) {
		w := &fakeKGWriter{ownOK: false} // no caller id ⇒ no own node
		rk := NewRememberKnowledge(w)
		_, err := rk.Execute(context.Background(), json.RawMessage(
			`{"kind":"fact","fact":"I brew ale"}`), cfgOwnNode)
		if err == nil {
			t.Fatal("want error for unlinked/unstamped caller, got nil")
		}
		if len(w.created) != 0 {
			t.Errorf("a proposal was created: %+v", w.created)
		}
	})

	t.Run("linked node absent", func(t *testing.T) {
		w := &fakeKGWriter{ownOK: false}
		rk := NewRememberKnowledge(w)
		ctx := WithCaller(context.Background(), "agent-9")
		_, err := rk.Execute(ctx, json.RawMessage(
			`{"kind":"fact","fact":"I brew ale"}`), cfgOwnNode)
		if err == nil {
			t.Fatal("want error for agent with no linked node, got nil")
		}
		if len(w.created) != 0 {
			t.Errorf("a proposal was created: %+v", w.created)
		}
	})
}

// Test 5: a campaign-scoped Agent (the Butler) proposes all three kinds with the
// subject preserved from the args and no own-node anchor.
func TestRememberKnowledge_CampaignScope(t *testing.T) {
	t.Run("fact preserves subject", func(t *testing.T) {
		w := &fakeKGWriter{}
		rk := NewRememberKnowledge(w)
		ctx := WithCaller(context.Background(), "butler-1")
		_, err := rk.Execute(ctx, json.RawMessage(
			`{"kind":"fact","subject":"The Duke","fact":"rules the city"}`), cfgCampaign)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if w.ownCalled {
			t.Error("OwnNode was called under campaign scope")
		}
		got := w.created[0]
		want := ProposedWrite{V: 1, Kind: "fact", Subject: "The Duke", Fact: "rules the city"}
		if got != want {
			t.Errorf("proposed write = %+v, want %+v", got, want)
		}
	})

	t.Run("new entry", func(t *testing.T) {
		w := &fakeKGWriter{}
		rk := NewRememberKnowledge(w)
		ctx := WithCaller(context.Background(), "butler-1")
		_, err := rk.Execute(ctx, json.RawMessage(
			`{"kind":"node","node_type":"faction","name":"Ironhold","body":"a smiths' guild"}`), cfgCampaign)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		got := w.created[0]
		want := ProposedWrite{V: 1, Kind: "node", NodeType: "faction", Name: "Ironhold", Body: "a smiths' guild"}
		if got != want {
			t.Errorf("proposed write = %+v, want %+v", got, want)
		}
	})
}

// Test 6: scope resolution — nil config fails CLOSED to own_node, a bogus scope
// fails LOUD, and a nil writer reports unavailable.
func TestRememberKnowledge_ScopeResolution(t *testing.T) {
	t.Run("nil config defaults own_node", func(t *testing.T) {
		w := &fakeKGWriter{ownRef: KGNodeRef{ID: "node-1", Name: "Bart"}, ownOK: true}
		rk := NewRememberKnowledge(w)
		ctx := WithCaller(context.Background(), "agent-9")
		_, err := rk.Execute(ctx, json.RawMessage(`{"kind":"fact","fact":"I brew ale"}`), nil)
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if !w.ownCalled {
			t.Error("nil config did not fail closed to own_node (OwnNode never called)")
		}
		if w.created[0].NodeID != "node-1" {
			t.Errorf("proposal not anchored on own node: %+v", w.created[0])
		}
	})

	t.Run("bogus scope is loud", func(t *testing.T) {
		w := &fakeKGWriter{}
		rk := NewRememberKnowledge(w)
		ctx := WithCaller(context.Background(), "agent-9")
		_, err := rk.Execute(ctx, json.RawMessage(`{"kind":"fact","fact":"x"}`),
			json.RawMessage(`{"scope":"world"}`))
		if err == nil {
			t.Fatal("want loud error for unknown scope, got nil")
		}
		if len(w.created) != 0 {
			t.Errorf("a proposal was created under a bogus scope: %+v", w.created)
		}
	})

	t.Run("nil writer unavailable", func(t *testing.T) {
		rk := NewRememberKnowledge(nil)
		_, err := rk.Execute(context.Background(), json.RawMessage(`{"kind":"fact","fact":"x"}`), cfgCampaign)
		if err == nil {
			t.Fatal("want unavailable error for nil writer, got nil")
		}
	})
}

// Test 7: remember_knowledge executes INLINE in the loop (it is proposal-mediated
// despite ReadOnly=false), while a plain non-read-only, non-proposal Tool is still
// hard-refused — the ADR-0030 refusal survives ADR-0052's carve-out.
func TestLoopRunsRememberKnowledgeInline(t *testing.T) {
	w := &fakeKGWriter{ownRef: KGNodeRef{ID: "node-1", Name: "Bart"}, ownOK: true}
	reg := BuiltinRegistry(Deps{KGW: w})
	reg.MustRegister(stubTool{name: "mutate", readOnly: false}) // NOT proposal-mediated
	gs := NewGrantSet(reg,
		Grant{ToolName: "remember_knowledge", Config: cfgOwnNode},
		Grant{ToolName: "mutate"},
	)

	p := &scriptedProvider{
		t: t,
		steps: []scriptStep{
			{reply: AssistantMessage{ToolCalls: []ToolCall{
				{ID: "c1", Name: "remember_knowledge", Input: json.RawMessage(`{"kind":"fact","fact":"I brew ale"}`)},
			}}},
			{reply: AssistantMessage{ToolCalls: []ToolCall{
				{ID: "c2", Name: "mutate", Input: json.RawMessage(`{}`)},
			}}},
			{reply: AssistantMessage{Text: "done"}},
		},
	}
	loop := NewLoop(p, gs)
	ctx := WithCaller(context.Background(), "agent-9")
	if _, err := loop.Run(ctx, []Message{{Role: RoleUser, Text: "remember this"}}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	remember := findToolResult(t, p.seenMessages, "c1")
	if remember.IsError {
		t.Errorf("remember_knowledge was refused inline: %q", remember.Content)
	}
	if len(w.created) != 1 {
		t.Fatalf("want 1 proposal created, got %d", len(w.created))
	}
	mutate := findToolResult(t, p.seenMessages, "c2")
	if !mutate.IsError || !strings.Contains(mutate.Content, "not read-only") {
		t.Errorf("plain non-read-only tool was NOT hard-refused: %+v", mutate)
	}
}

// Test 8: barge pin — a cancel landing exactly at write time still leaves the
// proposal; the loop errors on the next round but the row is never rolled back
// (ADR-0052: a barged reply still yields its proposal).
func TestLoopRememberKnowledgeSurvivesBarge(t *testing.T) {
	ctx, cancel := context.WithCancel(WithCaller(context.Background(), "agent-9"))
	w := &fakeKGWriter{ownRef: KGNodeRef{ID: "node-1", Name: "Bart"}, ownOK: true}
	w.onCreate = cancel // the barge lands as the proposal is written

	reg := BuiltinRegistry(Deps{KGW: w})
	gs := NewGrantSet(reg, Grant{ToolName: "remember_knowledge", Config: cfgOwnNode})
	p := &scriptedProvider{
		t: t,
		steps: []scriptStep{
			{reply: AssistantMessage{ToolCalls: []ToolCall{
				{ID: "c1", Name: "remember_knowledge", Input: json.RawMessage(`{"kind":"fact","fact":"I brew ale"}`)},
			}}},
			// A second step would speak final text, but the cancelled ctx aborts
			// the loop at the top of round 1 before it is reached.
			{reply: AssistantMessage{Text: "unreachable"}},
		},
	}
	loop := NewLoop(p, gs)
	_, err := loop.Run(ctx, []Message{{Role: RoleUser, Text: "remember this"}})
	if err == nil {
		t.Fatal("want the barged loop to error, got nil")
	}
	if len(w.created) != 1 {
		t.Errorf("barge rolled back the proposal: %d created, want 1", len(w.created))
	}
}
