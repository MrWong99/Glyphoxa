package tool

import (
	"context"
	"time"
)

// Deps carries the injected read sources the built-in knowledge Tools need
// (S1, #296). It exists so pkg/tool stays free of an internal/storage import:
// storage already imports pkg/tool (the grant editor lists the Registry), so a
// reverse edge would be an import cycle. Instead the retrieval paths are handed
// in through these narrow, storage-free interfaces, satisfied by the adapter in
// internal/knowledge.
//
// Every field is optional. A nil source means the Tool is still REGISTERED (so
// the grant editor's catalog is identical in every mode) but its Execute returns
// an "unavailable in this mode" error result rather than a nil-pointer panic —
// the standalone voice bench and the grant-editor RPC build a zero Deps and
// still surface the full Tool list. The live web boot fills the fields.
type Deps struct {
	// Transcripts backs transcript_search. nil ⇒ the Tool is registered but its
	// Execute reports it is unavailable.
	Transcripts TranscriptSearcher
	// KG backs kg_query. nil ⇒ the Tool is registered but its Execute reports it
	// is unavailable.
	KG KGReader
	// KGW backs remember_knowledge (#300, ADR-0052). nil ⇒ the Tool is registered
	// but its Execute reports it is unavailable in this mode (the grant-editor RPC
	// and voice bench build a zero Deps).
	KGW KGWriter
}

// ProposedWrite is the versioned, storage-free payload one remember_knowledge
// call proposes to the Knowledge Graph (#300, ADR-0052). It is a tagged union
// over Kind ("fact", "edge", "node"); the adapter marshals it to the
// knowledge_proposal.proposed_write jsonb verbatim, so the field set and json
// tags ARE the on-disk contract. V is the schema version (always 1) so a future
// shape change is detectable. Fields not part of a Kind stay zero and omitempty
// keeps them out of the jsonb.
//
// Per ADR-0052 a proposal is the ONLY effect of the Tool — nothing touches
// kg_node/kg_edge until the GM approves in PR-b's review surface. A speculative
// draft (ADR-0053 ensemble fan-out) that is later discarded may still leave a
// proposal row; that is ADR-0052-consistent (the NPC heard the fact) and the GM
// review is the safety net.
type ProposedWrite struct {
	V        int    `json:"v"`
	Kind     string `json:"kind"`
	NodeID   string `json:"node_id,omitempty"`
	Subject  string `json:"subject,omitempty"`
	Fact     string `json:"fact,omitempty"`
	Relation string `json:"relation,omitempty"`
	Target   string `json:"target,omitempty"`
	NodeType string `json:"node_type,omitempty"`
	Name     string `json:"name,omitempty"`
	Body     string `json:"body,omitempty"`
}

// KGNodeRef is a storage-free handle to an Agent's own linked Node (ADR-0008
// NPC-Node↔Agent link): its id (the anchor a Character NPC's own_node proposals
// attach to) and its display Name (the subject the handler stamps over whatever
// the LLM supplied). Kept storage-free so pkg/tool never sees a storage type.
type KGNodeRef struct {
	ID   string
	Name string
}

// KGWriter is the narrow write seam remember_knowledge needs (#300, ADR-0052).
// It is deliberately not part of [KGReader]: writing is a distinct authority.
// *knowledge.Adapter satisfies it; a nil KGW on [Deps] reports unavailable at
// Execute.
//
//   - OwnNode resolves the caller's own linked Node for own_node-scoped
//     proposals. The agentID is the turn ctx caller ([CallerID]), never the LLM
//     args. ok=false means the Agent has no linked wiki entry — the handler
//     refuses rather than proposing against a wrong or absent Node.
//   - CreateProposal records the proposal row (status pending). It is the ONLY
//     side effect; per ADR-0052 barge semantics the adapter writes it under a
//     cancel-immune context so a barged turn's proposal is never rolled back.
type KGWriter interface {
	OwnNode(ctx context.Context, agentID string) (KGNodeRef, bool, error)
	CreateProposal(ctx context.Context, agentID string, w ProposedWrite) error
}

// TranscriptHit is one persisted transcript Line the knowledge adapter surfaces
// to transcript_search — a storage-free projection so pkg/tool never sees a
// storage type. Who is the speaker's display name, Kind the line kind (human
// utterance vs Agent reply), Text the spoken words, At the wall-clock time.
type TranscriptHit struct {
	Who  string
	Kind string
	Text string
	At   time.Time
}

// TranscriptSearcher is the narrow read transcript_search needs: a relevance
// search over the active Campaign's persisted transcript. The Campaign is
// resolved INSIDE the adapter from the active Voice Session (never passed by the
// LLM), so the model cannot search another Campaign's transcript. A limit ≤ 0 is
// the adapter's default. *knowledge.Store satisfies it.
type TranscriptSearcher interface {
	SearchTranscript(ctx context.Context, query string, limit int) ([]TranscriptHit, error)
}

// KGFact is one Knowledge Graph fact the adapter surfaces to kg_query — a
// storage-free projection. Type is the GM-facing label ("Character", "Location",
// …), already mapped by the adapter so pkg/tool needs no storage enum. Body is
// the Node's prose.
type KGFact struct {
	Name string
	Type string
	Body string
}

// KGReader is the narrow read kg_query needs, in two scopes (S1/S3, ADR-0029):
//
//   - OwnNodeFacts returns one Agent's own linked Node plus its single-hop
//     neighbourhood, already gm_private-filtered and edge-aware (it wraps
//     storage.AgentNodeFacts). It is the least-privilege scope an NPC's grant
//     narrows to — the handler resolves the agentID from the caller identity, not
//     the LLM's args.
//   - SearchFacts is the campaign-wide relevance search the Butler's grant uses.
//     The adapter MUST drop gm_private rows: storage.SearchNodes is GM-facing and
//     does NOT filter them, so an unfiltered pass would leak GM secrets into an
//     NPC prompt (ADR-0008, the load-bearing filter).
//
// *knowledge.Store satisfies it.
type KGReader interface {
	OwnNodeFacts(ctx context.Context, agentID string) ([]KGFact, error)
	SearchFacts(ctx context.Context, query string, limit int) ([]KGFact, error)
}
