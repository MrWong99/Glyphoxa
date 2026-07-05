package storage

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

// Knowledge Graph Node fulltext search (#131, ADR-0008 v1.0 "Fulltext search
// (tsvector) only"). The kg_node.fts generated column (migration 00011) weights
// name over body; SearchNodes ranks matches with ts_rank. This is the GM-facing
// wiki search, so gm_private Nodes are INCLUDED — the NPC-prompt exclusion lives
// only in ListPublicNodes and is unchanged.

// BuildTSQuery turns a raw GM query string into a safe to_tsquery('simple') input.
// tsquery operator characters (& | ! ( ) : * …) are never passed through as
// operators: every non-letter/non-digit rune is a term separator, so a malicious
// or accidental operator can only ever split words. Surviving terms AND-join, and
// only the LAST term gets the ":*" prefix marker (typeahead — the word the GM is
// still typing). Returns "" when nothing survives, which SearchNodes treats as a
// no-op (no matches, not an error).
func BuildTSQuery(q string) string {
	terms := strings.FieldsFunc(q, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(terms) == 0 {
		return ""
	}
	terms[len(terms)-1] += ":*"
	return strings.Join(terms, " & ")
}

// SearchNodes returns the Campaign's Knowledge Graph Nodes whose name or body
// match the query, ranked by relevance (ts_rank over the weighted fts column:
// name weight A outranks body weight B), then newest-first, then id. The search
// is served by the kg_node_fts_idx GIN index (fts @@ q), not a substring scan.
// gm_private Nodes are INCLUDED (GM-facing search). An empty BuildTSQuery result
// yields (nil, nil) — no matches, not an error.
func (s *Store) SearchNodes(ctx context.Context, campaignID uuid.UUID, query string, limit int) ([]KGNode, error) {
	tsq := BuildTSQuery(query)
	if tsq == "" {
		return nil, nil
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+kgNodeColumns+`
		   FROM kg_node, to_tsquery('simple', $2) q
		  WHERE campaign_id = $1 AND fts @@ q
		  ORDER BY ts_rank(fts, q) DESC, updated_at DESC, id
		  LIMIT $3`, campaignID, tsq, limit)
	if err != nil {
		return nil, fmt.Errorf("storage: search kg nodes for campaign %s: %w", campaignID, err)
	}
	defer rows.Close()

	var out []KGNode
	for rows.Next() {
		n, err := scanKGNode(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan kg node search row: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: search kg nodes for campaign %s: %w", campaignID, err)
	}
	return out, nil
}
