//go:build integration

package storage_test

import (
	"context"
	"strings"
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

// TestPromptKG_NeverReturnsPrivateAspects is the #542 seam pin, the per-Aspect
// sibling of TestPromptKG_NeverReturnsGMPrivate. Aspects exist precisely so a
// PUBLIC Node can hold a private fact — which means the whole-Node filter above
// cannot protect it, and the guarantee has to be proven separately at the same
// seam. Both prompt-facing reads are driven against a public Node carrying one
// public and one private Aspect.
func TestPromptKG_NeverReturnsPrivateAspects(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	view := st.PromptKG()

	// A PUBLIC innkeeper with a public role and a GM-only secret — the exact case
	// that used to force the GM to split the character in two.
	bart := mkNode(t, st, campaignID, storage.KGNodeNPC, "Bart the innkeeper")
	if err := st.ReplaceNodeAspects(ctx, campaignID, bart.ID, []storage.NewKGNodeAspect{
		{Key: "Role", Value: "Runs the Rusty Anchor"},
		{Key: "Secret", Value: "Took the smugglers' bribe in Eastmonth", GMPrivate: true},
	}); err != nil {
		t.Fatalf("ReplaceNodeAspects: %v", err)
	}
	agentID := linkAgent(t, st, campaignID, bart.ID, "Bart")

	assertNoPrivateAspect := func(op string, nodes []storage.KGNode, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		var sawPublic bool
		for _, n := range nodes {
			for _, a := range n.Aspects {
				if a.GMPrivate {
					t.Errorf("%s leaked gm_private aspect %q on node %q through the prompt seam", op, a.Key, n.Name)
				}
				if strings.Contains(a.Value, "bribe") {
					t.Errorf("%s returned the seeded secret aspect through node %q", op, n.Name)
				}
				if a.Key == "Role" {
					sawPublic = true
				}
			}
		}
		if !sawPublic {
			t.Errorf("%s dropped the PUBLIC aspect too — the node must keep its public facts", op)
		}
	}

	facts, err := view.AgentNodeFacts(ctx, agentID)
	assertNoPrivateAspect("AgentNodeFacts", facts, err)
	hits, err := view.SearchPublicNodes(ctx, campaignID, "innkeeper", 10)
	assertNoPrivateAspect("SearchPublicNodes", hits, err)

	// Contrast: the GM-facing read still carries the private Aspect, so the filter
	// is proven to live in the seam rather than in the table.
	gm, err := st.ListNodes(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	var sawSecret bool
	for _, n := range gm {
		for _, a := range n.Aspects {
			if a.GMPrivate && strings.Contains(a.Value, "bribe") {
				sawSecret = true
			}
		}
	}
	if !sawSecret {
		t.Error("GM-facing ListNodes no longer sees the private aspect — the seam must filter, not the table")
	}
}
