package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Knowledge Graph Node Aspect persistence (#542, ADR-0008 third amendment): the
// per-fact visibility layer on a Node. An Aspect is one ordered (key, value) row
// carrying its OWN gm_private flag, so "Bart runs the Rusty Anchor" can be public
// while "Bart took the smugglers' bribe" stays GM-only — on the same Node, in the
// same graph.
//
// kg_node.body is retained as the free-form remainder, so a Node with no Aspects
// behaves exactly as it did before this slice.

// KGNodeAspect is one persisted Aspect of a Node. Position is the author order
// within its Node (0-based, dense after every ReplaceNodeAspects). GMPrivate hides
// THIS row — and only this row — from every prompt-facing read.
//
// The json tags are the wire format of the jsonb aggregate the Node reads pack
// their Aspects into (see kgNodeAspectsExpr), not a stored document: the columns
// are real columns.
type KGNodeAspect struct {
	ID        uuid.UUID `json:"id"`
	Position  int       `json:"position"`
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	GMPrivate bool      `json:"gm_private"`
}

// NewKGNodeAspect is one Aspect row as the editor supplies it. Position is NOT
// carried: [Store.ReplaceNodeAspects] assigns it from the slice order, so the
// author order is whatever the GM dragged the rows into and dense positions are
// an invariant rather than a client responsibility.
type NewKGNodeAspect struct {
	Key       string
	Value     string
	GMPrivate bool
}

// kgNodeAspectsExpr builds the correlated aggregate that packs a Node's Aspects
// into ONE jsonb array column, ordered by position — so a Node read stays a single
// round trip instead of an N+1 (the prompt-facing neighbourhood read runs inside
// the kgfacts turn budget, where a second round trip is not free).
//
// publicOnly pushes the gm_private exclusion INTO the aggregate. That is what
// makes per-Aspect privacy a seam property rather than call-site discipline: a
// prompt-facing read physically cannot receive a private Aspect to leak, exactly
// as PromptKGView's SQL already does for whole Nodes (#450).
//
// nodeIDExpr is the qualified id column of the outer kg_node row (`kg_node.id`, or
// `n.id` under an alias).
func kgNodeAspectsExpr(nodeIDExpr string, publicOnly bool) string {
	privacy := ""
	if publicOnly {
		privacy = " AND NOT a.gm_private"
	}
	return `COALESCE((SELECT jsonb_agg(jsonb_build_object(
	            'id', a.id, 'position', a.position, 'key', a.key,
	            'value', a.value, 'gm_private', a.gm_private)
	            ORDER BY a.position, a.id)
	          FROM kg_node_aspect a
	         WHERE a.node_id = ` + nodeIDExpr + privacy + `), '[]'::jsonb)`
}

// decodeAspects unpacks the jsonb aggregate column into the Node's Aspects. An
// empty aggregate yields a nil slice (never a zero-length non-nil one), so a
// Node with no Aspects compares and renders identically to one from before this
// slice existed.
func decodeAspects(raw []byte, into *[]KGNodeAspect) error {
	if len(raw) == 0 {
		return nil
	}
	var out []KGNodeAspect
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("storage: decode kg node aspects: %w", err)
	}
	if len(out) == 0 {
		return nil
	}
	*into = out
	return nil
}

