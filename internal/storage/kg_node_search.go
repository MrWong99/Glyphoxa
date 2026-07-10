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
// only in AgentNodeFacts (#133) and is unchanged.

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
	return s.searchNodes(ctx, campaignID, query, limit, false)
}

// SearchPublicNodes is SearchNodes with gm_private Nodes EXCLUDED IN THE QUERY —
// the prompt-facing knowledge search (#296). The exclusion is pushed into the
// WHERE clause so it applies BEFORE the LIMIT: a post-fetch filter in Go would
// drop the top-N ranked hits if they were all gm_private and starve a public
// match ranked N+1. This is the load-bearing ADR-0008 guard for the kg_query
// Tool — a GM-only Node must never reach an NPC's prompt. Existing GM-facing
// callers keep SearchNodes (the wiki search shows private Nodes to the GM).
func (s *Store) SearchPublicNodes(ctx context.Context, campaignID uuid.UUID, query string, limit int) ([]KGNode, error) {
	return s.searchNodes(ctx, campaignID, query, limit, true)
}

// searchNodes is the shared body of SearchNodes / SearchPublicNodes: publicOnly
// pushes an `AND NOT gm_private` into the ranked, LIMITed query so the exclusion
// precedes the LIMIT (never a post-fetch trim). An empty BuildTSQuery result
// yields (nil, nil).
func (s *Store) searchNodes(ctx context.Context, campaignID uuid.UUID, query string, limit int, publicOnly bool) ([]KGNode, error) {
	tsq := BuildTSQuery(query)
	if tsq == "" {
		return nil, nil
	}
	privacy := ""
	if publicOnly {
		privacy = " AND NOT gm_private"
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+kgNodeColumns+`
		   FROM kg_node, to_tsquery('simple', $2) q
		  WHERE campaign_id = $1 AND fts @@ q`+privacy+`
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
