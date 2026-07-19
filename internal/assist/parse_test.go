package assist

import (
	"strings"
	"testing"
)

// TestParseDraftHappyPath: a clean JSON draft parses with nodes and edges intact.
func TestParseDraftHappyPath(t *testing.T) {
	raw := `{"nodes":[
		{"type":"npc","name":"Bart","body":"An innkeeper.","gm_private":false},
		{"type":"location","name":"The Prancing Pony","body":"A tavern.","gm_private":false}
	],"edges":[{"from":0,"to":1,"type":"resides_in"}]}`
	d, err := parseDraft(raw)
	if err != nil {
		t.Fatalf("parseDraft: %v", err)
	}
	if len(d.Nodes) != 2 || len(d.Edges) != 1 {
		t.Fatalf("draft = %d nodes / %d edges, want 2/1", len(d.Nodes), len(d.Edges))
	}
	if d.Nodes[0].Type != "npc" || d.Nodes[0].Name != "Bart" {
		t.Errorf("node 0 = %+v, want npc Bart", d.Nodes[0])
	}
	if e := d.Edges[0]; e.FromIndex != 0 || e.ToIndex != 1 || e.Type != "resides_in" {
		t.Errorf("edge = %+v, want 0-resides_in->1", e)
	}
}

// TestParseDraftTolerantPackaging: code fences and surrounding prose around the
// JSON object are tolerated.
func TestParseDraftTolerantPackaging(t *testing.T) {
	raw := "Here is your draft:\n```json\n" +
		`{"nodes":[{"type":"note","name":"A note","body":"","gm_private":true}],"edges":[]}` +
		"\n```\nEnjoy!"
	d, err := parseDraft(raw)
	if err != nil {
		t.Fatalf("parseDraft: %v", err)
	}
	if len(d.Nodes) != 1 || d.Nodes[0].Name != "A note" || !d.Nodes[0].GMPrivate {
		t.Fatalf("draft nodes = %+v, want the one gm-private note", d.Nodes)
	}
}

// TestParseDraftDropsInvalidNodesAndRemapsEdges: a node with an unknown type or
// empty name is dropped WITH its incident edges, and surviving edge indices are
// remapped onto the filtered list.
func TestParseDraftDropsInvalidNodesAndRemapsEdges(t *testing.T) {
	raw := `{"nodes":[
		{"type":"kingdom","name":"Dropped — bad type"},
		{"type":"npc","name":"Gesa"},
		{"type":"npc","name":"   "},
		{"type":"faction","name":"The Guild"}
	],"edges":[
		{"from":0,"to":1,"type":"knows"},
		{"from":1,"to":3,"type":"member_of"},
		{"from":2,"to":3,"type":"member_of"}
	]}`
	d, err := parseDraft(raw)
	if err != nil {
		t.Fatalf("parseDraft: %v", err)
	}
	if len(d.Nodes) != 2 {
		t.Fatalf("nodes = %+v, want [Gesa, The Guild]", d.Nodes)
	}
	if len(d.Edges) != 1 {
		t.Fatalf("edges = %+v, want ONLY the remapped Gesa-member_of->Guild", d.Edges)
	}
	if e := d.Edges[0]; e.FromIndex != 0 || e.ToIndex != 1 || e.Type != "member_of" {
		t.Errorf("edge = %+v, want 0-member_of->1 after remap", e)
	}
}

// TestParseDraftDropsInvalidEdges: out-of-vocabulary, out-of-range, self,
// duplicate, and ADR-0008-matrix-invalid edges are dropped; the nodes survive.
func TestParseDraftDropsInvalidEdges(t *testing.T) {
	raw := `{"nodes":[
		{"type":"npc","name":"Bart"},
		{"type":"npc","name":"Gesa"}
	],"edges":[
		{"from":0,"to":1,"type":"married_to"},
		{"from":0,"to":9,"type":"knows"},
		{"from":0,"to":0,"type":"knows"},
		{"from":0,"to":1,"type":"resides_in"},
		{"from":0,"to":1,"type":"knows"},
		{"from":0,"to":1,"type":"knows"}
	]}`
	d, err := parseDraft(raw)
	if err != nil {
		t.Fatalf("parseDraft: %v", err)
	}
	if len(d.Edges) != 1 || d.Edges[0].Type != "knows" {
		t.Fatalf("edges = %+v, want exactly one knows edge (matrix/self/range/dup dropped)", d.Edges)
	}
}

// TestParseDraftCapsNodes: an over-long node list is truncated at maxDraftNodes
// and edges referencing the truncated tail are dropped.
func TestParseDraftCapsNodes(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"nodes":[`)
	for i := 0; i < maxDraftNodes+3; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"type":"note","name":"N` + strings.Repeat("x", i+1) + `"}`)
	}
	sb.WriteString(`],"edges":[{"from":0,"to":13,"type":"knows"}]}`)
	d, err := parseDraft(sb.String())
	if err != nil {
		t.Fatalf("parseDraft: %v", err)
	}
	if len(d.Nodes) != maxDraftNodes {
		t.Errorf("nodes = %d, want cap %d", len(d.Nodes), maxDraftNodes)
	}
	if len(d.Edges) != 0 {
		t.Errorf("edges = %+v, want the tail-referencing edge dropped", d.Edges)
	}
}

// TestParseDraftUnusable: no JSON at all, or nothing valid after filtering,
// errors — the engine maps this to ErrUnusableResponse.
func TestParseDraftUnusable(t *testing.T) {
	for name, raw := range map[string]string{
		"prose only":     "I cannot help with that.",
		"empty object":   "{}",
		"all invalid":    `{"nodes":[{"type":"widget","name":"X"}],"edges":[]}`,
		"unbalanced":     `{"nodes":[{"type":"note","name":"X"`,
		"non-json brace": "try { doSomething(); } finally",
	} {
		if _, err := parseDraft(raw); err == nil {
			t.Errorf("%s: parseDraft succeeded, want error", name)
		}
	}
}

// TestStripFences: a fenced persona loses its fences; unfenced text is untouched.
func TestStripFences(t *testing.T) {
	if got := stripFences("```markdown\nYou are Bart.\n```"); got != "You are Bart." {
		t.Errorf("fenced: got %q", got)
	}
	if got := stripFences("You are Bart."); got != "You are Bart." {
		t.Errorf("plain: got %q", got)
	}
}
