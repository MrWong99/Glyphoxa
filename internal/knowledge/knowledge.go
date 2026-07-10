// Package knowledge is the storage-backed adapter behind the read-only knowledge
// Tools (#296, S1): it wraps internal/storage's retrieval paths in the neutral,
// storage-free interfaces pkg/tool declares (tool.TranscriptSearcher,
// tool.KGReader), so pkg/tool never imports internal/storage. That import edge
// would be a cycle — storage already imports pkg/tool (the grant editor lists the
// Registry) — which is exactly why the seam is inverted here: the sources are
// injected as narrow interfaces and this package is the production wiring.
//
// It mirrors the kgfacts pattern (internal/kgfacts): a Store for the reads plus a
// Sessions source for the active Campaign, with "no active session → error" so a
// Tool called outside a live turn reports it cleanly rather than reading nothing.
// The load-bearing invariant is SearchFacts DROPPING gm_private rows:
// storage.SearchNodes is the GM-facing wiki search and does NOT filter them
// (ADR-0008), so an unfiltered pass would leak a GM secret into an NPC's prompt.
package knowledge

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
)

// ErrNoActiveSession is returned by the campaign-scoped reads when no Voice
// Session is live: the active session is what resolves the Campaign to scope to,
// so without one there is nothing to search. The Tool handler surfaces it as an
// error result the LLM can read ("knowledge is unavailable right now"), never a
// panic or a silent empty read against the wrong Campaign.
var ErrNoActiveSession = errors.New("knowledge: no active voice session")

// Store is the narrow set of storage reads the adapter needs. *storage.Store
// satisfies it. Kept local (not the whole *storage.Store) so the adapter's
// contract is explicit and unit-fakeable.
type Store interface {
	// SearchNodes is the GM-facing KG wiki search — it INCLUDES gm_private Nodes,
	// so SearchFacts must filter them before they reach a prompt.
	SearchNodes(ctx context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.KGNode, error)
	// SearchTranscriptLines is the campaign-scoped transcript search (#120).
	SearchTranscriptLines(ctx context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.TranscriptLine, error)
	// AgentNodeFacts is the Agent's own edge-aware neighbourhood, already
	// gm_private-filtered (#133).
	AgentNodeFacts(ctx context.Context, agentID uuid.UUID) ([]storage.KGNode, error)
}

// Sessions reports the active Voice Session (for its Campaign). *session.Manager
// satisfies it via Snapshot, the same shape kgfacts depends on; defined locally
// so this package does not import session.
type Sessions interface {
	Snapshot() (storage.VoiceSession, bool)
}

// Adapter implements both tool.TranscriptSearcher and tool.KGReader over a
// storage Store and the active-session source. Safe for concurrent use (its deps
// are). Build it once at web boot and set it on the session Manager's base config.
type Adapter struct {
	store    Store
	sessions Sessions
}

// New builds the adapter. Both deps must be non-nil — they are wiring
// requirements, so a nil is a boot bug, not a runtime condition.
func New(store Store, sessions Sessions) *Adapter {
	if store == nil || sessions == nil {
		panic("knowledge: New: nil store or sessions")
	}
	return &Adapter{store: store, sessions: sessions}
}

// activeCampaign resolves the Campaign the live session scopes reads to, or
// ErrNoActiveSession when idle.
func (a *Adapter) activeCampaign() (uuid.UUID, error) {
	s, ok := a.sessions.Snapshot()
	if !ok {
		return uuid.Nil, ErrNoActiveSession
	}
	return s.CampaignID, nil
}

// SearchTranscript implements [tool.TranscriptSearcher]. It searches the active
// Campaign's persisted transcript (#120) and projects each Line into a
// storage-free [tool.TranscriptHit]. The Campaign comes from the active session,
// never the caller, so the search can never cross Campaigns. No session yields
// ErrNoActiveSession.
func (a *Adapter) SearchTranscript(ctx context.Context, query string, limit int) ([]tool.TranscriptHit, error) {
	campaignID, err := a.activeCampaign()
	if err != nil {
		return nil, err
	}
	lines, err := a.store.SearchTranscriptLines(ctx, campaignID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("knowledge: search transcript: %w", err)
	}
	hits := make([]tool.TranscriptHit, 0, len(lines))
	for _, l := range lines {
		hits = append(hits, tool.TranscriptHit{
			Who:  l.Who,
			Kind: l.Kind,
			Text: l.Text,
			At:   l.TS,
		})
	}
	return hits, nil
}

// OwnNodeFacts implements [tool.KGReader]. It returns the Agent's own linked Node
// plus its single-hop neighbourhood (#133), already gm_private-filtered and
// edge-aware by storage. The agentID is the caller identity threaded from the
// turn ctx (never the LLM args); an empty or unparseable id has no neighbourhood
// to scope to and yields no facts (never a wider fallback).
func (a *Adapter) OwnNodeFacts(ctx context.Context, agentID string) ([]tool.KGFact, error) {
	aid, err := uuid.Parse(agentID)
	if err != nil || aid == uuid.Nil {
		return nil, nil
	}
	nodes, err := a.store.AgentNodeFacts(ctx, aid)
	if err != nil {
		return nil, fmt.Errorf("knowledge: own node facts: %w", err)
	}
	return toFacts(nodes), nil
}

// SearchFacts implements [tool.KGReader]. It runs the campaign-wide KG search for
// the Butler's grant, then DROPS every gm_private Node — the load-bearing
// ADR-0008 filter: storage.SearchNodes is GM-facing and does not filter, so an
// unfiltered result would leak a GM secret into an NPC's prompt. No session
// yields ErrNoActiveSession.
func (a *Adapter) SearchFacts(ctx context.Context, query string, limit int) ([]tool.KGFact, error) {
	campaignID, err := a.activeCampaign()
	if err != nil {
		return nil, err
	}
	nodes, err := a.store.SearchNodes(ctx, campaignID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("knowledge: search facts: %w", err)
	}
	public := nodes[:0:0]
	for _, n := range nodes {
		if n.GMPrivate {
			continue // NEVER surface a GM-private Node to a prompt (ADR-0008).
		}
		public = append(public, n)
	}
	return toFacts(public), nil
}

// toFacts projects storage Nodes into storage-free [tool.KGFact]s, mapping the
// Node type onto its GM-facing label here (so pkg/tool needs no storage enum).
func toFacts(nodes []storage.KGNode) []tool.KGFact {
	out := make([]tool.KGFact, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, tool.KGFact{
			Name: n.Name,
			Type: typeLabel(n.Type),
			Body: n.Body,
		})
	}
	return out
}

// typeLabel maps a Node type onto its GM-facing label, mirroring kgfacts. An
// unknown type falls back to "Note" (the DB enum keeps this exhaustive).
func typeLabel(t storage.KGNodeType) string {
	switch t {
	case storage.KGNodeCharacter:
		return "Character"
	case storage.KGNodeNPC:
		return "NPC"
	case storage.KGNodeLocation:
		return "Location"
	case storage.KGNodeFaction:
		return "Faction"
	case storage.KGNodeItem:
		return "Item"
	case storage.KGNodePlotThread:
		return "Plot thread"
	case storage.KGNodeNote:
		return "Note"
	default:
		return "Note"
	}
}

// Compile-time assertions that the adapter satisfies the tool seams.
var (
	_ tool.TranscriptSearcher = (*Adapter)(nil)
	_ tool.KGReader           = (*Adapter)(nil)
)
