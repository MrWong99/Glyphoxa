package main

import (
	"os"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/bundle"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// demoBundlePath is the canonical demo bundle shipped for `seed -bundle` and the
// container smoke test, relative to this package.
const demoBundlePath = "../../scripts/testdata/demo.glyphoxa.json"

// TestDemoBundleFixtureValid is the fixture-validity guard (TEST 6): it decodes
// the committed demo bundle and resolves every intra-bundle reference the way the
// importer will, WITHOUT a database. It fails loudly if the fixture goes stale —
// most importantly on a FormatVersion bump (bundle.Decode refuses a newer
// version), but also on a dangling node→agent link, an edge to a missing node, or
// a voice blob the canonical mapper can't parse. Keeping this Docker-free means CI
// catches a broken demo bundle on the fast `test` gate, not only in the smoke job.
func TestDemoBundleFixtureValid(t *testing.T) {
	f, err := os.Open(demoBundlePath)
	if err != nil {
		t.Fatalf("open demo bundle: %v", err)
	}
	defer f.Close()

	b, err := bundle.Decode(f)
	if err != nil {
		t.Fatalf("decode demo bundle: %v", err)
	}

	if b.Campaign.Name != "The Prancing Pony" {
		t.Errorf("campaign name = %q, want %q", b.Campaign.Name, "The Prancing Pony")
	}
	if b.Campaign.System != "dnd5e" || b.Campaign.Language != "en" {
		t.Errorf("campaign system/language = %q/%q, want dnd5e/en", b.Campaign.System, b.Campaign.Language)
	}
	if b.Campaign.History != nil {
		t.Errorf("demo bundle carries a History section; it must be history-less")
	}

	// Agents: exactly one Butler, at least one Character, and every voice parses
	// through the SINGLE canonical reader (catches a drifted voice shape, #224).
	agentRefs := make(map[string]bool, len(b.Campaign.Agents))
	butlers, characters := 0, 0
	for i := range b.Campaign.Agents {
		a := &b.Campaign.Agents[i]
		if agentRefs[a.ID] {
			t.Fatalf("duplicate agent ref %q", a.ID)
		}
		agentRefs[a.ID] = true
		switch a.Role {
		case string(storage.AgentRoleButler):
			butlers++
		case string(storage.AgentRoleCharacter):
			characters++
		default:
			t.Errorf("agent %q has unknown role %q", a.ID, a.Role)
		}
		if _, err := storage.VoiceFromJSON(a.Voice); err != nil {
			t.Errorf("agent %q voice does not parse through VoiceFromJSON: %v", a.ID, err)
		}
	}
	if butlers != 1 {
		t.Errorf("butler count = %d, want exactly 1", butlers)
	}
	if characters < 1 {
		t.Errorf("character NPC count = %d, want at least 1", characters)
	}

	// Nodes: unique refs; each node→agent link resolves to a real agent.
	nodeRefs := make(map[string]bool, len(b.Campaign.Nodes))
	linkedNPCNode := false
	for i := range b.Campaign.Nodes {
		n := &b.Campaign.Nodes[i]
		if nodeRefs[n.ID] {
			t.Fatalf("duplicate node ref %q", n.ID)
		}
		nodeRefs[n.ID] = true
		if n.AgentID != "" {
			if !agentRefs[n.AgentID] {
				t.Errorf("node %q links unknown agent %q", n.ID, n.AgentID)
			}
			linkedNPCNode = true
		}
	}
	if !linkedNPCNode {
		t.Errorf("no node links to an agent; demo should include an NPC-node agent link")
	}
	if len(b.Campaign.Nodes) < 3 {
		t.Errorf("node count = %d, want 3-4", len(b.Campaign.Nodes))
	}

	// Edges: at least two, and each endpoint resolves + validates the ADR-0008
	// object-side rule the importer's CreateEdge enforces.
	if len(b.Campaign.Edges) < 2 {
		t.Errorf("edge count = %d, want at least 2", len(b.Campaign.Edges))
	}
	nodeType := make(map[string]storage.KGNodeType, len(b.Campaign.Nodes))
	for i := range b.Campaign.Nodes {
		nodeType[b.Campaign.Nodes[i].ID] = storage.KGNodeType(b.Campaign.Nodes[i].Type)
	}
	for i := range b.Campaign.Edges {
		e := &b.Campaign.Edges[i]
		if !nodeRefs[e.From] {
			t.Errorf("edge references unknown from-node %q", e.From)
		}
		if !nodeRefs[e.To] {
			t.Errorf("edge references unknown to-node %q", e.To)
		}
		if err := storage.ValidateEdge(storage.KGEdgeType(e.Type), nodeType[e.From], nodeType[e.To]); err != nil {
			t.Errorf("edge %s->%s (%s) invalid: %v", e.From, e.To, e.Type, err)
		}
	}

	if len(b.Campaign.Characters) < 1 {
		t.Errorf("player character count = %d, want at least 1", len(b.Campaign.Characters))
	}
}
