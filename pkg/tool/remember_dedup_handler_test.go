package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// AC1: an exact/normalized re-proposal of an existing PENDING proposal for the
// same target creates no row and the tool reports it is already noted, echoing the
// known wording so the model learns it.
func TestRememberKnowledge_DedupAgainstPending(t *testing.T) {
	w := &fakeKGWriter{
		ownRef: KGNodeRef{ID: "node-1", Name: "Gesa"}, ownOK: true,
		known: KnownForTarget{Pending: []string{"Gesa ist die Schwester von Arturus."}},
	}
	rk := NewRememberKnowledge(w)
	ctx := WithCaller(context.Background(), "agent-9")
	out, err := rk.Execute(ctx, json.RawMessage(
		`{"kind":"fact","fact":"gesa ist die schwester von arturus"}`), cfgOwnNode)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(w.created) != 0 {
		t.Fatalf("a duplicate created a row: %+v", w.created)
	}
	if !strings.Contains(strings.ToLower(out), "already") {
		t.Errorf("result does not say already-noted: %q", out)
	}
	if !strings.Contains(out, "Gesa ist die Schwester von Arturus.") {
		t.Errorf("result does not echo the matched wording: %q", out)
	}
}

// AC1: a re-proposal of an ESTABLISHED node fact (already-canon body line) is
// likewise suppressed.
func TestRememberKnowledge_DedupAgainstEstablished(t *testing.T) {
	w := &fakeKGWriter{
		ownRef: KGNodeRef{ID: "node-1", Name: "Gesa"}, ownOK: true,
		known: KnownForTarget{Established: []string{"Gesa liebt es ihren Bruder zu ärgern"}},
	}
	rk := NewRememberKnowledge(w)
	ctx := WithCaller(context.Background(), "agent-9")
	_, err := rk.Execute(ctx, json.RawMessage(
		`{"kind":"fact","fact":"Gesa liebt es, ihren Bruder zu ärgern!"}`), cfgOwnNode)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(w.created) != 0 {
		t.Fatalf("a known established fact created a row: %+v", w.created)
	}
}

// AC3: a genuinely new fact still creates a proposal.
func TestRememberKnowledge_NewFactStillCreates(t *testing.T) {
	w := &fakeKGWriter{
		ownRef: KGNodeRef{ID: "node-1", Name: "Gesa"}, ownOK: true,
		known: KnownForTarget{Pending: []string{"Gesa ist die Schwester von Arturus."}},
	}
	rk := NewRememberKnowledge(w)
	ctx := WithCaller(context.Background(), "agent-9")
	_, err := rk.Execute(ctx, json.RawMessage(
		`{"kind":"fact","fact":"Gesa hasst Spinnen"}`), cfgOwnNode)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(w.created) != 1 {
		t.Fatalf("a new fact was not created: %d rows", len(w.created))
	}
}

// AC2: a scripted double-remember of the same fact in one session yields exactly
// one proposal row — the second call sees the first as pending and suppresses it.
func TestRememberKnowledge_DoubleRememberYieldsOneRow(t *testing.T) {
	w := &fakeKGWriter{ownRef: KGNodeRef{ID: "node-1", Name: "Gesa"}, ownOK: true}
	rk := NewRememberKnowledge(w)
	ctx := WithCaller(context.Background(), "agent-9")
	args := json.RawMessage(`{"kind":"fact","fact":"Gesa mag Kuchen"}`)
	if _, err := rk.Execute(ctx, args, cfgOwnNode); err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if _, err := rk.Execute(ctx, args, cfgOwnNode); err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if len(w.created) != 1 {
		t.Fatalf("double-remember created %d rows, want exactly 1", len(w.created))
	}
}

// AC4 (echo): the tool result feeds the agent its own pending proposals for the
// target so it can see what it has proposed this session.
func TestRememberKnowledge_ResultEchoesPending(t *testing.T) {
	w := &fakeKGWriter{
		ownRef: KGNodeRef{ID: "node-1", Name: "Gesa"}, ownOK: true,
		known: KnownForTarget{Pending: []string{"Gesa ist die Schwester von Arturus."}},
	}
	rk := NewRememberKnowledge(w)
	ctx := WithCaller(context.Background(), "agent-9")
	out, err := rk.Execute(ctx, json.RawMessage(`{"kind":"fact","fact":"Gesa hasst Spinnen"}`), cfgOwnNode)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "Gesa ist die Schwester von Arturus.") {
		t.Errorf("success result did not echo the agent's pending proposals: %q", out)
	}
}

// AC4 (hardening): the tool description tells the model not to re-propose known or
// already-proposed facts.
func TestRememberKnowledge_DescriptionHardened(t *testing.T) {
	d := strings.ToLower((&RememberKnowledge{}).Description())
	for _, want := range []string{"already", "not"} {
		if !strings.Contains(d, want) {
			t.Errorf("description missing %q guidance: %q", want, d)
		}
	}
}

// A dedup read hiccup must never drop the NPC's memory: the guard fails OPEN and
// the proposal is still created.
func TestRememberKnowledge_DedupReadErrorFailsOpen(t *testing.T) {
	w := &fakeKGWriter{
		ownRef: KGNodeRef{ID: "node-1", Name: "Gesa"}, ownOK: true,
		knownErr: context.DeadlineExceeded,
	}
	rk := NewRememberKnowledge(w)
	ctx := WithCaller(context.Background(), "agent-9")
	if _, err := rk.Execute(ctx, json.RawMessage(`{"kind":"fact","fact":"Gesa mag Kuchen"}`), cfgOwnNode); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(w.created) != 1 {
		t.Fatalf("dedup read error dropped the proposal: %d rows", len(w.created))
	}
}
