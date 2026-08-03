package storage

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Knowledge Graph whole-campaign read (#534, ADR-0008 amendment): the payload the
// Campaign screen's Graph view renders. Edges were authorable but never displayed,
// so the GM could not see the structure they built — and Edges are exactly what
// AgentNodeFacts walks to fill an NPC's Hot Context, which made poor edge hygiene
// degrade every NPC invisibly.
//
// It is deliberately ONE read per side (nodes by campaign, edges by campaign), not
// 300 per-node round trips, and it carries no prose: a campaign is hundreds of
// Nodes, and the graph needs their shape, not their content.

// KGGraphNode is one Node as the graph view needs it: identity, type, name and the
// two flags the rendering distinguishes on — WITHOUT body or aspect text. BodyLen
// and AspectCount are the "does this entry actually say anything" signal the health
// panel and the readiness marks derive from; sending the text itself would multiply
// the payload for something no node glyph renders.
type KGGraphNode struct {
	ID          uuid.UUID
	Type        KGNodeType
	Name        string
	GMPrivate   bool
	AgentID     uuid.NullUUID
	BodyLen     int
	AspectCount int
}

// ListGraphNodes returns every Node in a Campaign in the display order ListNodes
// uses (type, case-insensitive name, id), projected for the graph view. The order
// is stable and content-independent, which is what lets the client's layout be a
// pure function of the payload.
//
// This is a GM-FACING read: gm_private Nodes are included, because the Graph view
// is the GM's map of their own world. It is deliberately NOT part of [PromptKGView]
// — prompt assembly cannot reach it (#450).
func (s *Store) ListGraphNodes(ctx context.Context, campaignID uuid.UUID) ([]KGGraphNode, error) {
	rows, err := s.db.Query(ctx,
		`SELECT n.id, n.node_type, n.name, n.gm_private, n.agent_id,
		        length(n.body),
		        (SELECT count(*) FROM kg_node_aspect a WHERE a.node_id = n.id)
		   FROM kg_node n
		  WHERE n.campaign_id = $1
		  ORDER BY n.node_type, lower(n.name), n.id`, campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: list graph nodes for campaign %s: %w", campaignID, err)
	}
	defer rows.Close()

	var out []KGGraphNode
	for rows.Next() {
		var n KGGraphNode
		if err := rows.Scan(&n.ID, &n.Type, &n.Name, &n.GMPrivate, &n.AgentID, &n.BodyLen, &n.AspectCount); err != nil {
			return nil, fmt.Errorf("storage: scan graph node: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list graph nodes for campaign %s: %w", campaignID, err)
	}
	return out, nil
}

// KGNodePair is two Nodes in one Campaign whose embeddings are close — a
// PROBABLE-duplicate hint for the world health panel (#536). Similarity is 1
// minus cosine distance, so 1.0 is identical text.
type KGNodePair struct {
	AID, BID     uuid.UUID
	AName, BName string
	Similarity   float64
}

// SimilarNodePairs returns the Campaign's closest Node pairs by embedding cosine
// similarity, closest first (#536, ADR-0011). It is the "probable duplicates"
// derivation the health panel offers — a HINT the GM acts on, never an auto-merge:
// ADR-0052 rejected auto-merging near-duplicates because similarity is not a
// semantic judgment and a wrong merge corrupts canon invisibly. The same reasoning
// applies here, so this read exists only to point at pairs.
//
// It is an exact pairwise scan rather than an ANN probe: a Campaign is hundreds of
// Nodes, the read is GM-INITIATED (never on render), and an exact answer beats an
// approximate one for something a human is about to act on. Rows without an
// embedding are invisible until the backfill worker reaches them.
//
// minSimilarity is the cosine-similarity floor in [0,1]; limit caps the result.
func (s *Store) SimilarNodePairs(ctx context.Context, campaignID uuid.UUID, minSimilarity float64, limit int) ([]KGNodePair, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("storage: similar node pairs: limit must be > 0, got %d", limit)
	}
	// b.id > a.id yields each unordered pair exactly once and rules out self-pairs.
	rows, err := s.db.Query(ctx,
		`SELECT a.id, a.name, b.id, b.name, 1 - (a.embedding <=> b.embedding) AS similarity
		   FROM kg_node a
		   JOIN kg_node b
		     ON b.campaign_id = a.campaign_id AND b.id > a.id AND b.embedding IS NOT NULL
		  WHERE a.campaign_id = $1
		    AND a.embedding IS NOT NULL
		    AND 1 - (a.embedding <=> b.embedding) >= $2
		  ORDER BY similarity DESC, a.id, b.id
		  LIMIT $3`, campaignID, minSimilarity, limit)
	if err != nil {
		return nil, fmt.Errorf("storage: similar node pairs for campaign %s: %w", campaignID, err)
	}
	defer rows.Close()

	var out []KGNodePair
	for rows.Next() {
		var p KGNodePair
		if err := rows.Scan(&p.AID, &p.AName, &p.BID, &p.BName, &p.Similarity); err != nil {
			return nil, fmt.Errorf("storage: scan similar node pair: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: similar node pairs for campaign %s: %w", campaignID, err)
	}
	return out, nil
}

// CountUnembeddedNodesInCampaign counts a Campaign's Nodes still awaiting an
// embedding (#536). The probable-duplicate scan cannot see them, so the health
// panel reports the count rather than letting "no duplicates" imply a clean bill
// of health the check could not give.
//
// Distinct from [Store.CountUnembeddedNodes], which is the process-wide gauge
// (kept campaign-free to bound metric cardinality, ADR-0032).
func (s *Store) CountUnembeddedNodesInCampaign(ctx context.Context, campaignID uuid.UUID) (int, error) {
	var n int
	if err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM kg_node WHERE campaign_id = $1 AND embedding IS NULL`,
		campaignID).Scan(&n); err != nil {
		return 0, fmt.Errorf("storage: count unembedded kg nodes for campaign %s: %w", campaignID, err)
	}
	return n, nil
}
