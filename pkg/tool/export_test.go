package tool

import (
	"github.com/MrWong99/Glyphoxa/internal/textnorm"
	"github.com/MrWong99/Glyphoxa/pkg/kgvocab"
)

// proposalTargetKey is the test fakes' notion of "the same target" for a
// [KGWriter]: an own_node proposal by its anchor node id, a campaign fact/edge by
// its normalized subject, a new entry by its own normalized name. The production
// adapter (internal/knowledge's canonicalTargetKey) additionally resolves a
// subject that names a real Node onto its id. "" means no identifiable target.
func proposalTargetKey(w ProposedWrite) string {
	if w.NodeID != "" {
		return "id:" + w.NodeID
	}
	if s := textnorm.Normalize(w.Subject); s != "" {
		return "subj:" + s
	}
	if w.Kind == kgvocab.KindNode {
		if n := textnorm.Normalize(w.Name); n != "" {
			return "name:" + n
		}
	}
	return ""
}
