package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Knowledge Proposal persistence (ADR-0052, #300): the pending queue an Agent's
// remember_knowledge call writes to. Nothing here touches kg_node/kg_edge — a
// proposal is a SUGGESTION the GM reviews (PR-b). The layer is deliberately thin:
// a create for the Tool handler, a list for the review surface, and the
// agent→own-Node lookup the own_node scope anchors on. proposed_write is stored
// verbatim as jsonb (the pkg/tool.ProposedWrite union), interpreted only at
// approval time.

// KnowledgeProposal is one persisted proposal row. ProposedWrite is the raw
// jsonb tagged union; ReviewedAt is nil until the GM acts.
type KnowledgeProposal struct {
	ID               uuid.UUID
	CampaignID       uuid.UUID
	AuthoringAgentID uuid.UUID
	ProposedWrite    json.RawMessage
	Status           string
	CreatedAt        time.Time
	ReviewedAt       *time.Time
}

const knowledgeProposalColumns = `
	id, campaign_id, authoring_agent_id, proposed_write, status, created_at, reviewed_at`

func scanKnowledgeProposal(row pgx.Row) (KnowledgeProposal, error) {
	var p KnowledgeProposal
	err := row.Scan(
		&p.ID, &p.CampaignID, &p.AuthoringAgentID, &p.ProposedWrite,
		&p.Status, &p.CreatedAt, &p.ReviewedAt,
	)
	return p, err
}

// CreateKnowledgeProposal inserts a pending proposal and returns the persisted
// row. proposedWrite is the raw jsonb payload (the caller marshals it). There is
// NO dedup — near-duplicate proposals are surfaced side-by-side for the GM to
// merge or reject (ADR-0052: similarity is a hint, not a semantic judgment).
func (s *Store) CreateKnowledgeProposal(ctx context.Context, campaignID, agentID uuid.UUID, proposedWrite []byte) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO knowledge_proposal (campaign_id, authoring_agent_id, proposed_write)
		 VALUES ($1, $2, $3)`,
		campaignID, agentID, proposedWrite)
	if err != nil {
		return fmt.Errorf("storage: create knowledge proposal: %w", err)
	}
	return nil
}

// ListPendingKnowledgeProposals returns a Campaign's pending proposals
// oldest-first (the review order), hitting the partial pending index. An empty
// queue yields an empty slice, not an error.
func (s *Store) ListPendingKnowledgeProposals(ctx context.Context, campaignID uuid.UUID) ([]KnowledgeProposal, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+knowledgeProposalColumns+`
		   FROM knowledge_proposal
		  WHERE campaign_id = $1 AND status = 'pending'
		  ORDER BY created_at, id`, campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: list pending knowledge proposals: %w", err)
	}
	defer rows.Close()

	var out []KnowledgeProposal
	for rows.Next() {
		p, err := scanKnowledgeProposal(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan knowledge proposal: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list pending knowledge proposals: %w", err)
	}
	return out, nil
}

// AgentLinkedNode returns the Node an Agent is linked to (the NPC-Node↔Agent
// link, ADR-0008; kg_node.agent_id), the anchor an own_node-scoped
// remember_knowledge proposal attaches to. ok=false (no error) means the Agent
// has no linked entry — the Tool handler refuses rather than proposing against a
// wrong Node. The link is unique per Agent, so at most one row exists.
func (s *Store) AgentLinkedNode(ctx context.Context, agentID uuid.UUID) (KGNode, bool, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+kgNodeColumns+` FROM kg_node WHERE agent_id = $1 LIMIT 1`, agentID)
	n, err := scanKGNode(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return KGNode{}, false, nil
	}
	if err != nil {
		return KGNode{}, false, fmt.Errorf("storage: agent linked node for agent %s: %w", agentID, err)
	}
	return n, true, nil
}
