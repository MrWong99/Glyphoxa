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
