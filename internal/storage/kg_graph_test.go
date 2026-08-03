//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestListGraphNodes exercises the whole-campaign graph read against a real DB
// (#534): the projection, the content signals, the display order, and the
// campaign scope. The RPC test drives a fake that returns rows verbatim, so a
// wrong column or a scope typo in this SQL would otherwise never execute.
func TestListGraphNodes(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// Names chosen so the (node_type, lower(name), id) order is observable and is
	// NOT the insertion order: "bart" lowercase must sort before "Zara".
	npc := mkNode(t, st, campaignID, storage.KGNodeNPC, "Zara")
	bart, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeNPC, Name: "bart", Body: "An innkeeper.",
	})
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	secret, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeLocation, Name: "The cache", GMPrivate: true,
	})
	if err != nil {
		t.Fatalf("CreateNode secret: %v", err)
	}
	setAspects(t, st, campaignID, bart.ID,
		storage.NewKGNodeAspect{Key: "Role", Value: "Runs the inn"},
		storage.NewKGNodeAspect{Key: "Secret", Value: "Bribed", GMPrivate: true},
	)
	agentID := linkAgent(t, st, campaignID, npc.ID, "Zara")

	// A second campaign's Node must not appear.
	_, _, otherCampaign := seedCampaign(t, dsn)
	if _, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: otherCampaign, Type: storage.KGNodeNote, Name: "Elsewhere",
	}); err != nil {
		t.Fatalf("CreateNode other campaign: %v", err)
	}

	got, err := st.ListGraphNodes(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListGraphNodes: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d nodes, want exactly this campaign's 3: %+v", len(got), got)
	}

	// NPC sorts before Location in the enum, and within NPC the case-insensitive
	// name order puts "bart" first.
	if got[0].Name != "bart" || got[1].Name != "Zara" || got[2].Name != "The cache" {
		t.Errorf("order = %q/%q/%q, want (node_type, lower(name)) ordering",
			got[0].Name, got[1].Name, got[2].Name)
	}

	// The content signals are what the health panel and readiness marks read; they
	// must count BOTH private and public aspects, since an entry with only a secret
	// is still an authored entry.
	if got[0].BodyLen != len("An innkeeper.") {
		t.Errorf("body_len = %d, want %d", got[0].BodyLen, len("An innkeeper."))
	}
	if got[0].AspectCount != 2 {
		t.Errorf("aspect_count = %d, want 2 (private aspects counted)", got[0].AspectCount)
	}
	if got[1].BodyLen != 0 || got[1].AspectCount != 0 {
		t.Errorf("an empty entry reported body_len %d / aspect_count %d, want 0/0",
			got[1].BodyLen, got[1].AspectCount)
	}

	if !got[1].AgentID.Valid || got[1].AgentID.UUID != agentID {
		t.Errorf("agent link = %+v, want the linked agent %s", got[1].AgentID, agentID)
	}
	if got[0].AgentID.Valid {
		t.Errorf("unlinked node reported an agent: %+v", got[0].AgentID)
	}

	// GM-facing: gm_private is returned, flagged, for the client to draw.
	if !got[2].GMPrivate || got[2].ID != secret.ID {
		t.Errorf("gm_private node = %+v, want it present and flagged", got[2])
	}

	// An empty campaign is an empty slice, not an error — a brand-new campaign
	// opens the Graph tab too.
	empty, err := st.ListGraphNodes(ctx, otherCampaign)
	if err != nil {
		t.Fatalf("ListGraphNodes (other): %v", err)
	}
	if len(empty) != 1 || empty[0].Name != "Elsewhere" {
		t.Errorf("other campaign = %+v, want only its own node", empty)
	}
	if len(mustGraphNodes(t, st, uuid.New())) != 0 {
		t.Error("an unknown campaign returned nodes")
	}
}

func mustGraphNodes(t *testing.T, st *storage.Store, campaignID uuid.UUID) []storage.KGGraphNode {
	t.Helper()
	got, err := st.ListGraphNodes(context.Background(), campaignID)
	if err != nil {
		t.Fatalf("ListGraphNodes: %v", err)
	}
	return got
}

