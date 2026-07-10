package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Knowledge Graph Edge persistence (#132, ADR-0008 v1.0 + 2026-07-04 amendment):
// typed directional relationships between two same-Campaign Nodes, plus the
// NPC-Node ↔ Character NPC Agent link (nullable kg_node.agent_id). Every Edge is
// a one-way assertion (no auto-inverse); the object-side-only validity matrix
// gives the structural edge types typo protection without fighting the domain.

// ErrInvalidEdge is returned when an Edge's (type, from-type, to-type) combination
// violates the amendment's validity matrix, or when an Agent link targets a
// non-NPC Node (the DB CHECK). The RPC layer maps it to CodeInvalidArgument.
var ErrInvalidEdge = errors.New("storage: invalid edge")

// ErrConflict is returned when a write hits a UNIQUE constraint (Postgres 23505):
// a duplicate (from, to, type) Edge, or an Agent already linked to another Node.
// The RPC layer maps it to CodeAlreadyExists.
var ErrConflict = errors.New("storage: conflict")

// KGEdgeType is a Knowledge Graph Edge's type (CONTEXT.md "Edge", ADR-0008). It
// mirrors the kg_edge_type Postgres enum.
type KGEdgeType string

const (
	KGEdgeResidesIn      KGEdgeType = "resides_in"
	KGEdgeMemberOf       KGEdgeType = "member_of"
	KGEdgeOwns           KGEdgeType = "owns"
	KGEdgeKnows          KGEdgeType = "knows"
	KGEdgeEnemyOf        KGEdgeType = "enemy_of"
	KGEdgeAllyOf         KGEdgeType = "ally_of"
	KGEdgeParentOf       KGEdgeType = "parent_of"
	KGEdgeParticipatedIn KGEdgeType = "participated_in"
	KGEdgeMentionedIn    KGEdgeType = "mentioned_in"
)

// KGEdge is one persisted typed directional Edge between two Nodes in a Campaign.
type KGEdge struct {
	ID         uuid.UUID
	CampaignID uuid.UUID
	FromNodeID uuid.UUID
	ToNodeID   uuid.UUID
	Type       KGEdgeType
	CreatedAt  time.Time
}

// KGEdgeWithNodes is an Edge joined to its two endpoints' display fields, so the
// Campaign screen renders an incident-edge list without an N+1 per endpoint.
type KGEdgeWithNodes struct {
	KGEdge
	FromName string
	FromType KGNodeType
	ToName   string
	ToType   KGNodeType
}

// isCharacterOrNPC reports whether a Node type is a Character or an NPC — the
// two ends parent_of constrains.
func isCharacterOrNPC(t KGNodeType) bool {
	return t == KGNodeCharacter || t == KGNodeNPC
}

// ValidateEdge enforces the ADR-0008 amendment's object-side-only validity matrix.
// Structural edge types constrain their target (resides_in → Location, member_of
// → Faction, participated_in → PlotThread); parent_of constrains both ends to
// Character/NPC. The subject side of the structural types and every social/loose
// type (knows, owns, enemy_of, ally_of, mentioned_in) accept any Node type — the
// domain legitimately contains sentient swords that know kings. It is pure (no
// DB): the create path validates before the INSERT.
func ValidateEdge(t KGEdgeType, from, to KGNodeType) error {
	switch t {
	case KGEdgeResidesIn:
		if to != KGNodeLocation {
			return ErrInvalidEdge
		}
	case KGEdgeMemberOf:
		if to != KGNodeFaction {
			return ErrInvalidEdge
		}
	case KGEdgeParticipatedIn:
		if to != KGNodePlotThread {
			return ErrInvalidEdge
		}
	case KGEdgeParentOf:
		if !isCharacterOrNPC(from) || !isCharacterOrNPC(to) {
			return ErrInvalidEdge
		}
	case KGEdgeKnows, KGEdgeOwns, KGEdgeEnemyOf, KGEdgeAllyOf, KGEdgeMentionedIn:
		// Loose/social types: unconstrained on both ends by design.
	default:
		// An unknown edge type is never valid — reject defensively rather than
		// falling through to nil (the DB enum is the real gate, but the RPC layer
		// also validates before ever reaching storage).
		return ErrInvalidEdge
	}
	return nil
}

// NewKGEdge is the input to CreateEdge. The endpoints must be same-Campaign Nodes;
// the CampaignID scopes the endpoint lookup and pins both composite FKs.
type NewKGEdge struct {
	CampaignID uuid.UUID
	FromNodeID uuid.UUID
	ToNodeID   uuid.UUID
	Type       KGEdgeType
}

const kgEdgeColumns = `
	id, campaign_id, from_node_id, to_node_id, edge_type, created_at`

func scanKGEdge(row pgx.Row) (KGEdge, error) {
	var e KGEdge
	err := row.Scan(&e.ID, &e.CampaignID, &e.FromNodeID, &e.ToNodeID, &e.Type, &e.CreatedAt)
	return e, err
}

// pgErrCode extracts a Postgres SQLSTATE from an error chain, if any.
func pgErrCode(err error) (string, bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code, true
	}
	return "", false
}

