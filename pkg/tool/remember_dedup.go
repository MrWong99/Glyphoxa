package tool

import (
	"strings"

	"github.com/MrWong99/Glyphoxa/internal/textnorm"
)

// Write-time dedup for remember_knowledge (#411, ADR-0052 mechanism a): before a
// proposal row is created, the handler compares its salient text — normalized
// (casefold, punctuation stripped, whitespace collapsed) via the shared
// [textnorm.Normalize] — against what the KG already holds for the same target:
// the target's pending proposals and its established facts. An exact/normalized
// hit creates NO row and the tool reports the fact is already noted, so the model
// stops re-remembering the same thing every turn. Similarity beyond exact/
// normalized (embeddings) is out of scope (ADR-0052 no-auto-merge posture).

// ProposalSalient projects a [ProposedWrite] onto the free text the dedup guard
// compares. A fact is its statement; an edge is its relation and target; a new
// entry is its name and body. The result is compared normalized, so exact casing
// and punctuation here do not matter.
func ProposalSalient(w ProposedWrite) string {
	switch w.Kind {
	case proposalKindFact:
		return w.Fact
	case proposalKindEdge:
		return strings.TrimSpace(w.Relation + " " + w.Target)
	case proposalKindNode:
		return strings.TrimSpace(w.Name + " " + w.Body)
	default:
		return strings.TrimSpace(w.Fact + " " + w.Name + " " + w.Body)
	}
}

// ProposalTargetKey identifies the entity a proposal is ABOUT, so the guard only
// compares proposals addressing the same target — a coincidental text clash
// between two different subjects is never suppressed. An own_node proposal is
// keyed by its anchor node id (subject is cosmetic there); a campaign fact/edge by
// its normalized subject; a new entry by its own normalized name. An empty key
// means "no identifiable target" and the caller skips dedup.
func ProposalTargetKey(w ProposedWrite) string {
	if w.NodeID != "" {
		return "id:" + w.NodeID
	}
	if s := textnorm.Normalize(w.Subject); s != "" {
		return "subj:" + s
	}
	if w.Kind == proposalKindNode {
		if n := textnorm.Normalize(w.Name); n != "" {
			return "name:" + n
		}
	}
	return ""
}

// firstKnownMatch returns the raw existing text whose normalized form equals the
// normalized salient text, or ok=false when the salient is new (or empty). It
// returns the RAW candidate so the tool result can echo the exact known wording
// back to the model.
func firstKnownMatch(salient string, known []string) (string, bool) {
	want := textnorm.Normalize(salient)
	if want == "" {
		return "", false
	}
	for _, k := range known {
		if textnorm.Normalize(k) == want {
			return k, true
		}
	}
	return "", false
}