// AspectValues returns a Node's Aspect VALUES — without their keys — the
// granularity the ADR-0052 write-time dedup compares at, because a proposal's
// salient text is the fact itself and the key is only how it is filed (#411,
// #542). publicOnly is mandatory for every prompt-reachable consumer: a matched
// established fact is quoted back to the model in the Tool result, so a private
// Aspect matched here would leak a GM secret the prompt seam never carried.
func (n KGNode) AspectValues(publicOnly bool) []string {
	var out []string
	for _, a := range n.Aspects {
		if publicOnly && a.GMPrivate {
			continue
		}
		if v := strings.TrimSpace(a.Value); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// AspectLines renders a Node's Aspects as "Key: Value" lines — the flat form the
// embedding text consumes, where the key carries real signal about what kind of
// fact the value is. publicOnly drops gm_private rows.
//
// A row with no value renders as its key alone; a fully empty row is skipped.
func (n KGNode) AspectLines(publicOnly bool) []string {
	var out []string
	for _, a := range n.Aspects {
		if publicOnly && a.GMPrivate {
			continue
		}
		key := strings.TrimSpace(a.Key)
		value := strings.TrimSpace(a.Value)
		switch {
		case key == "" && value == "":
			continue
		case key == "":
			out = append(out, value)
		case value == "":
			out = append(out, key)
		default:
			out = append(out, key+": "+value)
		}
	}
	return out
}

// ReplaceNodeAspects rewrites a Node's Aspects to exactly the supplied list, in
// the supplied order, inside one transaction (#542). Replace-in-full — rather than
// a per-row diff — is what makes reorder, delete and edit ONE editor save with no
// client-side identity bookkeeping, and it keeps `position` dense by construction.
//
// The write is scoped to (node_id, campaign_id) like every other KG mutation
// (#342): a Node in another Campaign matches nothing, so the delete removes
// nothing and the insert is refused by the composite FK.
//
// An empty list clears the Node's Aspects (the Node keeps its free-form body).
func (s *Store) ReplaceNodeAspects(ctx context.Context, campaignID, nodeID uuid.UUID, aspects []NewKGNodeAspect) error {
	return s.InTx(ctx, func(tx *Store) error {
		if _, err := tx.db.Exec(ctx,
			`DELETE FROM kg_node_aspect WHERE node_id = $1 AND campaign_id = $2`,
			nodeID, campaignID); err != nil {
			return fmt.Errorf("storage: replace node aspects %s: clear: %w", nodeID, err)
		}
		for i, a := range aspects {
			if _, err := tx.db.Exec(ctx,
				`INSERT INTO kg_node_aspect (node_id, campaign_id, position, key, value, gm_private)
				 VALUES ($1, $2, $3, $4, $5, $6)`,
				nodeID, campaignID, i, a.Key, a.Value, a.GMPrivate); err != nil {
				return fmt.Errorf("storage: replace node aspects %s: insert %d: %w", nodeID, i, err)
			}
		}
		// A Node's Aspects are part of what the world knows about it, so an Aspect edit
		// invalidates the row's embedding exactly as a body edit does (#300, ADR-0011):
		// NULL it so the embedworker re-embeds with the new text and ADR-0052's
		// similarity hints keep reflecting reality. The updated_at bump also makes the
		// edit visible to the embedworker's stale-write guard.
		if _, err := tx.db.Exec(ctx,
			`UPDATE kg_node SET embedding = NULL, embedding_model = '', updated_at = now()
			  WHERE id = $1 AND campaign_id = $2`, nodeID, campaignID); err != nil {
			return fmt.Errorf("storage: replace node aspects %s: invalidate embedding: %w", nodeID, err)
		}
		return nil
	})
}

// ListNodeAspects returns one Node's Aspects in author order, INCLUDING private
// ones — a GM-facing read (the editor, the Campaign Bundle export). Prompt-facing
// code never calls it: it reads Aspects through the Node projections, whose
// aggregate filters gm_private in SQL.
func (s *Store) ListNodeAspects(ctx context.Context, campaignID, nodeID uuid.UUID) ([]KGNodeAspect, error) {
	rows, err := s.db.Query(ctx,
		`SELECT id, position, key, value, gm_private
		   FROM kg_node_aspect
		  WHERE node_id = $1 AND campaign_id = $2
		  ORDER BY position, id`, nodeID, campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: list node aspects %s: %w", nodeID, err)
	}
	defer rows.Close()

	var out []KGNodeAspect
	for rows.Next() {
		var a KGNodeAspect
		if err := rows.Scan(&a.ID, &a.Position, &a.Key, &a.Value, &a.GMPrivate); err != nil {
			return nil, fmt.Errorf("storage: scan node aspect: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list node aspects %s: %w", nodeID, err)
	}
	return out, nil
}

// appendNodeAspectTx appends one Aspect to the end of a Node's list inside an open
// transaction — the Knowledge Proposal approve path (#542, ADR-0052). The position
// is derived server-side from the current max, so two approvals racing on the same
// Node cannot collide on a client-chosen index. Returns the number of rows written
// (0 means the Node vanished mid-transaction; the caller blocks the approval).
//
// The proposed Aspect always lands PUBLIC: an Agent proposes what its character
// would say out loud, and a secret is a GM authorship act, never an inference from
// play.
func appendNodeAspectTx(ctx context.Context, tx *Store, campaignID, nodeID uuid.UUID, key, value string) (int64, error) {
	tag, err := tx.db.Exec(ctx,
		`INSERT INTO kg_node_aspect (node_id, campaign_id, position, key, value, gm_private)
		 SELECT $1, $2,
		        COALESCE((SELECT max(position) + 1 FROM kg_node_aspect WHERE node_id = $1), 0),
		        $3, $4, false
		  WHERE EXISTS (SELECT 1 FROM kg_node WHERE id = $1 AND campaign_id = $2)`,
		nodeID, campaignID, key, value)
	if err != nil {
		return 0, fmt.Errorf("storage: append node aspect %s: %w", nodeID, err)
	}
	return tag.RowsAffected(), nil
}
