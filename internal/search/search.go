// Package search is the Campaign-wide retrieval engine behind the Ctrl+K
// command palette (#591) and, later, the Butler planning chat's tool belt
// (#592: search_transcripts / find_node — appearances_of already has its own
// read). One engine, two consumers (the epic's "build the engine once"), so the
// methods are shaped as clean campaign-scoped queries, never UI aggregates:
//
//   - Nodes: keyword fulltext over name + Aspect values + body (the weighted
//     kg_node vectors, ADR-0008) — a GM searching "Bart" wants Bart, name-first.
//   - Transcripts: semantic ANN over the Transcript Chunk embeddings (ADR-0011)
//     when an embeddings provider is wired, degrading HONESTLY (the result says
//     which mode served it) to the keyword Transcript Line search when none is
//     or the embed fails. The query embedding is priced and logged, never
//     cap-gated (the mapgen off-session posture; ledger flush is the #592 ADR).
//   - Highlights: keyword fulltext over promoted Highlights' excerpt + reason.
//
// Every query takes the caller's [Access] explicitly, so the ADR-0056 player
// tiers land without re-plumbing: a player-tier caller never receives
// gm_private material (the public node vector matches for it, #542), and
// own-character access never searches campaign transcripts. Today's only web
// principal is the operator (ADR-0039/0055) — the RPC layer passes
// [AccessOperator] — but the exclusions are enforced and tested here, not
// deferred to the future policy seam.
package search

import (
	"context"
	"errors"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings"
)

// Access is the caller's resolved capability tier, decided by the auth layer —
// NEVER wire input: a request field would let a client claim a wider tier. The
// player values mirror the ADR-0056 Player Access Levels; AccessOperator is the
// GM-tier web principal (ADR-0039).
type Access int

const (
	// AccessOperator is the GM-tier caller: every source, gm_private included.
	AccessOperator Access = iota
	// AccessCampaignTranscripts is the ADR-0056 campaign-transcripts level:
	// transcript + highlight search, public node material only. The per-Campaign
	// share toggle that gates the level itself is the policy seam's check
	// (ADR-0056 intersects link × toggle × level), not repeated here.
	AccessCampaignTranscripts
	// AccessCampaignHighlights is the ADR-0056 campaign-highlights level:
	// highlight search and public node material; never transcript text.
	AccessCampaignHighlights
	// AccessOwnCharacter is the ADR-0056 own-character level. It must NEVER
	// search campaign transcripts (#591), and its highlight slice ("the promoted
	// Highlights they appear in") needs the caller's Character links — which
	// don't exist until the Linked Player lane is built — so it fails CLOSED
	// (empty results) rather than over-serving.
	AccessOwnCharacter
)

// Store is the narrow storage surface the engine needs; *storage.Store
// satisfies it.
type Store interface {
	// SearchNodes / SearchPublicNodes are the weighted kg_node fulltext searches;
	// the public variant matches against the gm_private-free vector AND withholds
	// private Aspects (#542) — one flag, three privacy decisions.
	SearchNodes(ctx context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.KGNode, error)
	SearchPublicNodes(ctx context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.KGNode, error)
	// SearchChunksByCampaign is the campaign-scoped ANN over embedded Transcript
	// Chunks (ADR-0011); SearchTranscriptLines is the keyword degrade path;
	// FirstLineIDAtOrAfter anchors a chunk hit on a deep-linkable Line.
	SearchChunksByCampaign(ctx context.Context, campaignID uuid.UUID, query []float32, k int) ([]storage.ChunkMatch, error)
	SearchTranscriptLines(ctx context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.TranscriptLine, error)
	FirstLineIDAtOrAfter(ctx context.Context, voiceSessionID uuid.UUID, at time.Time) (string, error)
	// SearchPromotedHighlights is the promoted-only Highlight fulltext search.
	SearchPromotedHighlights(ctx context.Context, tenantID, campaignID uuid.UUID, query string, limit int) ([]storage.Highlight, error)
}

// Embedder embeds the query text for the semantic transcript tier; the resolved
// embeddings provider satisfies it. Nil (no provider wired) degrades every
// transcript search to the keyword Line path.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// embedTimeout bounds the query embedding call — a slow provider must not hang
// the palette; on timeout the search degrades to keyword (the
// ListSimilarKnowledge posture).
const embedTimeout = 15 * time.Second

