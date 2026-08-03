package kgvocab_test

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/kgvocab"
)

// TestRelations pins the closed relation vocabulary: canonical order, validity,
// and the returned slice being a private copy.
func TestRelations(t *testing.T) {
	want := []string{
		kgvocab.RelationResidesIn, kgvocab.RelationMemberOf, kgvocab.RelationOwns,
		kgvocab.RelationKnows, kgvocab.RelationEnemyOf, kgvocab.RelationAllyOf,
		kgvocab.RelationParentOf, kgvocab.RelationParticipatedIn, kgvocab.RelationMentionedIn,
	}
	got := kgvocab.Relations()
	if len(got) != len(want) {
		t.Fatalf("Relations() has %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Relations()[%d] = %q, want %q", i, got[i], want[i])
		}
		if !kgvocab.ValidRelation(want[i]) {
			t.Errorf("ValidRelation(%q) = false, want true", want[i])
		}
	}
	if kgvocab.ValidRelation("loves") || kgvocab.ValidRelation("") {
		t.Error("ValidRelation accepted a value outside the closed vocabulary")
	}

	got[0] = "mutated"
	if kgvocab.Relations()[0] != kgvocab.RelationResidesIn {
		t.Error("Relations() must return a copy; mutating the result changed the vocabulary")
	}
}

// TestNodeTypes pins the closed node-type vocabulary and its validity check.
func TestNodeTypes(t *testing.T) {
	want := []string{
		kgvocab.NodeTypeCharacter, kgvocab.NodeTypeNPC, kgvocab.NodeTypeLocation,
		kgvocab.NodeTypeFaction, kgvocab.NodeTypeItem, kgvocab.NodeTypePlotThread,
		kgvocab.NodeTypeNote,
	}
	got := kgvocab.NodeTypes()
	if len(got) != len(want) {
		t.Fatalf("NodeTypes() has %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("NodeTypes()[%d] = %q, want %q", i, got[i], want[i])
		}
		if !kgvocab.ValidNodeType(want[i]) {
			t.Errorf("ValidNodeType(%q) = false, want true", want[i])
		}
	}
	if kgvocab.ValidNodeType("dragon") || kgvocab.ValidNodeType("") {
		t.Error("ValidNodeType accepted a value outside the closed vocabulary")
	}
}

// TestNodeTypeLabel pins the GM-facing label for EVERY node type — the one map
// kgfacts and knowledge consume — plus the defensive "Note" fallback.
func TestNodeTypeLabel(t *testing.T) {
	want := map[string]string{
		kgvocab.NodeTypeCharacter:  "Character",
		kgvocab.NodeTypeNPC:        "NPC",
		kgvocab.NodeTypeLocation:   "Location",
		kgvocab.NodeTypeFaction:    "Faction",
		kgvocab.NodeTypeItem:       "Item",
		kgvocab.NodeTypePlotThread: "Plot thread",
		kgvocab.NodeTypeNote:       "Note",
	}
	for typ, label := range want {
		if got := kgvocab.NodeTypeLabel(typ); got != label {
			t.Errorf("NodeTypeLabel(%q) = %q, want %q", typ, got, label)
		}
	}
	if got := kgvocab.NodeTypeLabel("dragon"); got != "Note" {
		t.Errorf("NodeTypeLabel(unknown) = %q, want the defensive Note fallback", got)
	}
}

// TestKindsAndVersion pins the proposal kind identifiers and the write version —
// the values the create path stamps and the approve/review paths check.
func TestKindsAndVersion(t *testing.T) {
	if kgvocab.KindFact != "fact" || kgvocab.KindEdge != "edge" || kgvocab.KindNode != "node" {
		t.Errorf("kinds = %q/%q/%q, want fact/edge/node (the on-disk contract)",
			kgvocab.KindFact, kgvocab.KindEdge, kgvocab.KindNode)
	}
	// v2 is the aspect-carrying fact payload (#542). Bumping it is the mechanism
	// that makes a stored v1 row unreadable rather than misread, so the constant is
	// pinned here: changing it silently would let an old proposal land under a shape
	// its author never wrote.
	if kgvocab.ProposalWriteVersion != 2 {
		t.Errorf("ProposalWriteVersion = %d, want 2 (ADR-0052, #542 aspect payload)", kgvocab.ProposalWriteVersion)
	}
}

// TestAspectBounds pins the Aspect caps every side of the contract shares — the
// Tool create path, the RPC editor path and the renderer all validate against
// these, so a drift here would let one side admit what another rejects.
func TestAspectBounds(t *testing.T) {
	if kgvocab.MaxAspectKeyRunes <= 0 || kgvocab.MaxAspectKeyRunes >= kgvocab.MaxAspectValueRunes {
		t.Errorf("MaxAspectKeyRunes = %d, want a positive cap well below MaxAspectValueRunes = %d",
			kgvocab.MaxAspectKeyRunes, kgvocab.MaxAspectValueRunes)
	}
	if kgvocab.MaxAspectsPerNode <= 0 {
		t.Errorf("MaxAspectsPerNode = %d, want a positive cap", kgvocab.MaxAspectsPerNode)
	}
	if kgvocab.DefaultAspectKey == "" {
		t.Error("DefaultAspectKey must be non-empty: a v2 fact payload always carries a key")
	}
}