// CreateEdge inserts a typed directional Edge after checking both endpoints exist
// in the given Campaign and the (type, from-type, to-type) combination is valid.
// A self-edge is rejected up front (ErrInvalidEdge); a missing or cross-campaign
// endpoint yields ErrNotFound (the endpoint SELECT is Campaign-scoped, so a
// foreign Node is simply invisible); an invalid combination yields ErrInvalidEdge;
// a duplicate (from, to, type) yields ErrConflict. Validation on the immutable
// node_type is sound without a trigger because a Node's type never changes.
func (s *Store) CreateEdge(ctx context.Context, e NewKGEdge) (KGEdge, error) {
	if e.FromNodeID == e.ToNodeID {
		return KGEdge{}, ErrInvalidEdge
	}

	var created KGEdge
	err := s.InTx(ctx, func(tx *Store) error {
		rows, err := tx.db.Query(ctx,
			`SELECT id, node_type FROM kg_node WHERE id IN ($1, $2) AND campaign_id = $3`,
			e.FromNodeID, e.ToNodeID, e.CampaignID)
		if err != nil {
			return fmt.Errorf("storage: create edge: load endpoints: %w", err)
		}
		types := map[uuid.UUID]KGNodeType{}
		for rows.Next() {
			var id uuid.UUID
			var t KGNodeType
			if err := rows.Scan(&id, &t); err != nil {
				rows.Close()
				return fmt.Errorf("storage: create edge: scan endpoint: %w", err)
			}
			types[id] = t
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("storage: create edge: load endpoints: %w", err)
		}

		fromType, okFrom := types[e.FromNodeID]
		toType, okTo := types[e.ToNodeID]
		if !okFrom || !okTo {
			return ErrNotFound
		}
		if err := ValidateEdge(e.Type, fromType, toType); err != nil {
			return err
		}

		row := tx.db.QueryRow(ctx,
			`INSERT INTO kg_edge (campaign_id, from_node_id, to_node_id, edge_type)
			 VALUES ($1, $2, $3, $4::kg_edge_type)
			 RETURNING `+kgEdgeColumns,
			e.CampaignID, e.FromNodeID, e.ToNodeID, e.Type)
		c, err := scanKGEdge(row)
		if err != nil {
			return mapEdgeWriteErr("insert edge", err)
		}
		created = c
		return nil
	})
	if err != nil {
		return KGEdge{}, err
	}
	return created, nil
}

// mapEdgeWriteErr translates the Postgres constraint failures the Edge/link writes
// can raise into the domain errors the RPC layer maps: 23505 (unique_violation) →
// ErrConflict; 23514 (check_violation, e.g. the NPC-only link CHECK or the
// no-self-edge CHECK) → ErrInvalidEdge. Anything else is wrapped opaquely.
func mapEdgeWriteErr(op string, err error) error {
	if code, ok := pgErrCode(err); ok {
		switch code {
		case "23505":
			return ErrConflict
		case "23514":
			return ErrInvalidEdge
		}
	}
	return fmt.Errorf("storage: %s: %w", op, err)
}