// SnippetRunes caps one transcript hit's rendered snippet, in runes (rune-safe
// truncation, never a split codepoint) — a 6-utterance chunk must not dominate
// a palette row.
const SnippetRunes = 300

// Engine runs the three campaign-scoped searches. Build with New; wire the
// optional embedder with SetEmbedder once at boot.
type Engine struct {
	store Store
	log   *slog.Logger

	// embedder enables the semantic transcript tier; nil degrades to keyword.
	// provider/model label the query-embed spend estimate (ADR-0045 spirit: the
	// meter prices per provider; the embeddings interface reports no usage).
	embedder Embedder
	provider observe.Provider
	model    string
}

// New builds the engine over the storage surface. A nil log uses the default.
func New(store Store, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{store: store, log: log}
}

// SetEmbedder wires the semantic transcript tier: the resolved embeddings
// provider plus the (provider, model) pair the query-embed pricing attributes
// to. Called once at boot before serving; nil emb is a no-op (keyword-only).
func (e *Engine) SetEmbedder(emb Embedder, provider observe.Provider, model string) {
	if emb == nil {
		return
	}
	e.embedder = emb
	e.provider = provider
	e.model = model
}

// SearchNodes is the keyword Node search (the palette's Entries group; the
// future find_node tool). AccessOperator searches the GM vector (gm_private
// Nodes and Aspects included — the wiki is the GM's); every player tier gets
// the public vector, so a secret can never leak through a HIT, let alone a
// payload (#542).
func (e *Engine) SearchNodes(ctx context.Context, campaignID uuid.UUID, access Access, query string, limit int) ([]storage.KGNode, error) {
	if access == AccessOperator {
		return e.store.SearchNodes(ctx, campaignID, query, limit)
	}
	return e.store.SearchPublicNodes(ctx, campaignID, query, limit)
}

// TranscriptHit is one transcript search hit, either grain. The
// (VoiceSessionID, LineID) pair is the deep-link key — relay line ids RESTART
// per session, so the pair is never split.
type TranscriptHit struct {
	// VoiceSessionID is the owning Voice Session; uuid.Nil for a legacy chunk
	// persisted without one (the hit renders, but has nowhere to link).
	VoiceSessionID uuid.UUID
	// LineID anchors the hit in the Session screen: the Line's own id (keyword)
	// or the first Line at/after the chunk's start (semantic); "" when none
	// resolves.
	LineID string
	// Who / Kind carry the keyword hit's speaker context; both empty for a
	// semantic chunk hit (a chunk spans speakers — the snippet carries names
	// inline).
	Who  string
	Kind string
	// At is the session-date context: the Line's timestamp or the chunk's start.
	At time.Time
	// Snippet is the matched text, truncated to SnippetRunes.
	Snippet string
}

// TranscriptResults carries the hits plus which strategy served them, so the
// caller can label a degraded search honestly instead of silently changing
// meaning (#591).
type TranscriptResults struct {
	// Semantic is true when the embeddings ANN served the hits; false on the
	// keyword Line path (no embedder, embed failure, or a denied tier's empty
	// result).
	Semantic bool
	Hits     []TranscriptHit
}

