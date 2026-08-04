//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// linkAgent creates a Character Agent in the campaign and links it to the NPC Node,
// returning the Agent id. It fails the test on error.
func linkAgent(t *testing.T, st *storage.Store, campaignID, npcID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	agentID, err := st.CreateAgent(context.Background(), storage.NewAgent{
		CampaignID: campaignID, Role: storage.AgentRoleCharacter, Name: name,
	})
	if err != nil {
		t.Fatalf("CreateAgent %q: %v", name, err)
	}
	if _, err := st.SetNodeAgent(context.Background(), campaignID, npcID,
		uuid.NullUUID{UUID: agentID, Valid: true}); err != nil {
		t.Fatalf("SetNodeAgent %q: %v", name, err)
	}
	return agentID
}

// mkEdge creates a typed Edge between two same-Campaign Nodes, failing on error.
func mkEdge(t *testing.T, st *storage.Store, campaignID, from, to uuid.UUID, typ storage.KGEdgeType) storage.KGEdge {
	t.Helper()
	e, err := st.CreateEdge(context.Background(), storage.NewKGEdge{
		CampaignID: campaignID, FromNodeID: from, ToNodeID: to, Type: typ,
	})
	if err != nil {
		t.Fatalf("CreateEdge %s: %v", typ, err)
	}
	return e
}

// nodeIDSet projects a Node slice to id→count for membership assertions.
func nodeIDSet(nodes []storage.KGNode) map[uuid.UUID]int {
	m := map[uuid.UUID]int{}
	for _, n := range nodes {
		m[n.ID]++
	}
	return m
}

// TestAgentNodeFacts is #133 AC1: an NPC's Hot Context is its own linked Node's
// facts plus its edge-adjacent Nodes' facts (BOTH edge directions), and nothing
// further. Coverage: own public node surfaces first (hop 0); an outgoing and an
// incoming neighbour both surface; a gm_private neighbour is excluded even though
// its Edge exists (ADR-0008 amendment); a two-hop node is absent (strictly single
// hop); a multi-edge neighbour is deduped to one row (min hop).
func TestAgentNodeFacts(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	own := mkNode(t, st, campaignID, storage.KGNodeNPC, "Bart the Innkeeper")
	agentID := linkAgent(t, st, campaignID, own.ID, "Bart")

	// Outgoing neighbour: Bart resides_in The Inn (public Location).
	inn := mkNode(t, st, campaignID, storage.KGNodeLocation, "The Inn")
	mkEdge(t, st, campaignID, own.ID, inn.ID, storage.KGEdgeResidesIn)
	// A SECOND edge to the same neighbour must dedupe to one surfaced row.
	mkEdge(t, st, campaignID, own.ID, inn.ID, storage.KGEdgeKnows)

	// Incoming neighbour: Cyra knows Bart (edge points AT Bart).
	cyra := mkNode(t, st, campaignID, storage.KGNodeCharacter, "Cyra")
	mkEdge(t, st, campaignID, cyra.ID, own.ID, storage.KGEdgeKnows)

	// gm_private neighbour: Bart resides_in a hidden Location — the Edge exists but
	// the Node must NOT surface (ADR-0008 amendment: gm_private filters surfacing).
	secret, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeLocation, Name: "The Hidden Cellar",
		Body: "Where the smugglers meet.", GMPrivate: true,
	})
	if err != nil {
		t.Fatalf("CreateNode secret: %v", err)
	}
	mkEdge(t, st, campaignID, own.ID, secret.ID, storage.KGEdgeResidesIn)

	// Two-hop node: The Inn member_of a Faction. The Faction is a neighbour-of-a-
	// neighbour and must be absent (strictly single hop).
	guild := mkNode(t, st, campaignID, storage.KGNodeFaction, "The Innkeepers' Guild")
	mkEdge(t, st, campaignID, inn.ID, guild.ID, storage.KGEdgeMemberOf)

	facts, err := st.AgentNodeFacts(ctx, agentID)
	if err != nil {
		t.Fatalf("AgentNodeFacts: %v", err)
	}

	got := nodeIDSet(facts)
	// Own node + both-direction public neighbours surface.
	if got[own.ID] != 1 {
		t.Errorf("own node not surfaced exactly once: %v", got[own.ID])
	}
	if got[inn.ID] != 1 {
		t.Errorf("outgoing neighbour The Inn not surfaced exactly once (multi-edge dedup): %v", got[inn.ID])
	}
	if got[cyra.ID] != 1 {
		t.Errorf("incoming neighbour Cyra not surfaced: %v", got[cyra.ID])
	}
	// Excluded: gm_private neighbour and the two-hop Faction.
	if got[secret.ID] != 0 {
		t.Errorf("gm_private neighbour leaked into the fact set: %+v", facts)
	}
	if got[guild.ID] != 0 {
		t.Errorf("two-hop Faction surfaced — traversal is not strictly single hop: %+v", facts)
	}
	if len(facts) != 3 {
		t.Fatalf("fact set = %d nodes, want 3 (own + inn + cyra): %+v", len(facts), facts)
	}
	// Own node ranks first (hop 0), neighbours after (hop 1).
	if facts[0].ID != own.ID {
		t.Errorf("own node did not rank first: got %q", facts[0].Name)
	}
}

