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
