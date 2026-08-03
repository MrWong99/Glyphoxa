package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Free-form Node tags and saved prep boards (#543, ADR-0008).
//
// Tags are ORGANIZATION, not schema. The seven Node types carry edge-validity
// rules and prompt semantics and are immutable after create, so that vocabulary
// stays closed — but organization should not require a migration and an enum edit
// every time a GM invents a category.
//
// Both are GM-ONLY: neither enters a prompt and neither affects AgentNodeFacts.
// That keeps the fact budget untouched and stops tags quietly becoming a second,
// unvalidated type system inside NPC context.

// MaxTagRunes bounds one tag. A tag is a label, not a sentence.
const MaxTagRunes = 40

// MaxTagsPerNode bounds how many an entry may carry — enough for real
// organization, few enough that the chip row stays readable.
const MaxTagsPerNode = 20

// ErrInvalidTag is returned for a blank or over-long tag.
var ErrInvalidTag = errors.New("storage: invalid tag")

// normalizeTag trims a tag and collapses inner whitespace, so "act two" and
// "act  two " are the same tag rather than two that look identical in the UI.
// Case is PRESERVED: a GM who writes "Act Two" gets "Act Two" back.
func normalizeTag(tag string) string {
	return strings.Join(strings.Fields(tag), " ")
}

// SetNodeTags replaces a Node's tags with exactly the supplied set, in one
// transaction. Replace-in-full matches how the chip editor works — add and remove
// are one save — and there is no ordering to preserve, so no position column.
//
// Tags are created on use: there is no tag table to maintain and no lifecycle to
// get wrong, which is the point of "free-form".
func (s *Store) SetNodeTags(ctx context.Context, campaignID, nodeID uuid.UUID, tags []string) error {
	clean := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, t := range tags {
		n := normalizeTag(t)
		if n == "" {
			continue
		}
		if len([]rune(n)) > MaxTagRunes {
			return fmt.Errorf("%w: %q is too long (max %d characters)", ErrInvalidTag, n, MaxTagRunes)
		}
		// Case-insensitive dedup within one save: "Act two" and "act two" are one tag.
		key := strings.ToLower(n)
		if seen[key] {
			continue
		}
		seen[key] = true
		clean = append(clean, n)
	}
	if len(clean) > MaxTagsPerNode {
		return fmt.Errorf("%w: an entry may carry at most %d tags", ErrInvalidTag, MaxTagsPerNode)
	}

	return s.InTx(ctx, func(tx *Store) error {
		if _, err := tx.db.Exec(ctx,
			`DELETE FROM kg_node_tag WHERE node_id = $1 AND campaign_id = $2`,
			nodeID, campaignID); err != nil {
			return fmt.Errorf("storage: set node tags %s: clear: %w", nodeID, err)
		}
		if len(clean) == 0 {
			return nil
		}
		if _, err := tx.db.Exec(ctx,
			`INSERT INTO kg_node_tag (node_id, campaign_id, tag)
			 SELECT $1, $2, t FROM unnest($3::text[]) AS t`,
			nodeID, campaignID, clean); err != nil {
			return fmt.Errorf("storage: set node tags %s: insert: %w", nodeID, err)
		}
		return nil
	})
}

// NodeTags returns one Node's tags, alphabetically.
func (s *Store) NodeTags(ctx context.Context, campaignID, nodeID uuid.UUID) ([]string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT tag FROM kg_node_tag
		  WHERE node_id = $1 AND campaign_id = $2 ORDER BY lower(tag)`, nodeID, campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: node tags %s: %w", nodeID, err)
	}
	defer rows.Close()
	return scanTagStrings(rows, "node tags")
}

// TaggedNode is one (node, tag) pair — the campaign-wide read the list and graph
// filters index client-side, so tagging a hundred entries is still one round trip.
type TaggedNode struct {
	NodeID uuid.UUID
	Tag    string
}

// CampaignTags returns every (node, tag) pair in a Campaign, ordered so the
// client can build both the tag vocabulary and the per-node index from one read.
func (s *Store) CampaignTags(ctx context.Context, campaignID uuid.UUID) ([]TaggedNode, error) {
	rows, err := s.db.Query(ctx,
		`SELECT node_id, tag FROM kg_node_tag WHERE campaign_id = $1 ORDER BY lower(tag), node_id`,
		campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: campaign tags: %w", err)
	}
	defer rows.Close()

	var out []TaggedNode
	for rows.Next() {
		var t TaggedNode
		if err := rows.Scan(&t.NodeID, &t.Tag); err != nil {
			return nil, fmt.Errorf("storage: scan campaign tag: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: campaign tags: %w", err)
	}
	return out, nil
}

// RenameTag renames a tag across the whole Campaign — the AC's "renaming a tag
// updates every use". Done in SQL rather than per-node, because a tag is one
// concept and renaming it entry-by-entry is how half-renamed vocabularies happen.
//
// A node already carrying the target name keeps a single row: the ON CONFLICT
// drops the duplicate rather than failing the whole rename.
func (s *Store) RenameTag(ctx context.Context, campaignID uuid.UUID, from, to string) error {
	fromN, toN := normalizeTag(from), normalizeTag(to)
	if fromN == "" || toN == "" {
		return fmt.Errorf("%w: a tag name must not be blank", ErrInvalidTag)
	}
	if len([]rune(toN)) > MaxTagRunes {
		return fmt.Errorf("%w: %q is too long (max %d characters)", ErrInvalidTag, toN, MaxTagRunes)
	}
	return s.InTx(ctx, func(tx *Store) error {
		// Drop rows that would collide, then rename the rest — simpler and more
		// predictable than an upsert dance over a composite primary key.
		if _, err := tx.db.Exec(ctx,
			`DELETE FROM kg_node_tag old
			  WHERE old.campaign_id = $1 AND lower(old.tag) = lower($2)
			    AND EXISTS (SELECT 1 FROM kg_node_tag cur
			                 WHERE cur.node_id = old.node_id AND lower(cur.tag) = lower($3))`,
			campaignID, fromN, toN); err != nil {
			return fmt.Errorf("storage: rename tag: drop collisions: %w", err)
		}
		if _, err := tx.db.Exec(ctx,
			`UPDATE kg_node_tag SET tag = $3
			  WHERE campaign_id = $1 AND lower(tag) = lower($2)`,
			campaignID, fromN, toN); err != nil {
			return fmt.Errorf("storage: rename tag: %w", err)
		}
		return nil
	})
}

func scanTagStrings(rows pgx.Rows, op string) ([]string, error) {
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("storage: scan %s: %w", op, err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: %s: %w", op, err)
	}
	return out, nil
}

// KGBoard is a named, ordered set of entries the GM pins for one session — the
// "tonight: the harbour heist" list they already keep in a text file outside the
// tool. NodeIDs is in board order.
type KGBoard struct {
	ID         uuid.UUID
	CampaignID uuid.UUID
	Name       string
	NodeIDs    []uuid.UUID
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// CreateBoard makes an empty prep board.
func (s *Store) CreateBoard(ctx context.Context, campaignID uuid.UUID, name string) (KGBoard, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return KGBoard{}, fmt.Errorf("storage: create board: name must not be empty")
	}
	var b KGBoard
	err := s.db.QueryRow(ctx,
		`INSERT INTO kg_board (campaign_id, name) VALUES ($1, $2)
		 RETURNING id, campaign_id, name, created_at, updated_at`, campaignID, name).
		Scan(&b.ID, &b.CampaignID, &b.Name, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return KGBoard{}, fmt.Errorf("storage: create board: %w", err)
	}
	return b, nil
}

// ListBoards returns a Campaign's boards with their entries, in board order. One
// read for the whole set: a campaign has a handful of boards, and the Session
// screen wants them all at once.
func (s *Store) ListBoards(ctx context.Context, campaignID uuid.UUID) ([]KGBoard, error) {
	rows, err := s.db.Query(ctx,
		`SELECT b.id, b.campaign_id, b.name, b.created_at, b.updated_at,
		        COALESCE(array_agg(n.node_id ORDER BY n.position, n.node_id)
		                 FILTER (WHERE n.node_id IS NOT NULL), '{}') AS node_ids
		   FROM kg_board b
		   LEFT JOIN kg_board_node n ON n.board_id = b.id
		  WHERE b.campaign_id = $1
		  GROUP BY b.id
		  ORDER BY lower(b.name), b.id`, campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: list boards: %w", err)
	}
	defer rows.Close()

	var out []KGBoard
	for rows.Next() {
		var b KGBoard
		if err := rows.Scan(&b.ID, &b.CampaignID, &b.Name, &b.CreatedAt, &b.UpdatedAt, &b.NodeIDs); err != nil {
			return nil, fmt.Errorf("storage: scan board: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list boards: %w", err)
	}
	return out, nil
}

// SetBoardNodes replaces a board's entries with exactly the supplied list, in
// order. Replace-in-full for the same reason the aspect editor uses it: add,
// remove and reorder are one save, and positions stay dense by construction.
func (s *Store) SetBoardNodes(ctx context.Context, campaignID, boardID uuid.UUID, nodeIDs []uuid.UUID) error {
	return s.InTx(ctx, func(tx *Store) error {
		var exists bool
		if err := tx.db.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM kg_board WHERE id = $1 AND campaign_id = $2)`,
			boardID, campaignID).Scan(&exists); err != nil {
			return fmt.Errorf("storage: set board nodes %s: %w", boardID, err)
		}
		if !exists {
			return ErrNotFound
		}
		if _, err := tx.db.Exec(ctx,
			`DELETE FROM kg_board_node WHERE board_id = $1 AND campaign_id = $2`,
			boardID, campaignID); err != nil {
			return fmt.Errorf("storage: set board nodes %s: clear: %w", boardID, err)
		}
		if len(nodeIDs) > 0 {
			pos := make([]int32, len(nodeIDs))
			for i := range nodeIDs {
				pos[i] = int32(i) //nolint:gosec // a board is a session's shortlist
			}
			if _, err := tx.db.Exec(ctx,
				`INSERT INTO kg_board_node (board_id, node_id, campaign_id, position)
				 SELECT $1, n, $2, p FROM unnest($3::uuid[], $4::int[]) AS t(n, p)
				 ON CONFLICT (board_id, node_id) DO NOTHING`,
				boardID, campaignID, nodeIDs, pos); err != nil {
				return fmt.Errorf("storage: set board nodes %s: insert: %w", boardID, err)
			}
		}
		if _, err := tx.db.Exec(ctx,
			`UPDATE kg_board SET updated_at = now() WHERE id = $1 AND campaign_id = $2`,
			boardID, campaignID); err != nil {
			return fmt.Errorf("storage: set board nodes %s: touch: %w", boardID, err)
		}
		return nil
	})
}

// RenameBoard renames a prep board.
func (s *Store) RenameBoard(ctx context.Context, campaignID, id uuid.UUID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("storage: rename board: name must not be empty")
	}
	tag, err := s.db.Exec(ctx,
		`UPDATE kg_board SET name = $3, updated_at = now() WHERE id = $1 AND campaign_id = $2`,
		id, campaignID, name)
	if err != nil {
		return fmt.Errorf("storage: rename board %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteBoard removes a prep board and its entries. The ENTRIES themselves are
// untouched — a board is a shortlist, not ownership.
func (s *Store) DeleteBoard(ctx context.Context, campaignID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM kg_board WHERE id = $1 AND campaign_id = $2`, id, campaignID)
	if err != nil {
		return fmt.Errorf("storage: delete board %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