// TestAgentNodeFacts_GMPrivateOwnNode pins that traversal STARTS from the linked
// Node regardless of its gm_private: a gm_private OWN Node is excluded from
// surfacing, but its public neighbours still surface (the Edge is walked, only the
// Node's own visibility is filtered).
func TestAgentNodeFacts_GMPrivateOwnNode(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// The NPC's own Node is gm_private.
	own, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeNPC, Name: "The Masked Broker",
		Body: "A GM-only identity.", GMPrivate: true,
	})
	if err != nil {
		t.Fatalf("CreateNode own: %v", err)
	}
	agentID := linkAgent(t, st, campaignID, own.ID, "Broker")

	// A public neighbour of the hidden own Node.
	market := mkNode(t, st, campaignID, storage.KGNodeLocation, "The Night Market")
	mkEdge(t, st, campaignID, own.ID, market.ID, storage.KGEdgeResidesIn)

	facts, err := st.AgentNodeFacts(ctx, agentID)
	if err != nil {
		t.Fatalf("AgentNodeFacts: %v", err)
	}
	got := nodeIDSet(facts)
	if got[own.ID] != 0 {
		t.Errorf("gm_private own node surfaced: %+v", facts)
	}
	if got[market.ID] != 1 {
		t.Errorf("public neighbour of the hidden own node did not surface: %+v", facts)
	}
	if len(facts) != 1 {
		t.Fatalf("fact set = %d nodes, want 1 (the public neighbour only): %+v", len(facts), facts)
	}
}

// TestAgentNodeFacts_Unlinked pins that an Agent with no linked Node gets an empty
// fact set — a Character NPC that was never linked injects no wiki facts.
func TestAgentNodeFacts_Unlinked(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// A public Node exists in the campaign, but no Node links this Agent.
	mkNode(t, st, campaignID, storage.KGNodeLocation, "Unrelated Keep")
	agentID, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID: campaignID, Role: storage.AgentRoleCharacter, Name: "Nobody",
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	facts, err := st.AgentNodeFacts(ctx, agentID)
	if err != nil {
		t.Fatalf("AgentNodeFacts: %v", err)
	}
	if len(facts) != 0 {
		t.Errorf("unlinked agent got facts: %+v", facts)
	}
}

// TestAgentNodeFacts_NeutralNoteSurvivesTheReciprocalEdge is the tie-break the
// slice shipped inverted.
//
// Every INCOMING edge contributes a (”, 0) row — incoming edges expand the
// neighbourhood but carry no feeling, because how someone else feels about a Node
// is not this NPC's feeling. An ordinary reciprocal pair (Bart knows Mira, Mira
// knows Bart) therefore puts Bart's real note next to an empty row, both at
// abs(disposition) = 0. Ordering on abs alone tied them, and `note` ASC then
// picked ” — so the feature's own headline case, a NEUTRAL note ("owes me money
// since the siege"), silently never reached the Hot Context.
func TestAgentNodeFacts_NeutralNoteSurvivesTheReciprocalEdge(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	bart := mkNode(t, st, campaignID, storage.KGNodeNPC, "Bart")
	agentID := linkAgent(t, st, campaignID, bart.ID, "Bart")
	mira := mkNode(t, st, campaignID, storage.KGNodeNPC, "Mira")

	// Bart's own assertion, with texture but a NEUTRAL disposition.
	out := mkEdge(t, st, campaignID, bart.ID, mira.ID, storage.KGEdgeKnows)
	if _, err := st.UpdateEdgeDetails(ctx, campaignID, out.ID, "owes me money since the siege", 0); err != nil {
		t.Fatalf("UpdateEdgeDetails: %v", err)
	}
	// The ordinary reciprocal edge, which contributes ('', 0).
	mkEdge(t, st, campaignID, mira.ID, bart.ID, storage.KGEdgeKnows)

	facts, err := st.AgentNodeFacts(ctx, agentID)
	if err != nil {
		t.Fatalf("AgentNodeFacts: %v", err)
	}
	var found bool
	for _, n := range facts {
		if n.ID != mira.ID {
			continue
		}
		found = true
		if n.RelationNote != "owes me money since the siege" {
			t.Errorf("the note lost to the reciprocal edge's empty row: %q", n.RelationNote)
		}
	}
	if !found {
		t.Fatal("Mira did not surface at all")
	}
}

