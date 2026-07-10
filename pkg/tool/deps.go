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
