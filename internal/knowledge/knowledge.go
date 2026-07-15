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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/textnorm"
	"github.com/MrWong99/Glyphoxa/pkg/kgvocab"
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
	// SearchPublicNodes is the prompt-facing KG search: gm_private Nodes are
	// EXCLUDED in the query (before the LIMIT), so a GM-only Node never reaches a
	// prompt and a public match is not starved by top-ranked private hits (ADR-0008).
	SearchPublicNodes(ctx context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.KGNode, error)
	// SearchTranscriptLines is the campaign-scoped transcript search (#120).
	SearchTranscriptLines(ctx context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.TranscriptLine, error)
	// AgentNodeFacts is the Agent's own edge-aware neighbourhood, already
	// gm_private-filtered (#133).
	AgentNodeFacts(ctx context.Context, agentID uuid.UUID) ([]storage.KGNode, error)
	// AgentLinkedNode is the Agent's own linked Node (the NPC-Node↔Agent link),
	// the anchor an own_node-scoped remember_knowledge proposal attaches to (#300,
	// ADR-0052). ok=false means the Agent has no linked entry.
	AgentLinkedNode(ctx context.Context, agentID uuid.UUID) (storage.KGNode, bool, error)
	// CreateKnowledgeProposal records a pending Knowledge Proposal (#300,
	// ADR-0052) — the sole effect of remember_knowledge. proposedWrite is the raw
	// jsonb payload; nothing touches kg_node/kg_edge until the GM approves.
	CreateKnowledgeProposal(ctx context.Context, campaignID, agentID uuid.UUID, proposedWrite []byte) error
	// ListPendingKnowledgeProposals backs the #411 write-time dedup: the pending
	// queue a re-proposal is checked against.
	ListPendingKnowledgeProposals(ctx context.Context, campaignID uuid.UUID) ([]storage.KnowledgeProposal, error)
	// ListNodes resolves a campaign-scoped proposal's subject Node by name so its
	// established body facts can be dedup candidates (#411).
	ListNodes(ctx context.Context, campaignID uuid.UUID) ([]storage.KGNode, error)
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
// the Butler's grant over SearchPublicNodes, which EXCLUDES gm_private Nodes in
// the query itself — the load-bearing ADR-0008 guard. The exclusion must be in
// the query, not a post-fetch Go filter: filtering after the SQL LIMIT would drop
// the top-N ranked hits when they are all gm_private and starve a public match
// ranked just past the limit. No session yields ErrNoActiveSession.
func (a *Adapter) SearchFacts(ctx context.Context, query string, limit int) ([]tool.KGFact, error) {
	campaignID, err := a.activeCampaign()
	if err != nil {
		return nil, err
	}
	nodes, err := a.store.SearchPublicNodes(ctx, campaignID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("knowledge: search facts: %w", err)
	}
	return toFacts(nodes), nil
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

// typeLabel maps a Node type onto its GM-facing label via the single label map
// in pkg/kgvocab (#449); an unknown type falls back to "Note" there.
func typeLabel(t storage.KGNodeType) string {
	return kgvocab.NodeTypeLabel(string(t))
}

// proposalWriteTimeout bounds the cancel-immune proposal INSERT: ADR-0052 barge
// semantics require the write to SURVIVE the turn's cancellation (a barged reply
// still yields its proposal), so it runs under context.WithoutCancel; the timeout
// then prevents a goroutine leak if the DB is wedged.
const proposalWriteTimeout = 5 * time.Second

// OwnNode implements [tool.KGWriter]. It resolves the caller's own linked Node
// (ADR-0008) for own_node-scoped proposals. The agentID is the turn ctx caller,
// never the LLM args; an empty or unparseable id has no linked Node (ok=false),
// so the handler refuses rather than proposing against a wrong Node.
func (a *Adapter) OwnNode(ctx context.Context, agentID string) (tool.KGNodeRef, bool, error) {
	aid, err := uuid.Parse(agentID)
	if err != nil || aid == uuid.Nil {
		return tool.KGNodeRef{}, false, nil
	}
	node, ok, err := a.store.AgentLinkedNode(ctx, aid)
	if err != nil {
		return tool.KGNodeRef{}, false, fmt.Errorf("knowledge: own node: %w", err)
	}
	if !ok {
		return tool.KGNodeRef{}, false, nil
	}
	return tool.KGNodeRef{ID: node.ID.String(), Name: node.Name}, true, nil
}

// CreateProposal implements [tool.KGWriter]. It resolves the Campaign from the
// active session (no session ⇒ ErrNoActiveSession), marshals the storage-free
// [tool.ProposedWrite] to jsonb HERE, and INSERTs the pending proposal. The
// insert runs under context.WithoutCancel(ctx) with a bounded timeout so a
// barge-in (turn ctx cancel) does not roll back a proposal the NPC already made
// (ADR-0052), while a wedged DB still cannot leak the goroutine.
func (a *Adapter) CreateProposal(ctx context.Context, agentID string, w tool.ProposedWrite) error {
	campaignID, err := a.activeCampaign()
	if err != nil {
		return err
	}
	aid, err := uuid.Parse(agentID)
	if err != nil || aid == uuid.Nil {
		return fmt.Errorf("knowledge: create proposal: invalid authoring agent id %q", agentID)
	}
	payload, err := json.Marshal(w)
	if err != nil {
		return fmt.Errorf("knowledge: create proposal: marshal proposed write: %w", err)
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), proposalWriteTimeout)
	defer cancel()
	if err := a.store.CreateKnowledgeProposal(writeCtx, campaignID, aid, payload); err != nil {
		return fmt.Errorf("knowledge: create proposal: %w", err)
	}
	return nil
}

