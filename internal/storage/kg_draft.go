package storage

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Knowledge-draft persistence (#479): the GM-confirmed batch-apply of an
// LLM-drafted set of Knowledge Graph entries, and the Character-NPC auto-node
// (ADR-0008 second amendment).

// KnowledgeDraftEdge is one edge of a knowledge draft to apply, referencing the
// draft's node list by index — the nodes have no ids until the apply lands.
type KnowledgeDraftEdge struct {
	FromIndex int
	ToIndex   int
	Type      KGEdgeType
}

// ApplyKnowledgeDraft creates a GM-confirmed knowledge draft's Nodes and Edges
// in ONE transaction (#479): either the whole draft lands or none of it. Every
// Node insert reuses CreateNode's semantics (enum-cast type); every Edge insert
// reuses createEdgeTx, so a matrix-invalid or self edge is ErrInvalidEdge and a
// duplicate (from, to, type) is ErrConflict — identical to a GM-authored
// CreateEdge — and any such failure rolls the WHOLE draft back (nothing
// half-lands). An out-of-range edge index is ErrInvalidEdge. The caller
// pre-validates indices and the matrix (the RPC layer refuses obviously bad
// drafts with better messages); this re-check is the transactional authority.
func (s *Store) ApplyKnowledgeDraft(ctx context.Context, campaignID uuid.UUID, nodes []NewKGNode, edges []KnowledgeDraftEdge) ([]KGNode, []KGEdge, error) {
	var createdNodes []KGNode
	var createdEdges []KGEdge
	err := s.InTx(ctx, func(tx *Store) error {
		createdNodes = make([]KGNode, 0, len(nodes))
		for _, n := range nodes {
			n.CampaignID = campaignID
			created, err := tx.CreateNode(ctx, n)
			if err != nil {
				return err
			}
			createdNodes = append(createdNodes, created)
		}
		createdEdges = make([]KGEdge, 0, len(edges))
		for _, e := range edges {
			if e.FromIndex < 0 || e.FromIndex >= len(createdNodes) ||
				e.ToIndex < 0 || e.ToIndex >= len(createdNodes) {
				return fmt.Errorf("storage: apply knowledge draft: edge index out of range: %w", ErrInvalidEdge)
			}
			created, err := createEdgeTx(ctx, tx, NewKGEdge{
				CampaignID: campaignID,
				FromNodeID: createdNodes[e.FromIndex].ID,
				ToNodeID:   createdNodes[e.ToIndex].ID,
				Type:       e.Type,
			})
			if err != nil {
				return err
			}
			createdEdges = append(createdEdges, created)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return createdNodes, createdEdges, nil
}

// CreateAgentWithNPCNode creates an Agent and — for a Character — its linked
// NPC Knowledge Graph Node in ONE transaction (#479, ADR-0008 second
// amendment): every new Character NPC starts with a wiki entry named after it,
// carrying the agent_id "voiced by" link. The Node starts with an empty body
// and gm_private=false (the Persona is NOT copied — persona is how the
// character speaks, the Node body is what the world knows). A non-character
// role creates no Node. Deleting the Agent later leaves the Node as a normal
// wiki-only entry (agent_id ON DELETE SET NULL).
func (s *Store) CreateAgentWithNPCNode(ctx context.Context, a NewAgent) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.InTx(ctx, func(tx *Store) error {
		aid, err := tx.CreateAgent(ctx, a)
		if err != nil {
			return err
		}
		id = aid
		if a.Role != AgentRoleCharacter {
			return nil
		}
		if _, err := tx.db.Exec(ctx,
			`INSERT INTO kg_node (campaign_id, node_type, name, body, gm_private, agent_id)
			 VALUES ($1, 'npc', $2, '', false, $3)`,
			a.CampaignID, a.Name, aid); err != nil {
			return fmt.Errorf("storage: create agent npc node: %w", err)
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// RenameAgentNode renames an Agent's linked NPC Node WHEN the two names still
// match (#479, ADR-0008 second amendment): after the auto-created Node, a GM
// renaming the Agent (typically off the "New NPC" placeholder) keeps the wiki
// entry in step — but once the GM has renamed the Node independently, the names
// diverge and the follow stops for good; bodies are never synced. The rename
// resets the embedding like any other name edit (ADR-0011) so similarity hints
// re-embed the new text. Matching no row (no linked Node, names already
// diverged, or oldName == newName) is a successful no-op.
func (s *Store) RenameAgentNode(ctx context.Context, campaignID, agentID uuid.UUID, oldName, newName string) error {
	if oldName == newName {
		return nil
	}
	if _, err := s.db.Exec(ctx,
		`UPDATE kg_node SET
		    name = $4,
		    embedding = NULL,
		    embedding_model = '',
		    updated_at = now()
		  WHERE campaign_id = $1 AND agent_id = $2 AND name = $3`,
		campaignID, agentID, oldName, newName); err != nil {
		return fmt.Errorf("storage: rename agent node for agent %s: %w", agentID, err)
	}
	return nil
}
