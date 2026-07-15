package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/kgvocab"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings"
)

// knowledgeProposals is the Knowledge Proposal review feature module (#300,
// ADR-0052): the GM review surface for the pending queue an Agent's
// remember_knowledge call files into. List renders each proposal (parsing the
// jsonb write into a oneof), approve lands the write atomically, reject drops it,
// and the similarity hint surfaces existing Nodes near a proposal's subject so
// the GM merges rather than duplicates (similarity is a HINT — no auto-merge,
// ADR-0052). The handlers resolve the single operator's active campaign
// server-side (ADR-0039), like the KG Node handlers.
type knowledgeProposals struct {
	store  knowledgeProposalStore
	active *activeCampaignSource
	// embedder embeds a proposal's subject text for the ListSimilarKnowledge vector
	// hint (#300, ADR-0011/0052). Nil (no embeddings provider wired, or a keyless
	// deployment) makes the hint degrade silently to fulltext SearchNodes. Set once
	// at boot before serving, so no lock is needed.
	embedder Embedder
}

// knowledgeProposalStore is the narrow review-queue surface the module needs
// (#300, ADR-0052); *storage.Store satisfies it: the pending list, the
// single-proposal read the similarity hint keys off, the atomic approve/reject
// writes, and the two similarity searches the hint runs.
type knowledgeProposalStore interface {
	ListPendingKnowledgeProposals(ctx context.Context, campaignID uuid.UUID) ([]storage.KnowledgeProposal, error)
	GetPendingKnowledgeProposal(ctx context.Context, campaignID, id uuid.UUID) (storage.KnowledgeProposal, error)
	ApproveKnowledgeProposal(ctx context.Context, campaignID, id uuid.UUID) error
	RejectKnowledgeProposal(ctx context.Context, campaignID, id uuid.UUID) error
	// SimilarNodes is the embedding-vector nearest-neighbour search; SearchNodes is
	// the fulltext fallback the hint degrades to without an embedder (ADR-0011).
	SimilarNodes(ctx context.Context, campaignID uuid.UUID, query []float32, k int) ([]storage.KGNode, error)
	SearchNodes(ctx context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.KGNode, error)
}

// Embedder embeds short texts to query vectors for the Knowledge Proposal
// similarity hint (#300, ADR-0052). The resolved embeddings provider satisfies it;
// nil disables the vector path (the hint falls back to fulltext search).
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// similarEmbedTimeout bounds the ListSimilarKnowledge embedding call — the hint is
// nice-to-have, so a slow provider must not hang the review surface; on timeout it
// degrades to fulltext search.
const similarEmbedTimeout = 15 * time.Second

// similarNodesLimit caps the similarity hint at 5 nearest entries (ADR-0052).
const similarNodesLimit = 5

