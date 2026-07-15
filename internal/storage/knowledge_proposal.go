package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MrWong99/Glyphoxa/pkg/kgvocab"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
)

// Knowledge Proposal persistence (ADR-0052, #300): the pending queue an Agent's
// remember_knowledge call writes to. Nothing here touches kg_node/kg_edge — a
// proposal is a SUGGESTION the GM reviews (PR-b). The layer is deliberately thin:
// a create for the Tool handler, a list for the review surface, and the
// agent→own-Node lookup the own_node scope anchors on. proposed_write is stored
// verbatim as jsonb (the pkg/tool.ProposedWrite union), interpreted only at
// approval time.

// KnowledgeProposal is one persisted proposal row. ProposedWrite is the raw
// jsonb tagged union; ReviewedAt is nil until the GM acts. AuthoringAgentName is
// the filing Agent's display name, joined from the agents table by the list/get
// reads (empty on a bare row that skipped the join).
type KnowledgeProposal struct {
	ID                 uuid.UUID
	CampaignID         uuid.UUID
	AuthoringAgentID   uuid.UUID
	AuthoringAgentName string
	ProposedWrite      json.RawMessage
	Status             string
	CreatedAt          time.Time
	ReviewedAt         *time.Time
}

// knowledgeProposalJoinColumns selects the proposal columns plus the joined
// authoring Agent name — the review surface shows "who proposed this". The JOIN is
// inner: an authoring_agent_id always references a live agents row (ON DELETE
// CASCADE reaps the proposal with its Agent), so a pending proposal always has a
// name.
const knowledgeProposalJoinColumns = `
	p.id, p.campaign_id, p.authoring_agent_id, a.name,
	p.proposed_write, p.status, p.created_at, p.reviewed_at`

func scanKnowledgeProposalJoined(row pgx.Row) (KnowledgeProposal, error) {
	var p KnowledgeProposal
	err := row.Scan(
		&p.ID, &p.CampaignID, &p.AuthoringAgentID, &p.AuthoringAgentName,
		&p.ProposedWrite, &p.Status, &p.CreatedAt, &p.ReviewedAt,
	)
	return p, err
}

// ProposalBlockedError is returned by ApproveKnowledgeProposal when the proposed
// write cannot land as-is (an unresolvable/ambiguous subject, a dangling anchor,
// an edge matrix violation or duplicate, or an unreadable payload). Reason is a
// human-actionable message the GM sees; the proposal row stays pending so the GM
// can fix the wiki and re-approve, or reject. The RPC layer maps it to
// CodeFailedPrecondition with Reason verbatim.
type ProposalBlockedError struct{ Reason string }

func (e *ProposalBlockedError) Error() string { return e.Reason }

// CreateKnowledgeProposal inserts a pending proposal and returns the persisted
// row. proposedWrite is the raw jsonb payload (the caller marshals it). This layer
// does NO dedup by design: exact/normalized write-time dedup lives one layer up in
// the Tool handler (pkg/tool remember_knowledge, #411), which suppresses a repeat
// BEFORE calling here; genuine near-duplicates that differ in wording still land
// side-by-side for the GM to merge or reject (ADR-0052: similarity is a hint, not a
// semantic judgment — no auto-merge).
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
		`SELECT `+knowledgeProposalJoinColumns+`
		   FROM knowledge_proposal p
		   JOIN agents a ON a.id = p.authoring_agent_id
		  WHERE p.campaign_id = $1 AND p.status = 'pending'
		  ORDER BY p.created_at, p.id`, campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: list pending knowledge proposals: %w", err)
	}
	defer rows.Close()

	var out []KnowledgeProposal
	for rows.Next() {
		p, err := scanKnowledgeProposalJoined(rows)
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

// GetPendingKnowledgeProposal loads a single PENDING proposal by id, scoped to its
// Campaign and joined to its authoring Agent's name (#300, ADR-0052). An
// already-reviewed or missing id — or one in another Campaign — yields ErrNotFound;
// the review surface uses it to build the similarity-hint query for a live
// proposal only.
func (s *Store) GetPendingKnowledgeProposal(ctx context.Context, campaignID, id uuid.UUID) (KnowledgeProposal, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+knowledgeProposalJoinColumns+`
		   FROM knowledge_proposal p
		   JOIN agents a ON a.id = p.authoring_agent_id
		  WHERE p.id = $1 AND p.campaign_id = $2 AND p.status = 'pending'`, id, campaignID)
	p, err := scanKnowledgeProposalJoined(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return KnowledgeProposal{}, ErrNotFound
	}
	if err != nil {
		return KnowledgeProposal{}, fmt.Errorf("storage: get pending knowledge proposal %s: %w", id, err)
	}
	return p, nil
}

