package tool

import "testing"

// ProposalSalient projects a proposed write onto the free text a dedup compares.
func TestProposalSalient(t *testing.T) {
	cases := []struct {
		name string
		w    ProposedWrite
		want string
	}{
		{"fact is its fact text", ProposedWrite{Kind: "fact", Subject: "Gesa", Fact: "ist die Schwester"}, "ist die Schwester"},
		{"edge is relation and target", ProposedWrite{Kind: "edge", Subject: "Gesa", Relation: "parent_of", Target: "Arturus"}, "parent_of Arturus"},
		{"node is name and body", ProposedWrite{Kind: "node", Name: "Ironhold", Body: "a smiths' guild"}, "Ironhold a smiths' guild"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ProposalSalient(tc.w); got != tc.want {
				t.Errorf("ProposalSalient = %q, want %q", got, tc.want)
			}
		})
	}
}

// ProposalTargetKey identifies the entity a proposal is about, so dedup only
// compares proposals addressing the SAME target (not a coincidental text clash
// across two different subjects).
func TestProposalTargetKey(t *testing.T) {
	// An own_node proposal is keyed by its anchor node id, regardless of subject.
	if k := ProposalTargetKey(ProposedWrite{Kind: "fact", NodeID: "n1", Subject: "Gesa", Fact: "x"}); k != "id:n1" {
		t.Errorf("own-node key = %q, want id:n1", k)
	}
	// A campaign proposal with no anchor is keyed by its normalized subject, so
	// casing/punctuation variants of the same subject collapse together.
	a := ProposalTargetKey(ProposedWrite{Kind: "fact", Subject: "The Duke", Fact: "x"})
	b := ProposalTargetKey(ProposedWrite{Kind: "fact", Subject: "the duke!", Fact: "y"})
	if a == "" || a != b {
		t.Errorf("subject keys differ: %q vs %q", a, b)
	}
	// A new-entry proposal is keyed by the new entry's own name.
	if k := ProposalTargetKey(ProposedWrite{Kind: "node", Name: "Ironhold"}); k == "" {
		t.Error("node key empty")
	}
	// Two different subjects must NOT share a key.
	if ProposalTargetKey(ProposedWrite{Subject: "Gesa"}) == ProposalTargetKey(ProposedWrite{Subject: "Arturus"}) {
		t.Error("distinct subjects share a target key")
	}
}

// firstKnownMatch returns the raw existing text that is normalized-equal to the
// salient text — the exact/normalized write-time guard (#411, mechanism a).
func TestFirstKnownMatch(t *testing.T) {
	known := []string{"Gesa ist die Schwester von Arturus.", "Gesa mag Kuchen"}
	// A casing/punctuation variant of a known fact matches and echoes the known text.
	if m, ok := firstKnownMatch("gesa ist die schwester von arturus", known); !ok || m != known[0] {
		t.Errorf("normalized variant did not match: got %q ok=%v", m, ok)
	}
	// A genuinely new fact matches nothing.
	if m, ok := firstKnownMatch("Gesa hasst Spinnen", known); ok {
		t.Errorf("new fact matched %q", m)
	}
	// An empty salient never matches (guards a proposal with no comparable text).
	if _, ok := firstKnownMatch("", known); ok {
		t.Error("empty salient matched")
	}
}