// ListKnowledgeProposals returns the active campaign's pending proposals
// oldest-first, each with its authoring Agent's name and its parsed write. An
// unparseable jsonb row is still listed (write oneof left UNSET) so the GM can
// reject it. No campaign is CodeNotFound; a storage failure is CodeInternal.
func (s *knowledgeProposals) ListKnowledgeProposals(
	ctx context.Context,
	_ *connect.Request[managementv1.ListKnowledgeProposalsRequest],
) (*connect.Response[managementv1.ListKnowledgeProposalsResponse], error) {
	c, err := s.active.resolve(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("ListKnowledgeProposals: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	proposals, err := s.store.ListPendingKnowledgeProposals(ctx, c.ID)
	if err != nil {
		slog.Default().Error("ListKnowledgeProposals: store list failed", "campaign_id", c.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	out := make([]*managementv1.KnowledgeProposal, 0, len(proposals))
	for _, p := range proposals {
		out = append(out, toProtoKnowledgeProposal(p))
	}
	return connect.NewResponse(&managementv1.ListKnowledgeProposalsResponse{Proposals: out}), nil
}

// ApproveKnowledgeProposal lands a pending proposal's write on the KG and marks it
// approved. An unparsable id is CodeInvalidArgument; a missing/already-reviewed id
// is CodeNotFound; a refused write is CodeFailedPrecondition carrying the storage
// reason verbatim so the GM sees exactly what to fix.
func (s *knowledgeProposals) ApproveKnowledgeProposal(
	ctx context.Context,
	req *connect.Request[managementv1.ApproveKnowledgeProposalRequest],
) (*connect.Response[managementv1.ApproveKnowledgeProposalResponse], error) {
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid proposal id"))
	}

	c, err := s.active.resolve(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("ApproveKnowledgeProposal: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	switch err := s.store.ApproveKnowledgeProposal(ctx, c.ID, id); {
	case err == nil:
		return connect.NewResponse(&managementv1.ApproveKnowledgeProposalResponse{}), nil
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("proposal already reviewed or gone"))
	default:
		var blocked *storage.ProposalBlockedError
		if errors.As(err, &blocked) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(blocked.Reason))
		}
		slog.Default().Error("ApproveKnowledgeProposal: store approve failed", "proposal_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
}

// RejectKnowledgeProposal drops a pending proposal without touching the KG. An
// unparsable id is CodeInvalidArgument; a missing/already-reviewed id is
// CodeNotFound.
func (s *knowledgeProposals) RejectKnowledgeProposal(
	ctx context.Context,
	req *connect.Request[managementv1.RejectKnowledgeProposalRequest],
) (*connect.Response[managementv1.RejectKnowledgeProposalResponse], error) {
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid proposal id"))
	}

	c, err := s.active.resolve(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("RejectKnowledgeProposal: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	switch err := s.store.RejectKnowledgeProposal(ctx, c.ID, id); {
	case err == nil:
		return connect.NewResponse(&managementv1.RejectKnowledgeProposalResponse{}), nil
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("proposal already reviewed or gone"))
	default:
		slog.Default().Error("RejectKnowledgeProposal: store reject failed", "proposal_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
}

// ListSimilarKnowledge returns up to 5 existing Nodes most similar to a pending
// proposal's subject — the ADR-0011 review hint. An unparsable id is
// CodeInvalidArgument; a missing/already-reviewed proposal is CodeNotFound. When
// an embeddings provider is wired it embeds the subject text and runs the vector
// nearest-neighbour search; with no provider OR any failure (embed error/timeout)
// it degrades SILENTLY to fulltext SearchNodes — the hint is best-effort and must
// never fail the review.
func (s *knowledgeProposals) ListSimilarKnowledge(
	ctx context.Context,
	req *connect.Request[managementv1.ListSimilarKnowledgeRequest],
) (*connect.Response[managementv1.ListSimilarKnowledgeResponse], error) {
	id, err := uuid.Parse(req.Msg.GetProposalId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid proposal id"))
	}

	c, err := s.active.resolve(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error("ListSimilarKnowledge: get active campaign failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	p, err := s.store.GetPendingKnowledgeProposal(ctx, c.ID, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("proposal already reviewed or gone"))
		}
		slog.Default().Error("ListSimilarKnowledge: get proposal failed", "proposal_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	queryText := similarityQueryText(p.ProposedWrite)

	nodes, err := s.similarNodes(ctx, c.ID, queryText)
	if err != nil {
		slog.Default().Error("ListSimilarKnowledge: similarity search failed", "campaign_id", c.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	out := make([]*managementv1.Node, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, toProtoNode(n))
	}
	return connect.NewResponse(&managementv1.ListSimilarKnowledgeResponse{Nodes: out}), nil
}

// similarNodes runs the vector nearest-neighbour search when an embedder is wired
// and the embed succeeds; otherwise it degrades silently to fulltext SearchNodes.
// A whitespace-only query short-circuits to no hits (SearchNodes would reject it).
func (s *knowledgeProposals) similarNodes(ctx context.Context, campaignID uuid.UUID, queryText string) ([]storage.KGNode, error) {
	if strings.TrimSpace(queryText) == "" {
		return nil, nil
	}
	if s.embedder != nil {
		embedCtx, cancel := context.WithTimeout(ctx, similarEmbedTimeout)
		defer cancel()
		vecs, err := s.embedder.Embed(embedCtx, []string{queryText})
		switch {
		case err != nil || len(vecs) != 1:
			slog.Default().Warn("ListSimilarKnowledge: embed failed; degrading to fulltext hint",
				"err", err, "vecs", len(vecs))
		case len(vecs[0]) != embeddings.Dim:
			// A wrong-dimension vector would make the ::vector cast in SimilarNodes fail
			// (CodeInternal). The hint is best-effort — degrade to fulltext instead.
			slog.Default().Warn("ListSimilarKnowledge: embed returned wrong dimension; degrading to fulltext hint",
				"want", embeddings.Dim, "got", len(vecs[0]))
		default:
			return s.store.SimilarNodes(ctx, campaignID, vecs[0], similarNodesLimit)
		}
	}
	// Fulltext fallback: no embedder, or the embed failed.
	return s.store.SearchNodes(ctx, campaignID, queryText, similarNodesLimit)
}

// similarityQueryText derives the search text for a proposal's subject per kind
// (#300): a fact is "subject: fact"; an edge is "subject relation target"; a node
// is "name\n\nbody". An unparseable write yields "" (no hint).
func similarityQueryText(raw json.RawMessage) string {
	var w tool.ProposedWrite
	if err := json.Unmarshal(raw, &w); err != nil {
		return ""
	}
	switch w.Kind {
	case kgvocab.KindFact:
		return w.Subject + ": " + w.Fact
	case kgvocab.KindEdge:
		return w.Subject + " " + w.Relation + " " + w.Target
	case kgvocab.KindNode:
		return w.Name + "\n\n" + w.Body
	default:
		return ""
	}
}

// toProtoKnowledgeProposal maps a storage proposal onto its wire representation,
// parsing the proposed_write jsonb into the write oneof. A parse failure (or an
// unknown kind) leaves the oneof UNSET and logs a warning — the row is STILL listed
// so the GM can reject an unreadable proposal.
func toProtoKnowledgeProposal(p storage.KnowledgeProposal) *managementv1.KnowledgeProposal {
	out := &managementv1.KnowledgeProposal{
		Id:                 p.ID.String(),
		AuthoringAgentId:   p.AuthoringAgentID.String(),
		AuthoringAgentName: p.AuthoringAgentName,
		CreatedAt:          timestamppb.New(p.CreatedAt),
	}

	var w tool.ProposedWrite
	if err := json.Unmarshal(p.ProposedWrite, &w); err != nil || w.V != kgvocab.ProposalWriteVersion {
		slog.Default().Warn("KnowledgeProposal: unparseable proposed_write; listing with unset write",
			"proposal_id", p.ID, "err", err)
		return out // write oneof left unset → UI renders "unreadable — reject"
	}

	switch w.Kind {
	case kgvocab.KindFact:
		out.Write = &managementv1.KnowledgeProposal_Fact{Fact: &managementv1.ProposedFact{
			NodeId:  w.NodeID,
			Subject: w.Subject,
			Fact:    w.Fact,
		}}
	case kgvocab.KindEdge:
		out.Write = &managementv1.KnowledgeProposal_Edge{Edge: &managementv1.ProposedEdge{
			NodeId:   w.NodeID,
			Subject:  w.Subject,
			Relation: toProtoEdgeType(storage.KGEdgeType(w.Relation)),
			Target:   w.Target,
		}}
	case kgvocab.KindNode:
		out.Write = &managementv1.KnowledgeProposal_Node{Node: &managementv1.ProposedNode{
			NodeType: toProtoNodeType(storage.KGNodeType(w.NodeType)),
			Name:     w.Name,
			Body:     w.Body,
		}}
	default:
		slog.Default().Warn("KnowledgeProposal: unknown kind; listing with unset write",
			"proposal_id", p.ID, "kind", w.Kind)
	}
	return out
}
