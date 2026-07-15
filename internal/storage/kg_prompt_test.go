//go:build integration

package storage_test

import (
	"context"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestPromptKG_NeverReturnsGMPrivate is the #450 seam pin: it seeds gm_private
// Nodes right where each prompt-facing read would surface them — a top-ranked
// private search hit and a private edge-neighbour — and drives EVERY read the
// [storage.PromptKGView] exposes, asserting zero leakage. Prompt assembly holds
// only this view (kgfacts Hot Context, the knowledge Tool adapter), so this test
// is the one place the "no gm_private row can reach a prompt" invariant is
// proven against a real DB; the per-call-site warning comments point here.
func TestPromptKG_NeverReturnsGMPrivate(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	view := st.PromptKG()

	// Public and private Nodes sharing search terms, so the private one would
	// rank in an unfiltered search.
	pub := mkNode(t, st, campaignID, storage.KGNodeLocation, "Harbor of Silverport")
	secret, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeLocation, Name: "Harbor smugglers' cache",
		Body: "GM only: the harbor cache holds the stolen crown.", GMPrivate: true,
	})
	if err != nil {
		t.Fatalf("CreateNode secret: %v", err)
	}

	// An NPC linked to an Agent, with one public and one gm_private neighbour.
	own := mkNode(t, st, campaignID, storage.KGNodeNPC, "Dockmaster Ilva")
	agentID := linkAgent(t, st, campaignID, own.ID, "Ilva")
	mkEdge(t, st, campaignID, own.ID, pub.ID, storage.KGEdgeResidesIn)
	mkEdge(t, st, campaignID, own.ID, secret.ID, storage.KGEdgeKnows)

	assertNoPrivate := func(op string, nodes []storage.KGNode, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		for _, n := range nodes {
			if n.GMPrivate {
				t.Errorf("%s leaked gm_private node %q through the prompt seam", op, n.Name)
			}
			if n.ID == secret.ID {
				t.Errorf("%s returned the seeded secret node %q", op, n.Name)
			}
		}
	}

	// Seam read 1: the prompt-facing search. The private Node matches "harbor"
	// too and must be excluded in the query, not post-hoc.
	hits, err := view.SearchPublicNodes(ctx, campaignID, "harbor", 10)
	assertNoPrivate("SearchPublicNodes", hits, err)
	if len(hits) != 1 || hits[0].ID != pub.ID {
		t.Errorf("SearchPublicNodes = %d hits, want exactly the public harbor node", len(hits))
	}

	// Seam read 2: the Agent's Hot Context neighbourhood. The private neighbour's
	// Edge exists but the Node must not surface.
	facts, err := view.AgentNodeFacts(ctx, agentID)
	assertNoPrivate("AgentNodeFacts", facts, err)
	got := nodeIDSet(facts)
	if got[own.ID] != 1 || got[pub.ID] != 1 || len(facts) != 2 {
		t.Errorf("AgentNodeFacts = %+v, want exactly own node + public neighbour", facts)
	}

	// Contrast: the GM-facing reads on *Store still see the private Node — the
	// filter lives in the seam, not in the data.
	gmHits, err := st.SearchNodes(ctx, campaignID, "harbor", 10)
	if err != nil {
		t.Fatalf("SearchNodes: %v", err)
	}
	if nodeIDSet(gmHits)[secret.ID] != 1 {
		t.Errorf("GM-facing SearchNodes no longer sees the gm_private node — the seam must filter, not the table")
	}
}