// SearchTranscripts searches the Campaign's spoken record (the palette's
// Transcripts group; the future search_transcripts tool): semantic ANN over
// embedded Transcript Chunks when possible, else keyword over Transcript
// Lines. Only the operator and the campaign-transcripts level may search
// transcript text at all — campaign-highlights and own-character return empty
// WITHOUT a provider call (#591: "own-character access must never search
// campaign transcripts"; no spend for a denied tier).
func (e *Engine) SearchTranscripts(ctx context.Context, campaignID uuid.UUID, access Access, query string, limit int) (TranscriptResults, error) {
	if access != AccessOperator && access != AccessCampaignTranscripts {
		return TranscriptResults{}, nil
	}

	if vec, ok := e.embedQuery(ctx, query); ok {
		matches, err := e.store.SearchChunksByCampaign(ctx, campaignID, vec, limit)
		if err != nil {
			return TranscriptResults{}, err
		}
		hits := make([]TranscriptHit, 0, len(matches))
		for _, m := range matches {
			hit := TranscriptHit{
				VoiceSessionID: m.Chunk.VoiceSessionID,
				At:             m.Chunk.StartedAt,
				Snippet:        truncateRunes(m.Chunk.Content, SnippetRunes),
			}
			if m.Chunk.VoiceSessionID != uuid.Nil {
				lineID, err := e.store.FirstLineIDAtOrAfter(ctx, m.Chunk.VoiceSessionID, m.Chunk.StartedAt)
				switch {
				case errors.Is(err, storage.ErrNotFound):
					// No Line at/after the chunk's window (e.g. flushed after the
					// last persisted Line): the hit renders without a scroll target.
				case err != nil:
					return TranscriptResults{}, err
				default:
					hit.LineID = lineID
				}
			}
			hits = append(hits, hit)
		}
		return TranscriptResults{Semantic: true, Hits: hits}, nil
	}

	lines, err := e.store.SearchTranscriptLines(ctx, campaignID, query, limit)
	if err != nil {
		return TranscriptResults{}, err
	}
	hits := make([]TranscriptHit, 0, len(lines))
	for _, l := range lines {
		hits = append(hits, TranscriptHit{
			VoiceSessionID: l.VoiceSessionID,
			LineID:         l.LineID,
			Who:            l.Who,
			Kind:           l.Kind,
			At:             l.TS,
			Snippet:        truncateRunes(l.Text, SnippetRunes),
		})
	}
	return TranscriptResults{Semantic: false, Hits: hits}, nil
}

// SearchHighlights searches the Campaign's PROMOTED Highlights (the palette's
// Highlights group). Candidates never match — a Highlight Candidate is not yet
// a Highlight (ADR-0051 GM curation stays private) — and that exclusion lives
// in the SQL, before the LIMIT. own-character fails closed: its "promoted
// Highlights they appear in" slice needs the caller's Character links, which
// the Linked Player lane hasn't built yet.
func (e *Engine) SearchHighlights(ctx context.Context, tenantID, campaignID uuid.UUID, access Access, query string, limit int) ([]storage.Highlight, error) {
	if access == AccessOwnCharacter {
		return nil, nil
	}
	return e.store.SearchPromotedHighlights(ctx, tenantID, campaignID, query, limit)
}

// embedQuery embeds the query text for the semantic tier, with the three-way
// degradation guard the Knowledge Proposal similarity hint pinned down: nil
// embedder, embed failure/timeout/wrong count, or wrong dimension all return
// ok=false — degrade, never error (a wrong-dim vector would fail the ::vector
// cast as CodeInternal). A successful embed is priced and logged with its
// estimated USD (#591): tokens are the documented ceil(runes/4) estimate
// (ADR-0045's no-usage fallback — the embeddings interface reports none),
// priced per provider (spend.EstimateEmbeddingUSD), never cap-gated (ADR-0046
// caps are live-session-only) and not yet ledger-flushed (the #592 ADR owns the
// off-session flush boundary).
func (e *Engine) embedQuery(ctx context.Context, query string) ([]float32, bool) {
	if e.embedder == nil {
		return nil, false
	}
	embedCtx, cancel := context.WithTimeout(ctx, embedTimeout)
	defer cancel()
	vecs, err := e.embedder.Embed(embedCtx, []string{query})
	switch {
	case err != nil || len(vecs) != 1:
		e.log.Warn("search: query embed failed; degrading to keyword line search", "err", err, "vectors", len(vecs))
		return nil, false
	case len(vecs[0]) != embeddings.Dim:
		e.log.Warn("search: query embedding has wrong dimension; degrading to keyword line search",
			"got", len(vecs[0]), "want", embeddings.Dim)
		return nil, false
	}

	tokens := (utf8.RuneCountInString(query) + 3) / 4
	e.log.Info("search: query embedded",
		"provider", e.provider,
		"model", e.model,
		"estimated_tokens", tokens,
		"estimated_usd", spend.EstimateEmbeddingUSD(e.provider, tokens))
	return vecs[0], true
}

// truncateRunes trims s to at most max runes, appending an ellipsis when it
// cut — rune-safe so a multibyte character is never split (mirrors kgfacts).
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