// TestSimilarNodePairs exercises the GM-initiated duplicate scan against a real
// DB (#536): each unordered pair once, closest first, above the floor, scoped to
// the campaign, and blind to rows the embedding worker has not reached.
func TestSimilarNodePairs(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// A and B embed on the same axis (identical direction ⇒ similarity 1); C is
	// orthogonal; D is left unembedded and must be invisible to the scan.
	a := mkNode(t, st, campaignID, storage.KGNodeNote, "Bart")
	b := mkNode(t, st, campaignID, storage.KGNodeNote, "Bart the innkeeper")
	c := mkNode(t, st, campaignID, storage.KGNodeNote, "Something else")
	d := mkNode(t, st, campaignID, storage.KGNodeNote, "Not yet indexed")
	for _, n := range []struct {
		node storage.KGNode
		axis int
	}{{a, 0}, {b, 0}, {c, 1}} {
		if err := st.SetNodeEmbedding(ctx, n.node.ID, unitVec(n.axis), "m", n.node.UpdatedAt); err != nil {
			t.Fatalf("SetNodeEmbedding: %v", err)
		}
	}

	pairs, err := st.SimilarNodePairs(ctx, campaignID, 0.9, 25)
	if err != nil {
		t.Fatalf("SimilarNodePairs: %v", err)
	}
	if len(pairs) != 1 {
		t.Fatalf("got %d pairs, want exactly the A/B pair: %+v", len(pairs), pairs)
	}
	got := map[uuid.UUID]bool{pairs[0].AID: true, pairs[0].BID: true}
	if !got[a.ID] || !got[b.ID] {
		t.Errorf("pair = %+v, want the two same-axis nodes", pairs[0])
	}
	if pairs[0].AID == pairs[0].BID {
		t.Error("a node was paired with itself")
	}
	if pairs[0].Similarity < 0.99 {
		t.Errorf("similarity = %v, want ~1 for identical vectors", pairs[0].Similarity)
	}
	if pairs[0].AName == "" || pairs[0].BName == "" {
		t.Errorf("pair carries no names: %+v", pairs[0])
	}

	// The orthogonal node is below the floor; the unembedded one is invisible.
	for _, p := range pairs {
		if p.AID == c.ID || p.BID == c.ID {
			t.Error("an orthogonal node was reported as a probable duplicate")
		}
		if p.AID == d.ID || p.BID == d.ID {
			t.Error("an unembedded node appeared in the scan")
		}
	}

	// The unembedded count is what stops "no duplicates" from implying a clean
	// bill of health the scan could not give.
	n, err := st.CountUnembeddedNodesInCampaign(ctx, campaignID)
	if err != nil {
		t.Fatalf("CountUnembeddedNodesInCampaign: %v", err)
	}
	if n != 1 {
		t.Errorf("unembedded = %d, want 1", n)
	}

	// Another campaign's identical pair must not surface here.
	_, _, other := seedCampaign(t, dsn)
	oa := mkNode(t, st, other, storage.KGNodeNote, "Bart")
	ob := mkNode(t, st, other, storage.KGNodeNote, "Bart again")
	for _, n := range []storage.KGNode{oa, ob} {
		if err := st.SetNodeEmbedding(ctx, n.ID, unitVec(0), "m", n.UpdatedAt); err != nil {
			t.Fatalf("SetNodeEmbedding (other): %v", err)
		}
	}
	again, err := st.SimilarNodePairs(ctx, campaignID, 0.9, 25)
	if err != nil {
		t.Fatalf("SimilarNodePairs (rescan): %v", err)
	}
	if len(again) != 1 {
		t.Errorf("got %d pairs after seeding another campaign, want the scan campaign-scoped", len(again))
	}
}

// TestLastSpokenByAgent pins the prep dashboard's "last spoke" signal (#544): one
// grouped read for the whole roster, Agent turns only, most recent per speaker.
func TestLastSpokenByAgent(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)
	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}
	sessionID := vs.ID

	base := time.Now().UTC().Truncate(time.Second)
	lines := []struct {
		who, kind string
		at        time.Time
	}{
		{"Bart", "npc", base.Add(-3 * time.Hour)},
		{"Bart", "npc", base.Add(-1 * time.Hour)}, // the most recent Bart line
		{"Glyphoxa", "butler", base.Add(-2 * time.Hour)},
		{"Lukas", "player", base}, // a human turn — a different question
	}
	for i, l := range lines {
		if err := st.UpsertTranscriptLine(ctx, storage.TranscriptLine{
			VoiceSessionID: sessionID, CampaignID: campaignID,
			LineID: fmt.Sprintf("l%d", i), Seq: int64(i),
			Who: l.who, Kind: l.kind, TS: l.at, Text: "…",
		}); err != nil {
			t.Fatalf("UpsertTranscriptLine: %v", err)
		}
	}

	got, err := st.LastSpokenByAgent(ctx, campaignID)
	if err != nil {
		t.Fatalf("LastSpokenByAgent: %v", err)
	}
	byWho := map[string]time.Time{}
	for _, g := range got {
		byWho[g.Who] = g.At
	}
	if len(byWho) != 2 {
		t.Fatalf("got %v, want exactly the two Agent speakers", byWho)
	}
	if _, ok := byWho["Lukas"]; ok {
		t.Error("a player turn was reported as an Agent speaking")
	}
	if !byWho["Bart"].Equal(base.Add(-1 * time.Hour)) {
		t.Errorf("Bart's last line = %v, want the most recent one", byWho["Bart"])
	}
	if !byWho["Glyphoxa"].Equal(base.Add(-2 * time.Hour)) {
		t.Errorf("Butler's last line = %v", byWho["Glyphoxa"])
	}

	// Another campaign's lines must not leak in.
	_, _, other := seedCampaign(t, dsn)
	if empty, err := st.LastSpokenByAgent(ctx, other); err != nil || len(empty) != 0 {
		t.Errorf("other campaign = %v (err %v), want empty", empty, err)
	}
}