// ApproveKnowledgeProposal lands a pending proposal's write on the Knowledge Graph
// and marks it approved — ATOMICALLY (#300, ADR-0052): the claim UPDATE and the KG
// write share one transaction, so a refused write rolls the claim back and the row
// stays pending (never a half-approved proposal, never a lost race). The claim is
// conditional on status='pending' and takes the row lock, so a concurrent
// double-approve sees 0 rows and yields ErrNotFound. An unreadable payload (wrong
// version / unknown kind) or an unlandable write (unresolvable/ambiguous subject,
// dangling anchor, edge matrix violation, duplicate, self-edge) yields a
// *ProposalBlockedError with a human reason and leaves the row pending.
//
// Per ADR-0052 there is NO auto-merge: a fact/edge subject that names no wiki
// entry is refused with an actionable message, never silently created or fuzzily
// matched.
func (s *Store) ApproveKnowledgeProposal(ctx context.Context, campaignID, id uuid.UUID) error {
	return s.InTx(ctx, func(tx *Store) error {
		var raw json.RawMessage
		err := tx.db.QueryRow(ctx,
			`UPDATE knowledge_proposal
			    SET status = 'approved', reviewed_at = now()
			  WHERE id = $1 AND campaign_id = $2 AND status = 'pending'
			 RETURNING proposed_write`, id, campaignID).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("storage: approve knowledge proposal %s: claim: %w", id, err)
		}

		var w tool.ProposedWrite
		if err := json.Unmarshal(raw, &w); err != nil {
			return &ProposalBlockedError{Reason: "unreadable proposal — reject it"}
		}
		// The approve side checks the SAME version and kind identifiers the create
		// side stamped — both from pkg/kgvocab (#449), so they cannot drift.
		if w.V != kgvocab.ProposalWriteVersion {
			return &ProposalBlockedError{Reason: "unreadable proposal — reject it"}
		}
		switch w.Kind {
		case kgvocab.KindFact:
			return tx.applyProposedFact(ctx, campaignID, w)
		case kgvocab.KindEdge:
			return tx.applyProposedEdge(ctx, campaignID, w)
		case kgvocab.KindNode:
			return tx.applyProposedNode(ctx, campaignID, w)
		default:
			return &ProposalBlockedError{Reason: "unreadable proposal — reject it"}
		}
	})
}

// applyProposedFact appends the proposal's fact sentence to the target Node's body
// (blank body → the fact alone; non-blank → separated by a blank line) and resets
// the Node's embedding so the edit re-enters the backfill queue. The anchor is the
// proposal's NodeID when set (an own_node NPC proposal), else the Subject resolved
// by name. A NodeID that no longer resolves is blocked (the entry was deleted
// after the proposal was filed). Runs inside the approval tx.
func (tx *Store) applyProposedFact(ctx context.Context, campaignID uuid.UUID, w tool.ProposedWrite) error {
	anchor, err := tx.resolveProposalAnchor(ctx, campaignID, w.NodeID, w.Subject)
	if err != nil {
		return err
	}
	tag, err := tx.db.Exec(ctx,
		`UPDATE kg_node
		    SET body = CASE WHEN body = '' THEN $2 ELSE body || E'\n\n' || $2 END,
		        embedding = NULL,
		        embedding_model = '',
		        updated_at = now()
		  WHERE id = $1 AND campaign_id = $3`, anchor, w.Fact, campaignID)
	if err != nil {
		return fmt.Errorf("storage: approve fact: append body: %w", err)
	}
	// The anchor was resolved earlier in this same tx, but a concurrent DELETE could
	// land between resolve and UPDATE. 0 rows means the entry is gone: block (tx
	// rolls back, row stays pending) rather than silently swallow the fact —
	// consistent with the edge path's dangling-endpoint refusal.
	if tag.RowsAffected() == 0 {
		return &ProposalBlockedError{Reason: "the entry this proposal is about no longer exists — reject it"}
	}
	return nil
}