// DeleteEdge removes a typed Edge by id, scoped to its owning Campaign (#342): the
// DELETE matches (id, campaign_id), so an Edge in another Campaign is not deleted
// and yields ErrNotFound — a cross-campaign delete is refused server-side. A
// missing id likewise yields ErrNotFound.
func (s *Store) DeleteEdge(ctx context.Context, campaignID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM kg_edge WHERE id = $1 AND campaign_id = $2`, id, campaignID)
	if err != nil {
		return fmt.Errorf("storage: delete kg edge %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// NodeEdges returns a Node's incident Edges split by direction — outgoing
// (from_node_id = nodeID) and incoming (to_node_id = nodeID) — each joined to both
// endpoints' name/type so the Campaign screen renders without an N+1. One query
// fetches both directions (ordered created_at, id); the split is done in Go.
//
// The read is scoped to the owning Campaign (#356): the anchor Node must belong to
// campaignID, else ErrNotFound — a Node in another Campaign is invisible, leaking
// neither its edges nor its joined endpoint names (incl. gm_private ones) nor an
// existence oracle. A truly-missing Node id is the same ErrNotFound. Same
// no-oracle discipline as the cross-campaign DeleteEdge/SetNodeAgent refusals.
func (s *Store) NodeEdges(ctx context.Context, campaignID, nodeID uuid.UUID) (outgoing, incoming []KGEdgeWithNodes, err error) {
	// Confirm the anchor Node belongs to this Campaign before returning any edge
	// data — this is what turns a cross-campaign (or missing) Node into ErrNotFound
	// instead of a silently-empty list that would still confirm the id parses.
	var exists bool
	if err := s.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM kg_node WHERE id = $1 AND campaign_id = $2)`,
		nodeID, campaignID).Scan(&exists); err != nil {
		return nil, nil, fmt.Errorf("storage: node edges %s: anchor lookup: %w", nodeID, err)
	}
	if !exists {
		return nil, nil, ErrNotFound
	}

	// Edges are same-campaign by construction (CreateEdge enforces it), so filtering
	// the anchor by campaign above is sufficient; the WHERE below stays direction-only.
	rows, err := s.db.Query(ctx,
		`SELECT e.id, e.campaign_id, e.from_node_id, e.to_node_id, e.edge_type, e.created_at,
		        fn.name, fn.node_type, tn.name, tn.node_type
		   FROM kg_edge e
		   JOIN kg_node fn ON fn.id = e.from_node_id
		   JOIN kg_node tn ON tn.id = e.to_node_id
		  WHERE e.from_node_id = $1 OR e.to_node_id = $1
		  ORDER BY e.created_at, e.id`, nodeID)
	if err != nil {
		return nil, nil, fmt.Errorf("storage: node edges %s: %w", nodeID, err)
	}
	defer rows.Close()

	for rows.Next() {
		var e KGEdgeWithNodes
		if err := rows.Scan(
			&e.ID, &e.CampaignID, &e.FromNodeID, &e.ToNodeID, &e.Type, &e.CreatedAt,
			&e.FromName, &e.FromType, &e.ToName, &e.ToType,
		); err != nil {
			return nil, nil, fmt.Errorf("storage: node edges %s: scan: %w", nodeID, err)
		}
		if e.FromNodeID == nodeID {
			outgoing = append(outgoing, e)
		} else {
			incoming = append(incoming, e)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("storage: node edges %s: %w", nodeID, err)
	}
	return outgoing, incoming, nil
}

// SetNodeAgent links or unlinks a Node's Character NPC Agent (the "voiced by"
// link, ADR-0008 amendment). Both paths are scoped to the owning Campaign (#342):
// every UPDATE matches the Node's campaign_id against the caller's campaignID, so a
// Node in another Campaign is invisible and yields ErrNotFound — a cross-campaign
// link or unlink is refused server-side. A valid agentID links: the single UPDATE
// also matches the Node's campaign against the Agent's in one statement, so a
// missing or cross-campaign Agent matches no row and yields ErrNotFound; a non-NPC
// Node trips the DB CHECK (ErrInvalidEdge); an Agent already linked to another Node
// trips the UNIQUE index (ErrConflict). An invalid agentID unlinks (agent_id =
// NULL). A missing Node yields ErrNotFound.
func (s *Store) SetNodeAgent(ctx context.Context, campaignID, nodeID uuid.UUID, agentID uuid.NullUUID) (KGNode, error) {
	if !agentID.Valid {
		row := s.db.QueryRow(ctx,
			`UPDATE kg_node SET agent_id = NULL, updated_at = now()
			  WHERE id = $1 AND campaign_id = $2
			 RETURNING `+kgNodeColumns, nodeID, campaignID)
		n, err := scanKGNode(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return KGNode{}, ErrNotFound
		}
		if err != nil {
			return KGNode{}, fmt.Errorf("storage: unlink node agent %s: %w", nodeID, err)
		}
		return n, nil
	}

	row := s.db.QueryRow(ctx,
		`UPDATE kg_node SET agent_id = $2, updated_at = now()
		  WHERE id = $1
		    AND campaign_id = $3
		    AND campaign_id = (SELECT campaign_id FROM agents WHERE id = $2)
		 RETURNING `+kgNodeColumns, nodeID, agentID.UUID, campaignID)
	n, err := scanKGNode(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// No row matched: the Node is missing / in another Campaign, or the Agent is
		// missing / in another Campaign (the subquery yielded no matching campaign_id).
		return KGNode{}, ErrNotFound
	}
	if err != nil {
		return KGNode{}, mapEdgeWriteErr(fmt.Sprintf("link node agent %s", nodeID), err)
	}
	return n, nil
}
