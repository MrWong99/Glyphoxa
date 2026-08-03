package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MrWong99/Glyphoxa/pkg/kgvocab"
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
//
// ID identifies an EXISTING row the caller is editing (uuid.Nil for a row the GM
// just added). Carrying it is what keeps a row's identity stable across saves: an
// unrecognised id is treated as a new row rather than trusted, so a stale client
// can add duplicates only by genuinely re-adding content, never by re-saving.
type NewKGNodeAspect struct {
	ID        uuid.UUID
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

// KGNodeAspectWrite is one editor save of a Node's Aspects (#542). Rows carries
// the list the GM authored, in their order, each keeping the id of the row it
// came from. Known carries the ids the editor had LOADED.
//
// Replace-in-full is the right editor contract (one save covers add, edit, reorder
// and delete, with no client-side identity bookkeeping), but the naive form —
// "delete everything, insert the list" — has two failure modes, and the fix for
// one must not create the other:
//
//   - It silently destroys an Aspect that a Knowledge Proposal approval appended
//     while the editor was open. Known bounds the delete to rows the editor
//     actually saw, so a row it never knew about survives.
//   - Deleting and reinserting ROTATES every row's id, which makes a second save
//     from a stale client (a second tab, or a failed background refetch) match
//     nothing and DUPLICATE the entire list. So rows are updated in place by id
//     and ids stay stable across saves; a save that repeats itself is idempotent.
type KGNodeAspectWrite struct {
	Known []uuid.UUID
	Rows  []NewKGNodeAspect
}

// ReplaceNodeAspects rewrites a Node's Aspects to the supplied list, preserving
// rows the caller had not loaded and keeping row identity stable (see
// [KGNodeAspectWrite]), inside one transaction.
//
// The write is scoped to (node_id, campaign_id) like every other KG mutation
// (#342): a Node in another Campaign matches nothing, so nothing is removed and
// the insert is refused by the composite FK.
//
// An empty Rows list clears the Aspects the caller knew about (the Node keeps its
// free-form body). Exceeding [kgvocab.MaxAspectsPerNode] once survivors are
// counted yields ErrAspectsFull.
func (s *Store) ReplaceNodeAspects(ctx context.Context, campaignID, nodeID uuid.UUID, w KGNodeAspectWrite) error {
	return s.InTx(ctx, func(tx *Store) error {
		return replaceNodeAspectsTx(ctx, tx, campaignID, nodeID, w)
	})
}

// lockNodeTx takes the Node's row lock so concurrent Aspect writers on the SAME
// Node serialise (#542). Without it, an editor save and a proposal approval — or
// two approvals — interleave between their read and their write, which is how a
// count-then-insert cap check gets bypassed. Returns ok=false when the Node does
// not exist in this Campaign.
func lockNodeTx(ctx context.Context, tx *Store, campaignID, nodeID uuid.UUID) (bool, error) {
	var id uuid.UUID
	err := tx.db.QueryRow(ctx,
		`SELECT id FROM kg_node WHERE id = $1 AND campaign_id = $2 FOR UPDATE`,
		nodeID, campaignID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("storage: lock kg node %s: %w", nodeID, err)
	}
	return true, nil
}

// replaceNodeAspectsTx is ReplaceNodeAspects' body, callable from an already-open
// transaction so a Node save and its Aspect save land atomically.
func replaceNodeAspectsTx(ctx context.Context, tx *Store, campaignID, nodeID uuid.UUID, w KGNodeAspectWrite) error {
	ok, err := lockNodeTx(ctx, tx, campaignID, nodeID)
	if err != nil {
		return err
	}
	if !ok {
		// Another Campaign's Node, or one deleted meanwhile: nothing to write.
		return nil
	}

	before, err := tx.ListNodeAspects(ctx, campaignID, nodeID)
	if err != nil {
		return err
	}
	existing := make(map[uuid.UUID]bool, len(before))
	for _, a := range before {
		existing[a.ID] = true
	}
	// A row is only "kept in place" if it really exists on this Node — an id the
	// client invented, or one already deleted, becomes a fresh insert instead of a
	// silent no-op update.
	sent := make(map[uuid.UUID]bool, len(w.Rows))
	for _, r := range w.Rows {
		if r.ID != uuid.Nil && existing[r.ID] {
			sent[r.ID] = true
		}
	}

	// Delete the rows the editor LOADED and is no longer sending — that, and only
	// that, is a deletion the GM asked for.
	var removed []uuid.UUID
	for _, id := range w.Known {
		if existing[id] && !sent[id] {
			removed = append(removed, id)
		}
	}
	if len(removed) > 0 {
		if _, err := tx.db.Exec(ctx,
			`DELETE FROM kg_node_aspect
			  WHERE node_id = $1 AND campaign_id = $2 AND id = ANY($3)`,
			nodeID, campaignID, removed); err != nil {
			return fmt.Errorf("storage: replace node aspects %s: delete: %w", nodeID, err)
		}
	}

	// Survivors: rows on the Node the caller neither sent nor knew about — a fact a
	// proposal approval appended mid-edit. They keep their identity and land after
	// the authored list, exactly where the append put them.
	var survivors []KGNodeAspect
	for _, a := range before {
		if sent[a.ID] {
			continue
		}
		if slices.Contains(removed, a.ID) {
			continue
		}
		survivors = append(survivors, a)
	}

	if len(w.Rows)+len(survivors) > kgvocab.MaxAspectsPerNode {
		return ErrAspectsFull
	}

	for i, r := range w.Rows {
		if r.ID != uuid.Nil && existing[r.ID] {
			// Update IN PLACE: the row keeps its id, so a repeated save is idempotent
			// instead of duplicating the list.
			if _, err := tx.db.Exec(ctx,
				`UPDATE kg_node_aspect
				    SET position = $4, key = $5, value = $6, gm_private = $7, updated_at = now()
				  WHERE id = $3 AND node_id = $1 AND campaign_id = $2`,
				nodeID, campaignID, r.ID, i, r.Key, r.Value, r.GMPrivate); err != nil {
				return fmt.Errorf("storage: replace node aspects %s: update %d: %w", nodeID, i, err)
			}
			continue
		}
		if _, err := tx.db.Exec(ctx,
			`INSERT INTO kg_node_aspect (node_id, campaign_id, position, key, value, gm_private)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			nodeID, campaignID, i, r.Key, r.Value, r.GMPrivate); err != nil {
			return fmt.Errorf("storage: replace node aspects %s: insert %d: %w", nodeID, i, err)
		}
	}

	// Renumber the survivors after the authored list so positions stay dense and
	// collision-free.
	for i, a := range survivors {
		if _, err := tx.db.Exec(ctx,
			`UPDATE kg_node_aspect SET position = $3 WHERE id = $4 AND node_id = $1 AND campaign_id = $2`,
			nodeID, campaignID, len(w.Rows)+i, a.ID); err != nil {
			return fmt.Errorf("storage: replace node aspects %s: renumber survivor: %w", nodeID, err)
		}
	}

	// A Node's Aspects are part of what the world knows about it, so an Aspect edit
	// invalidates the row's embedding exactly as a body edit does (#300, ADR-0011):
	// NULL it so the embedworker re-embeds with the new text and ADR-0052's
	// similarity hints keep reflecting reality.
	//
	// Guarded on an ACTUAL change, mirroring UpdateNode's IS DISTINCT FROM: a save
	// that touched only the name would otherwise re-embed the whole entry for
	// nothing.
	after, err := tx.ListNodeAspects(ctx, campaignID, nodeID)
	if err != nil {
		return err
	}
	if !sameStoredAspects(before, after) {
		if _, err := tx.db.Exec(ctx,
			`UPDATE kg_node SET embedding = NULL, embedding_model = '', updated_at = now()
			  WHERE id = $1 AND campaign_id = $2`, nodeID, campaignID); err != nil {
			return fmt.Errorf("storage: replace node aspects %s: invalidate embedding: %w", nodeID, err)
		}
	}
	return nil
}

// sameStoredAspects reports whether two stored lists carry identical text and
// privacy in the same order — the "nothing to re-embed" test.
func sameStoredAspects(before, after []KGNodeAspect) bool {
	if len(before) != len(after) {
		return false
	}
	for i := range before {
		if before[i].Key != after[i].Key ||
			before[i].Value != after[i].Value ||
			before[i].GMPrivate != after[i].GMPrivate {
			return false
		}
	}
	return true
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

// ErrAspectsFull is returned by the approve path when a Node already carries
// [kgvocab.MaxAspectsPerNode] Aspects. It is a refusal, not a failure: silently
// exceeding the cap would leave the entry permanently unsaveable in the editor,
// which validates against the same cap — the GM would be unable to fix even a
// typo without first deleting rows they never chose to add.
var ErrAspectsFull = errors.New("storage: node has reached its aspect limit")

// appendNodeAspectTx appends one Aspect to the end of a Node's list inside an open
// transaction — the Knowledge Proposal approve path (#542, ADR-0052). Returns the
// number of rows written; 0 means the Node vanished mid-transaction and the caller
// blocks the approval.
//
// The position is derived server-side from the current max rather than from a
// client index. Two approvals committing concurrently can still compute the same
// max and land on equal positions — there is no unique constraint, deliberately,
// because refusing a GM's approval over a display-order tie would be the worse
// failure. Ordering stays deterministic regardless: every read sorts by
// (position, id).
//
// The proposed Aspect always lands PUBLIC: an Agent proposes what its character
// would say out loud, and a secret is a GM authorship act, never an inference from
// play.
func appendNodeAspectTx(ctx context.Context, tx *Store, campaignID, nodeID uuid.UUID, key, value string) (int64, error) {
	// Take the Node's row lock first: the cap below is a count-then-insert, so two
	// approvals committing concurrently would each see room and together exceed it.
	ok, err := lockNodeTx(ctx, tx, campaignID, nodeID)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil // the entry vanished; the caller blocks the approval
	}

	var count int
	if err := tx.db.QueryRow(ctx,
		`SELECT count(*) FROM kg_node_aspect WHERE node_id = $1 AND campaign_id = $2`,
		nodeID, campaignID).Scan(&count); err != nil {
		return 0, fmt.Errorf("storage: append node aspect %s: count: %w", nodeID, err)
	}
	if count >= kgvocab.MaxAspectsPerNode {
		return 0, ErrAspectsFull
	}

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

// CreateNodeWithAspects creates a Node and its Aspects in ONE transaction (#542).
// Two statements would leave a created entry with no aspects when the second
// failed — and the client, seeing only an opaque error, cannot tell whether to
// retry (duplicating the entry) or not.
func (s *Store) CreateNodeWithAspects(ctx context.Context, n NewKGNode, aspects []NewKGNodeAspect) (KGNode, error) {
	var out KGNode
	err := s.InTx(ctx, func(tx *Store) error {
		created, err := tx.CreateNode(ctx, n)
		if err != nil {
			return err
		}
		// A brand-new Node has nothing to preserve, so there is nothing "known".
		if err := replaceNodeAspectsTx(ctx, tx, n.CampaignID, created.ID,
			KGNodeAspectWrite{Rows: aspects}); err != nil {
			return err
		}
		saved, err := tx.ListNodeAspects(ctx, n.CampaignID, created.ID)
		if err != nil {
			return err
		}
		created.Aspects = saved
		out = created
		return nil
	})
	if err != nil {
		return KGNode{}, err
	}
	return out, nil
}

// UpdateNodeWithAspects saves a Node's editor fields and its Aspects in ONE
// transaction (#542), so a save is never half-applied — the editor's name change
// and its aspect edits are one act to the GM and must be one act to the database.
// A missing Node yields ErrNotFound with nothing written.
func (s *Store) UpdateNodeWithAspects(ctx context.Context, u KGNodeUpdate, w KGNodeAspectWrite) (KGNode, error) {
	var out KGNode
	err := s.InTx(ctx, func(tx *Store) error {
		updated, err := tx.UpdateNode(ctx, u)
		if err != nil {
			return err
		}
		if err := replaceNodeAspectsTx(ctx, tx, u.CampaignID, u.ID, w); err != nil {
			return err
		}
		saved, err := tx.ListNodeAspects(ctx, u.CampaignID, u.ID)
		if err != nil {
			return err
		}
		updated.Aspects = saved
		out = updated
		return nil
	})
	if err != nil {
		return KGNode{}, err
	}
	return out, nil
}
