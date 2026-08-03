package rpc

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/kgfacts"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// kgPreview is the agent-knowledge lens (#535): a read-only view of exactly what
// a Character NPC's Hot Context facts block will contain, and what the caps cut.
//
// Today the only way to know why an NPC didn't mention something is to reason
// about AgentNodeFacts and renderFacts from memory. This makes an invisible
// system visible — including the deterministic prefix-stop, which silently drops
// the remainder of the facts block once MaxBlockChars is crossed.
type kgPreview struct {
	store  kgPreviewStore
	active *activeCampaignSource
}

// kgPreviewStore is the narrow read surface the lens needs. It is the PROMPT-FACING
// seam deliberately — the same [storage.PromptKGView] the voice loop holds — so
// the preview reads what the turn reads, gm_private filtering included. A GM-facing
// read here would show facts the NPC never receives, which is worse than showing
// nothing.
//
// AgentLinkedNode distinguishes "no linked entry" from "linked but nothing to
// say": AgentNodeFacts is keyed by Agent, so an unlinked one legitimately returns
// an empty set and there is no campaign-wide fallback.
type kgPreviewStore interface {
	AgentNodeFacts(ctx context.Context, agentID uuid.UUID) ([]storage.KGNode, error)
	AgentLinkedNode(ctx context.Context, agentID uuid.UUID) (storage.KGNode, bool, error)
	GetAgent(ctx context.Context, id uuid.UUID) (storage.Agent, error)
}

// GetAgentFactPreview renders the Agent's facts block through the SAME path the
// voice loop uses (#535): [storage.PromptKGView.AgentNodeFacts] plus
// [kgfacts.RenderPreview]. Reimplementing the budget arithmetic client-side would
// let the preview drift from what is actually injected, which is the one failure
// this feature cannot afford.
//
// The Agent is verified to belong to the active Campaign before anything is read,
// so a crafted agent_id cannot preview another Campaign's world (#342).
//
// An unparsable id is CodeInvalidArgument; an Agent outside the active Campaign is
// CodeNotFound; a storage failure is CodeInternal.
func (s *kgPreview) GetAgentFactPreview(
	ctx context.Context,
	req *connect.Request[managementv1.GetAgentFactPreviewRequest],
) (*connect.Response[managementv1.GetAgentFactPreviewResponse], error) {
	agentID, err := uuid.Parse(req.Msg.GetAgentId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid agent id"))
	}

	c, err := s.active.resolve(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("GetAgentFactPreview: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	// Campaign scoping FIRST: AgentNodeFacts is keyed by Agent ALONE, so without this
	// check an id from another Campaign would happily render that Campaign's world
	// into this operator's screen (#342).
	agent, err := s.store.GetAgent(ctx, agentID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("agent not found"))
		}
		slog.Default().Error("GetAgentFactPreview: get agent failed", "agent_id", agentID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if agent.CampaignID != c.ID {
		// Indistinguishable from "no such agent" on purpose: another Campaign's roster
		// is not something this session may probe for.
		return nil, connect.NewError(connect.CodeNotFound, errors.New("agent not found"))
	}

	linkedNode, linked, err := s.store.AgentLinkedNode(ctx, agentID)
	if err != nil {
		slog.Default().Error("GetAgentFactPreview: linked node lookup failed", "agent_id", agentID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	out := &managementv1.GetAgentFactPreviewResponse{
		MaxChars:      kgfacts.MaxBlockChars,
		MaxFacts:      kgfacts.MaxFacts,
		MaxNeighbours: storage.MaxAgentFactNodes,
		Linked:        linked,
	}
	if !linked {
		// An explicit "not linked" state, not an empty graph. The GM's fix is to link
		// an entry, and saying so beats an empty panel they have to interpret.
		return connect.NewResponse(out), nil
	}
	out.LinkedNodeId = linkedNode.ID.String()

	nodes, err := s.store.AgentNodeFacts(ctx, agentID)
	if err != nil {
		slog.Default().Error("GetAgentFactPreview: agent node facts failed", "agent_id", agentID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	// The SQL read has its OWN row cap, applied before the renderer ever sees a
	// Node. A hub NPC past it would otherwise show its extra neighbours as merely
	// "not adjacent" — the GM would go looking for a missing Edge that is not
	// missing. Reported separately from the renderer's truncation because the two
	// have different fixes.
	out.NeighbourhoodClipped = len(nodes) >= storage.MaxAgentFactNodes

	p := kgfacts.RenderPreview(nodes)
	out.Facts = p.Facts
	out.Chars = int32(p.Chars) //nolint:gosec // bounded by kgfacts.MaxBlockChars
	out.Truncated = p.Truncated
	for _, id := range p.IncludedIDs {
		out.IncludedNodeIds = append(out.IncludedNodeIds, id.String())
	}
	for _, id := range p.DroppedIDs {
		out.DroppedNodeIds = append(out.DroppedNodeIds, id.String())
	}
	return connect.NewResponse(out), nil
}

// kgPreviewSource composes the ONE prompt-facing KG read with the two plain Agent
// reads the lens needs for scoping and the linked-entry state. The facts read is
// taken from [storage.PromptKGView] rather than *Store on purpose: the lens must
// show what the TURN shows, gm_private filtering included, and a GM-facing read
// here would confidently display facts the NPC never receives.
type kgPreviewSource struct {
	prompt storage.PromptKGView
	store  *storage.Store
}

func (v kgPreviewSource) AgentNodeFacts(ctx context.Context, agentID uuid.UUID) ([]storage.KGNode, error) {
	return v.prompt.AgentNodeFacts(ctx, agentID)
}

func (v kgPreviewSource) AgentLinkedNode(ctx context.Context, agentID uuid.UUID) (storage.KGNode, bool, error) {
	return v.store.AgentLinkedNode(ctx, agentID)
}

func (v kgPreviewSource) GetAgent(ctx context.Context, id uuid.UUID) (storage.Agent, error) {
	return v.store.GetAgent(ctx, id)
}

// kgPreviewStoreOf builds the lens's read source over a concrete Store.
func kgPreviewStoreOf(s *storage.Store) kgPreviewStore {
	return kgPreviewSource{prompt: s.PromptKG(), store: s}
}