// applyProposedEdge creates the proposed Edge inside the approval tx, reusing the
// exact GM-authored CreateEdge path (createEdgeTx: endpoint load + validity matrix
// + insert). The FROM Node is the NodeID when set, else the Subject resolved by
// name; the TO Node is the Target resolved by name (Target is a NAME until
// approval, ADR-0052). CreateEdge's storage errors are translated to human
// ProposalBlockedError reasons the GM can act on.
func (tx *Store) applyProposedEdge(ctx context.Context, campaignID uuid.UUID, w tool.ProposedWrite) error {
	from, err := tx.resolveProposalAnchor(ctx, campaignID, w.NodeID, w.Subject)
	if err != nil {
		return err
	}
	to, err := tx.resolveNodeByName(ctx, campaignID, w.Target)
	if err != nil {
		return err
	}
	_, err = createEdgeTx(ctx, tx, NewKGEdge{
		CampaignID: campaignID,
		FromNodeID: from,
		ToNodeID:   to,
		Type:       KGEdgeType(w.Relation),
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrConflict):
		return &ProposalBlockedError{Reason: "this relationship already exists — reject the proposal"}
	case errors.Is(err, ErrInvalidEdge):
		// Self-edge or a validity-matrix violation: an actionable structural refusal.
		return &ProposalBlockedError{Reason: fmt.Sprintf(
			"a %q relationship isn't allowed between these entries — reject the proposal", w.Relation)}
	case errors.Is(err, ErrNotFound):
		// createEdgeTx already saw both resolved ids; a not-found here means the row
		// vanished mid-tx. Treat as a blocked, re-triable refusal.
		return &ProposalBlockedError{Reason: "one of the entries this relationship connects no longer exists — reject it"}
	default:
		return fmt.Errorf("storage: approve edge: %w", err)
	}
}

// applyProposedNode inserts the proposed brand-new Node (Butler-scope, gm_private
// false, embedding NULL so the backfill worker embeds it) inside the approval tx.
func (tx *Store) applyProposedNode(ctx context.Context, campaignID uuid.UUID, w tool.ProposedWrite) error {
	if _, err := tx.db.Exec(ctx,
		`INSERT INTO kg_node (campaign_id, node_type, name, body, gm_private)
		 VALUES ($1, $2::kg_node_type, $3, $4, false)`,
		campaignID, w.NodeType, w.Name, w.Body); err != nil {
		return fmt.Errorf("storage: approve node: insert: %w", err)
	}
	return nil
}

// resolveProposalAnchor resolves the Node a fact/edge proposal attaches to: the
// nodeID when the proposal carries one (an own_node NPC proposal anchored on its
// linked Node), else the subject resolved by name. A nodeID that no longer resolves
// in this Campaign is blocked (the entry was deleted after filing).
func (tx *Store) resolveProposalAnchor(ctx context.Context, campaignID uuid.UUID, nodeID, subject string) (uuid.UUID, error) {
	if strings.TrimSpace(nodeID) != "" {
		id, err := uuid.Parse(nodeID)
		if err != nil {
			return uuid.Nil, &ProposalBlockedError{Reason: "the entry this proposal is about no longer exists — reject it"}
		}
		var exists bool
		if err := tx.db.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM kg_node WHERE id = $1 AND campaign_id = $2)`,
			id, campaignID).Scan(&exists); err != nil {
			return uuid.Nil, fmt.Errorf("storage: resolve proposal anchor: %w", err)
		}
		if !exists {
			return uuid.Nil, &ProposalBlockedError{Reason: "the entry this proposal is about no longer exists — reject it"}
		}
		return id, nil
	}
	return tx.resolveNodeByName(ctx, campaignID, subject)
}

// resolveNodeByName resolves a Node by case-insensitive, trimmed name within a
// Campaign — the ADR-0052 no-auto-merge resolution: exactly one match lands, zero
// or many are refused with an actionable reason so the GM fixes the wiki first. It
// never creates or fuzzily matches.
func (tx *Store) resolveNodeByName(ctx context.Context, campaignID uuid.UUID, name string) (uuid.UUID, error) {
	display := strings.TrimSpace(name)
	rows, err := tx.db.Query(ctx,
		`SELECT id FROM kg_node WHERE campaign_id = $1 AND lower(name) = lower(btrim($2))`,
		campaignID, name)
	if err != nil {
		return uuid.Nil, fmt.Errorf("storage: resolve node by name: %w", err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return uuid.Nil, fmt.Errorf("storage: resolve node by name: scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return uuid.Nil, fmt.Errorf("storage: resolve node by name: %w", err)
	}
	switch len(ids) {
	case 1:
		return ids[0], nil
	case 0:
		return uuid.Nil, &ProposalBlockedError{Reason: fmt.Sprintf(
			"no wiki entry named %q — create it first, then approve; or reject", display)}
	default:
		return uuid.Nil, &ProposalBlockedError{Reason: fmt.Sprintf(
			"multiple entries named %q — rename one first", display)}
	}
}

// RejectKnowledgeProposal drops a pending proposal (status rejected, reviewed_at
// stamped) WITHOUT touching the KG (#300, ADR-0052). The row is kept for audit. A
// missing/already-reviewed id — or one in another Campaign — yields ErrNotFound.
func (s *Store) RejectKnowledgeProposal(ctx context.Context, campaignID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE knowledge_proposal
		    SET status = 'rejected', reviewed_at = now()
		  WHERE id = $1 AND campaign_id = $2 AND status = 'pending'`, id, campaignID)
	if err != nil {
		return fmt.Errorf("storage: reject knowledge proposal %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
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
