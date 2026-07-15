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
	if kgvocab.ProposalWriteVersion != 1 {
		t.Errorf("ProposalWriteVersion = %d, want 1 (ADR-0052)", kgvocab.ProposalWriteVersion)
	}
}
