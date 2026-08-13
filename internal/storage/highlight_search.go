package storage

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Promoted-Highlight fulltext search for the Ctrl+K campaign palette (#591).
// The highlight.fts generated column (migration 00051) weights excerpt over
// reason; SearchPromotedHighlights ranks matches with ts_rank. Candidates are
// EXCLUDED IN THE QUERY, before the LIMIT: a Highlight Candidate is not yet a
// Highlight (CONTEXT.md) and GM curation stays inside the session-end review
// (ADR-0051) — a post-fetch trim in Go would let top-ranked candidates starve a
// promoted match ranked below them (the SearchPublicNodes lesson, #296).

// SearchPromotedHighlights returns the Campaign's PROMOTED Highlights whose
// excerpt or reason matches the query, ranked by relevance (ts_rank over the
// weighted fts column: excerpt weight A outranks reason weight B), then newest
// moment first, then id. Tenant- AND campaign-scoped in the query, like every
// Highlight read. An empty BuildTSQuery result yields (nil, nil) — no matches,
// not an error.
func (s *Store) SearchPromotedHighlights(ctx context.Context, tenantID, campaignID uuid.UUID, query string, limit int) ([]Highlight, error) {
	tsq := BuildTSQuery(query)
	if tsq == "" {
		return nil, nil
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+highlightColumns+`
		   FROM highlight, to_tsquery('simple', $3) q
		  WHERE tenant_id = $1 AND campaign_id = $2 AND status = 'promoted' AND fts @@ q
		  ORDER BY ts_rank(fts, q) DESC, starts_at DESC, id
		  LIMIT $4`, tenantID, campaignID, tsq, limit)
	if err != nil {
		return nil, fmt.Errorf("storage: search promoted highlights for campaign %s: %w", campaignID, err)
	}
	defer rows.Close()

	var out []Highlight
	for rows.Next() {
		h, err := scanHighlight(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan highlight search row: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: search promoted highlights for campaign %s: %w", campaignID, err)
	}
	return out, nil
}
