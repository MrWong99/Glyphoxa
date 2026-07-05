package storage_test

import (
	"errors"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestValidateEdge pins the ADR-0008 (2026-07-04 amendment) object-side-only
// validity matrix EXACTLY. Structural edge types constrain their target (and
// parent_of both ends); the subject side of the structural types and every
// social/loose type (knows, owns, enemy_of, ally_of, mentioned_in) accept any
// Node type — a sentient sword may know a king, a ghost may reside in a tavern.
// Pure function, no DB: it runs in the fast unit gate.
func TestValidateEdge(t *testing.T) {
	t.Parallel()

	const (
		char = storage.KGNodeCharacter
		npc  = storage.KGNodeNPC
		loc  = storage.KGNodeLocation
		fac  = storage.KGNodeFaction
		item = storage.KGNodeItem
		plot = storage.KGNodePlotThread
		note = storage.KGNodeNote
	)
	allTypes := []storage.KGNodeType{char, npc, loc, fac, item, plot, note}

	cases := []struct {
		name    string
		edge    storage.KGEdgeType
		from    storage.KGNodeType
		to      storage.KGNodeType
		wantErr bool
	}{
		// resides_in constrains only the target to Location; the subject is free.
		{"resides_in → location ok (char subject)", storage.KGEdgeResidesIn, char, loc, false},
		{"resides_in → location ok (item subject: sentient sword)", storage.KGEdgeResidesIn, item, loc, false},
		{"resides_in → location ok (ghost note subject)", storage.KGEdgeResidesIn, note, loc, false},
		{"resides_in → faction rejected", storage.KGEdgeResidesIn, char, fac, true},
		{"resides_in → npc rejected", storage.KGEdgeResidesIn, char, npc, true},

		// member_of constrains only the target to Faction.
		{"member_of → faction ok (npc subject)", storage.KGEdgeMemberOf, npc, fac, false},
		{"member_of → faction ok (item subject)", storage.KGEdgeMemberOf, item, fac, false},
		{"member_of → location rejected", storage.KGEdgeMemberOf, char, loc, true},

		// participated_in constrains only the target to PlotThread.
		{"participated_in → plot ok (char subject)", storage.KGEdgeParticipatedIn, char, plot, false},
		{"participated_in → plot ok (faction subject)", storage.KGEdgeParticipatedIn, fac, plot, false},
		{"participated_in → note rejected", storage.KGEdgeParticipatedIn, char, note, true},

		// parent_of constrains BOTH ends to Character/NPC.
		{"parent_of char→char ok", storage.KGEdgeParentOf, char, char, false},
		{"parent_of npc→char ok", storage.KGEdgeParentOf, npc, char, false},
		{"parent_of char→npc ok", storage.KGEdgeParentOf, char, npc, false},
		{"parent_of npc→npc ok", storage.KGEdgeParentOf, npc, npc, false},
		{"parent_of location→char rejected (bad subject)", storage.KGEdgeParentOf, loc, char, true},
		{"parent_of char→location rejected (bad target)", storage.KGEdgeParentOf, char, loc, true},
		{"parent_of item→item rejected", storage.KGEdgeParentOf, item, item, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := storage.ValidateEdge(tc.edge, tc.from, tc.to)
			if tc.wantErr && !errors.Is(err, storage.ErrInvalidEdge) {
				t.Fatalf("ValidateEdge(%s, %s, %s) = %v, want ErrInvalidEdge", tc.edge, tc.from, tc.to, err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateEdge(%s, %s, %s) = %v, want nil", tc.edge, tc.from, tc.to, err)
			}
		})
	}

	// The five loose types accept EVERY from/to combination — no typo protection,
	// by design (the domain legitimately contains anything-relates-to-anything
	// social facts). Assert exhaustively so a future "tighten a loose type" change
	// trips this test.
	loose := []storage.KGEdgeType{
		storage.KGEdgeKnows, storage.KGEdgeOwns, storage.KGEdgeEnemyOf,
		storage.KGEdgeAllyOf, storage.KGEdgeMentionedIn,
	}
	for _, e := range loose {
		for _, from := range allTypes {
			for _, to := range allTypes {
				if err := storage.ValidateEdge(e, from, to); err != nil {
					t.Errorf("loose edge %s(%s→%s) = %v, want nil (loose types are unconstrained)", e, from, to, err)
				}
			}
		}
	}
}
