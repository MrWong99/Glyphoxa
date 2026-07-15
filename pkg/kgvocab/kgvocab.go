// Package kgvocab is the single home of the Knowledge Proposal write-contract
// vocabulary (#449, ADR-0052/ADR-0008): the proposal write version, the proposal
// kind identifiers, the closed Edge relation and Node type vocabularies, and the
// GM-facing Node type labels.
//
// It is a LEAF package — it imports nothing from this repository — so both sides
// of the write contract can consume one definition: pkg/tool (the create path,
// which must stay storage-free) declares its JSON-schema enums and validates
// arguments from it, and internal/storage (the approve path), internal/rpc (the
// review surface), internal/kgfacts and internal/knowledge (the label renderers)
// re-check the same shapes against the same values. Adding a relation or node
// type is therefore ONE edit here: every validator, schema enum, and typed
// constant is compiler-linked to these declarations, so the create and approve
// sides cannot drift apart and label drift is impossible.
//
// The wire/DB representations are exactly these strings (lowercase snake_case);
// the Postgres kg_edge_type / kg_node_type enums mirror them.
package kgvocab

// ProposalWriteVersion is the schema version stamped onto every ProposedWrite
// ("v":1, ADR-0052), so a later shape change is detectable on the stored jsonb.
// The create path stamps it; the approve and review paths reject a row whose
// version differs as unreadable.
const ProposalWriteVersion = 1

// Proposal kinds (ADR-0052): the tagged-union discriminator of a ProposedWrite.
// fact/edge may be proposed own_node or campaign; node (a brand-new wiki entry)
// is Butler-only (campaign scope).
const (
	KindFact = "fact"
	KindEdge = "edge"
	KindNode = "node"
)

// The closed Edge relation vocabulary (ADR-0008): every relation an Edge can
// carry and an Agent may propose.
const (
	RelationResidesIn      = "resides_in"
	RelationMemberOf       = "member_of"
	RelationOwns           = "owns"
	RelationKnows          = "knows"
	RelationEnemyOf        = "enemy_of"
	RelationAllyOf         = "ally_of"
	RelationParentOf       = "parent_of"
	RelationParticipatedIn = "participated_in"
	RelationMentionedIn    = "mentioned_in"
)

// relations is the relation vocabulary in its canonical (schema enum) order.
var relations = []string{
	RelationResidesIn,
	RelationMemberOf,
	RelationOwns,
	RelationKnows,
	RelationEnemyOf,
	RelationAllyOf,
	RelationParentOf,
	RelationParticipatedIn,
	RelationMentionedIn,
}

// Relations returns the closed relation vocabulary in canonical order, as a
// fresh slice the caller may keep.
func Relations() []string { return append([]string(nil), relations...) }

// ValidRelation reports whether s is one of the closed relation vocabulary.
func ValidRelation(s string) bool {
	for _, r := range relations {
		if s == r {
			return true
		}
	}
	return false
}

// The closed Node type vocabulary (ADR-0008): the kg_node_type values.
const (
	NodeTypeCharacter  = "character"
	NodeTypeNPC        = "npc"
	NodeTypeLocation   = "location"
	NodeTypeFaction    = "faction"
	NodeTypeItem       = "item"
	NodeTypePlotThread = "plot_thread"
	NodeTypeNote       = "note"
)

// nodeTypes is the node-type vocabulary in its canonical (enum / schema) order.
var nodeTypes = []string{
	NodeTypeCharacter,
	NodeTypeNPC,
	NodeTypeLocation,
	NodeTypeFaction,
	NodeTypeItem,
	NodeTypePlotThread,
	NodeTypeNote,
}

// NodeTypes returns the closed node-type vocabulary in canonical order, as a
// fresh slice the caller may keep.
func NodeTypes() []string { return append([]string(nil), nodeTypes...) }

// ValidNodeType reports whether s is one of the closed node-type vocabulary.
func ValidNodeType(s string) bool {
	_, ok := nodeTypeLabels[s]
	return ok
}

// nodeTypeLabels maps each node type to its GM-facing label — the one label map
// kgfacts and knowledge used to duplicate.
var nodeTypeLabels = map[string]string{
	NodeTypeCharacter:  "Character",
	NodeTypeNPC:        "NPC",
	NodeTypeLocation:   "Location",
	NodeTypeFaction:    "Faction",
	NodeTypeItem:       "Item",
	NodeTypePlotThread: "Plot thread",
	NodeTypeNote:       "Note",
}

// NodeTypeLabel returns the GM-facing label for a node type ("Character",
// "Plot thread", …). An unknown type falls back to "Note" defensively — the DB
// enum keeps the input exhaustive in practice.
func NodeTypeLabel(t string) string {
	if l, ok := nodeTypeLabels[t]; ok {
		return l
	}
	return "Note"
}