// TestAgentNodeFacts_StrongestFeelingStillWins: among edges that DO say
// something, the strongest disposition is still what surfaces — the "says
// something first" term must not have inverted that.
func TestAgentNodeFacts_StrongestFeelingStillWins(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	bart := mkNode(t, st, campaignID, storage.KGNodeNPC, "Bart")
	agentID := linkAgent(t, st, campaignID, bart.ID, "Bart")
	mira := mkNode(t, st, campaignID, storage.KGNodeNPC, "Mira")

	mild := mkEdge(t, st, campaignID, bart.ID, mira.ID, storage.KGEdgeKnows)
	if _, err := st.UpdateEdgeDetails(ctx, campaignID, mild.ID, "a passing acquaintance", 1); err != nil {
		t.Fatalf("UpdateEdgeDetails mild: %v", err)
	}
	strong := mkEdge(t, st, campaignID, bart.ID, mira.ID, storage.KGEdgeEnemyOf)
	if _, err := st.UpdateEdgeDetails(ctx, campaignID, strong.ID, "burned down the stables", -2); err != nil {
		t.Fatalf("UpdateEdgeDetails strong: %v", err)
	}

	facts, err := st.AgentNodeFacts(ctx, agentID)
	if err != nil {
		t.Fatalf("AgentNodeFacts: %v", err)
	}
	for _, n := range facts {
		if n.ID != mira.ID {
			continue
		}
		if n.RelationDisposition != -2 || n.RelationNote != "burned down the stables" {
			t.Fatalf("the strongest feeling did not win: note=%q disposition=%d",
				n.RelationNote, n.RelationDisposition)
		}
		return
	}
	t.Fatal("Mira did not surface at all")
}

// TestAppearances_DoNotChangeWhatAnNPCKnows is #545's AC4, and the reason the
// Appearances index is deliberately NOT an Edge.
//
// An Edge is world truth that AgentNodeFacts walks into an NPC's Hot Context. If
// a mention were modelled as one, "the party talked about the ogre" would become
// "the ogre is connected to the party" in the NPC's head — a fact nobody wrote,
// recited back at the table. So the index lives outside the graph, and this test
// pins that: index a session hard, and the NPC's facts are byte-identical.
func TestAppearances_DoNotChangeWhatAnNPCKnows(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	own := mkNode(t, st, campaignID, storage.KGNodeNPC, "Bart the innkeeper")
	agentID := linkAgent(t, st, campaignID, own.ID, "Bart")
	inn := mkNode(t, st, campaignID, storage.KGNodeLocation, "The Inn")
	mkEdge(t, st, campaignID, own.ID, inn.ID, storage.KGEdgeResidesIn)

	before, err := st.AgentNodeFacts(ctx, agentID)
	if err != nil {
		t.Fatalf("AgentNodeFacts before: %v", err)
	}
	edgesBefore := countRows(t, pool, "kg_edge", campaignID)

	// Record appearances for BOTH nodes, several times over.
	sess, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}
	var rows []storage.NodeAppearance
	for i, nodeID := range []uuid.UUID{own.ID, inn.ID, own.ID} {
		lineID := fmt.Sprintf("u:%d", i)
		if _, err := pool.Exec(ctx,
			`INSERT INTO transcript_line (voice_session_id, campaign_id, line_id, seq, who, kind, ts, text)
			 VALUES ($1,$2,$3,$4,'GM','gm',now(),'they spoke of it')
			 ON CONFLICT DO NOTHING`,
			sess.ID, campaignID, lineID, i); err != nil {
			t.Fatalf("seed line: %v", err)
		}
		rows = append(rows, storage.NodeAppearance{
			NodeID: nodeID, CampaignID: campaignID, VoiceSessionID: sess.ID,
			LineID: lineID, At: time.Now(),
		})
	}
	if err := st.RecordNodeAppearances(ctx, rows); err != nil {
		t.Fatalf("RecordNodeAppearances: %v", err)
	}

	after, err := st.AgentNodeFacts(ctx, agentID)
	if err != nil {
		t.Fatalf("AgentNodeFacts after: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("indexing changed the NPC's facts:\nbefore %+v\nafter  %+v", before, after)
	}
	if got := countRows(t, pool, "kg_edge", campaignID); got != edgesBefore {
		t.Errorf("indexing created %d edges", got-edgesBefore)
	}

	// And the index itself works: the entry knows where it was mentioned.
	hits, err := st.ListNodeAppearances(ctx, campaignID, own.ID, 0)
	if err != nil {
		t.Fatalf("ListNodeAppearances: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("appearances = %d, want the two lines Bart was named in", len(hits))
	}
	if hits[0].Text != "they spoke of it" || hits[0].Who != "GM" {
		t.Errorf("the line did not travel with the appearance: %+v", hits[0])
	}

	// Retraction (#437, ADR-0040): deleting a Line takes its appearances with it.
	// A GM who retracts something said at the table must not find it still listed.
	if _, err := pool.Exec(ctx,
		`DELETE FROM transcript_line WHERE voice_session_id = $1 AND line_id = 'u:0'`, sess.ID); err != nil {
		t.Fatalf("delete line: %v", err)
	}
	left, err := st.ListNodeAppearances(ctx, campaignID, own.ID, 0)
	if err != nil {
		t.Fatalf("ListNodeAppearances after retraction: %v", err)
	}
	if len(left) != 1 {
		t.Errorf("appearances after retracting a line = %d, want 1", len(left))
	}
}

// countRows counts a campaign's rows in a table.
func countRows(t *testing.T, pool *pgxpool.Pool, table string, campaignID uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM "+table+" WHERE campaign_id = $1", campaignID).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}
