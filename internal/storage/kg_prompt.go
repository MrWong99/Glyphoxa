package storage

import (
	"context"

	"github.com/google/uuid"
)

// PromptKGView is the prompt-facing Knowledge Graph read surface (#450,
// ADR-0008): the type prompt-assembly code (Hot Context fact recall, the
// kg_query Tool adapter) holds for KG reads. Every method filters gm_private
// rows inside its SQL, and the type exposes NO unfiltered read — "reaches a
// prompt" and "may see gm_private" are separated by the type system instead of
// by remembering the right *Store method name at each call site. GM and web
// review surfaces keep the full-KG reads on *Store (SearchNodes, SimilarNodes,
// ListNodes), which never flow into prompt assembly.
//
// The zero-lag filtering semantics are those of the underlying *Store reads;
// the gm_private exclusion is pinned by the seam integration test
// (TestPromptKG_NeverReturnsGMPrivate).
type PromptKGView struct{ s *Store }

// PromptKG returns the prompt-facing read view of this Store — the ONLY KG
// handle prompt-assembly wiring should be given (#450).
func (s *Store) PromptKG() PromptKGView { return PromptKGView{s: s} }

// SearchPublicNodes is the prompt-facing KG search: gm_private Nodes are
// excluded in the query, before the LIMIT (see [Store.SearchPublicNodes]).
func (v PromptKGView) SearchPublicNodes(ctx context.Context, campaignID uuid.UUID, query string, limit int) ([]KGNode, error) {
	return v.s.SearchPublicNodes(ctx, campaignID, query, limit)
}

// AgentNodeFacts is the Agent's own edge-aware Node neighbourhood, gm-public
// only (see [Store.AgentNodeFacts]).
func (v PromptKGView) AgentNodeFacts(ctx context.Context, agentID uuid.UUID) ([]KGNode, error) {
	return v.s.AgentNodeFacts(ctx, agentID)
}