// ExistingKnowledge implements [tool.KGWriter] (#411, ADR-0052 mechanism a): it
// reports what the KG already holds for a proposal's target so the handler can
// suppress an exact/normalized re-proposal and echo the target's pending proposals
// back to the model. It gathers two candidate sets, both scoped to the target
// entity (never global): the target Node's established body facts (its body split
// into non-empty lines) and the salient text of every PENDING proposal addressing
// the same target.
//
// The target key is UNIFIED across the two write paths: an own_node proposal keys
// on its anchor node id, and a campaign proposal keys on its subject NAME — but the
// same real entity would then carry two keys, so a Butler and its linked NPC would
// double-propose the same fact invisibly to each other. Here the subject name is
// resolved to the node id via the campaign's Node list, so both paths collapse onto
// "id:<node>" whenever the subject names a real Node; only an unresolvable name
// keeps a "subj:" key. Established body facts skip gm_private Nodes — a GM secret
// must never surface into a prompt via the echo (ADR-0008). No session ⇒
// ErrNoActiveSession; the comparison itself is the handler's.
func (a *Adapter) ExistingKnowledge(ctx context.Context, _ string, w tool.ProposedWrite) (tool.KnownForTarget, error) {
	campaignID, err := a.activeCampaign()
	if err != nil {
		return tool.KnownForTarget{}, err
	}

	nodes, err := a.store.ListNodes(ctx, campaignID)
	if err != nil {
		return tool.KnownForTarget{}, fmt.Errorf("knowledge: existing knowledge: list nodes: %w", err)
	}
	nameToID := make(map[string]string, len(nodes))
	idToNode := make(map[string]storage.KGNode, len(nodes))
	for _, n := range nodes {
		idToNode[n.ID.String()] = n
		if k := textnorm.Normalize(n.Name); k != "" {
			nameToID[k] = n.ID.String()
		}
	}

	wantKey := canonicalTargetKey(w, nameToID)
	if wantKey == "" {
		return tool.KnownForTarget{}, nil // no identifiable target ⇒ nothing to compare
	}

	var known tool.KnownForTarget
	if id, ok := strings.CutPrefix(wantKey, "id:"); ok {
		if n, ok := idToNode[id]; ok && !n.GMPrivate {
			known.Established = bodyLines(n.Body)
		}
	}

	pending, err := a.store.ListPendingKnowledgeProposals(ctx, campaignID)
	if err != nil {
		return tool.KnownForTarget{}, fmt.Errorf("knowledge: existing knowledge: %w", err)
	}
	for _, p := range pending {
		var pw tool.ProposedWrite
		if err := json.Unmarshal(p.ProposedWrite, &pw); err != nil {
			continue // a malformed legacy row is not a comparable duplicate
		}
		if canonicalTargetKey(pw, nameToID) == wantKey {
			if s := tool.ProposalSalient(pw); s != "" {
				known.Pending = append(known.Pending, s)
			}
		}
	}
	return known, nil
}

// canonicalTargetKey is [tool.ProposalTargetKey] with subject-name → node-id
// resolution layered on: a subject that names a real Node collapses onto the
// node-id key so own_node and campaign proposals about the same entity unify. An
// unresolvable name keeps the pure "subj:" key; an empty target yields "".
func canonicalTargetKey(w tool.ProposedWrite, nameToID map[string]string) string {
	if w.NodeID != "" {
		return "id:" + w.NodeID
	}
	name := w.Subject
	if name == "" && w.Kind == "node" {
		name = w.Name
	}
	n := textnorm.Normalize(name)
	if n == "" {
		return ""
	}
	if id, ok := nameToID[n]; ok {
		return "id:" + id
	}
	return "subj:" + n
}

// bodyLines splits a Node's body prose into its individual established facts —
// one per non-empty, trimmed line — the granularity the GM-approved writes append
// at, so a re-proposal of a single line is caught.
func bodyLines(body string) []string {
	var out []string
	for _, ln := range strings.Split(body, "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// Compile-time assertions that the adapter satisfies the tool seams.
var (
	_ tool.TranscriptSearcher = (*Adapter)(nil)
	_ tool.KGReader           = (*Adapter)(nil)
	_ tool.KGWriter           = (*Adapter)(nil)
)
