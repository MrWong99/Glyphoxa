package rpc_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings"
)

// --- fakeCampaignStore proposal-review methods (#300) ---

func (f *fakeCampaignStore) ListPendingKnowledgeProposals(_ context.Context, campaignID uuid.UUID) ([]storage.KnowledgeProposal, error) {
	f.listProposalsCampaign = campaignID
	return f.pendingProposals, f.listProposalsErr
}

func (f *fakeCampaignStore) GetPendingKnowledgeProposal(_ context.Context, campaignID, id uuid.UUID) (storage.KnowledgeProposal, error) {
	f.getProposalCampaign = campaignID
	if f.getProposalErr != nil {
		return storage.KnowledgeProposal{}, f.getProposalErr
	}
	return f.getProposal, nil
}

func (f *fakeCampaignStore) ApproveKnowledgeProposal(_ context.Context, campaignID, id uuid.UUID) error {
	f.approveCampaign = campaignID
	f.approveCalls = append(f.approveCalls, id)
	return f.approveErr
}

func (f *fakeCampaignStore) RejectKnowledgeProposal(_ context.Context, campaignID, id uuid.UUID) error {
	f.rejectCampaign = campaignID
	f.rejectCalls = append(f.rejectCalls, id)
	return f.rejectErr
}

func (f *fakeCampaignStore) SimilarNodes(_ context.Context, campaignID uuid.UUID, query []float32, k int) ([]storage.KGNode, error) {
	f.similarCalls++
	f.similarQueryVec = query
	if f.similarErr != nil {
		return nil, f.similarErr
	}
	return f.similarResults, nil
}

// fakeEmbedder records its input texts and returns a fixed vector (or an error).
type fakeEmbedder struct {
	texts []string
	vec   []float32
	err   error
}

func (e *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.texts = append(e.texts, texts...)
	if e.err != nil {
		return nil, e.err
	}
	return [][]float32{e.vec}, nil
}

// proposalRaw marshals a ProposedWrite into a proposal row's jsonb.
func proposalRaw(t *testing.T, w tool.ProposedWrite) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// TestListKnowledgeProposals_MapsThreeKinds asserts the jsonb→oneof mapping for
// fact/edge/node and that an unparseable row is still listed with an unset write.
func TestListKnowledgeProposals_MapsThreeKinds(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campaign = storage.Campaign{ID: uuid.New(), Name: "Lost Mine"}
	store.pendingProposals = []storage.KnowledgeProposal{
		{ID: uuid.New(), AuthoringAgentID: uuid.New(), AuthoringAgentName: "Bart", CreatedAt: time.Now(),
			ProposedWrite: proposalRaw(t, tool.ProposedWrite{V: 1, Kind: "fact", Subject: "Bart", Fact: "Fears dark"})},
		{ID: uuid.New(), AuthoringAgentID: uuid.New(), AuthoringAgentName: "Glyphoxa", CreatedAt: time.Now(),
			ProposedWrite: proposalRaw(t, tool.ProposedWrite{V: 1, Kind: "edge", Subject: "Bart", Relation: "resides_in", Target: "Inn"})},
		{ID: uuid.New(), AuthoringAgentID: uuid.New(), AuthoringAgentName: "Glyphoxa", CreatedAt: time.Now(),
			ProposedWrite: proposalRaw(t, tool.ProposedWrite{V: 1, Kind: "node", NodeType: "faction", Name: "Zhent", Body: "shadow"})},
		{ID: uuid.New(), AuthoringAgentID: uuid.New(), AuthoringAgentName: "Glyphoxa", CreatedAt: time.Now(),
			ProposedWrite: json.RawMessage(`{"garbage":true}`)},
	}
	client := crudClient(t, store)

	resp, err := client.ListKnowledgeProposals(context.Background(),
		connect.NewRequest(&managementv1.ListKnowledgeProposalsRequest{}))
	if err != nil {
		t.Fatalf("ListKnowledgeProposals: %v", err)
	}
	ps := resp.Msg.GetProposals()
	if len(ps) != 4 {
		t.Fatalf("got %d proposals, want 4 (incl. the unparseable one)", len(ps))
	}
	if ps[0].GetFact() == nil || ps[0].GetFact().GetFact() != "Fears dark" || ps[0].GetAuthoringAgentName() != "Bart" {
		t.Errorf("fact mapping wrong: %+v", ps[0])
	}
	if e := ps[1].GetEdge(); e == nil || e.GetRelation() != managementv1.EdgeType_EDGE_TYPE_RESIDES_IN || e.GetTarget() != "Inn" {
		t.Errorf("edge mapping wrong: %+v", ps[1])
	}
	if n := ps[2].GetNode(); n == nil || n.GetNodeType() != managementv1.NodeType_NODE_TYPE_FACTION || n.GetName() != "Zhent" {
		t.Errorf("node mapping wrong: %+v", ps[2])
	}
	// Unparseable: write oneof unset, but still present.
	if ps[3].GetFact() != nil || ps[3].GetEdge() != nil || ps[3].GetNode() != nil {
		t.Errorf("unparseable row should have unset write oneof: %+v", ps[3])
	}
	if store.listProposalsCampaign != store.campaign.ID {
		t.Errorf("list scoped to %s, want active %s", store.listProposalsCampaign, store.campaign.ID)
	}
}

func TestListKnowledgeProposals_NoCampaignIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campErr = storage.ErrNotFound
	client := crudClient(t, store)
	_, err := client.ListKnowledgeProposals(context.Background(),
		connect.NewRequest(&managementv1.ListKnowledgeProposalsRequest{}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

// TestApproveKnowledgeProposal_ErrorMapping covers the whole error surface.
func TestApproveKnowledgeProposal_ErrorMapping(t *testing.T) {
	t.Parallel()

	t.Run("bad uuid is InvalidArgument", func(t *testing.T) {
		store := newFakeStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		client := crudClient(t, store)
		_, err := client.ApproveKnowledgeProposal(context.Background(),
			connect.NewRequest(&managementv1.ApproveKnowledgeProposalRequest{Id: "not-a-uuid"}))
		if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
			t.Errorf("code = %v, want InvalidArgument", got)
		}
	})

	t.Run("happy path scopes to active campaign", func(t *testing.T) {
		store := newFakeStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		client := crudClient(t, store)
		id := uuid.New()
		if _, err := client.ApproveKnowledgeProposal(context.Background(),
			connect.NewRequest(&managementv1.ApproveKnowledgeProposalRequest{Id: id.String()})); err != nil {
			t.Fatalf("approve: %v", err)
		}
		if store.approveCampaign != store.campaign.ID || len(store.approveCalls) != 1 || store.approveCalls[0] != id {
			t.Errorf("approve not scoped/forwarded: campaign=%s calls=%v", store.approveCampaign, store.approveCalls)
		}
	})

	t.Run("not found is NotFound", func(t *testing.T) {
		store := newFakeStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		store.approveErr = storage.ErrNotFound
		client := crudClient(t, store)
		_, err := client.ApproveKnowledgeProposal(context.Background(),
			connect.NewRequest(&managementv1.ApproveKnowledgeProposalRequest{Id: uuid.New().String()}))
		if got := connect.CodeOf(err); got != connect.CodeNotFound {
			t.Errorf("code = %v, want NotFound", got)
		}
	})

	t.Run("blocked is FailedPrecondition with verbatim reason", func(t *testing.T) {
		store := newFakeStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		store.approveErr = &storage.ProposalBlockedError{Reason: `no wiki entry named "Waterdeep" — create it first, then approve; or reject`}
		client := crudClient(t, store)
		_, err := client.ApproveKnowledgeProposal(context.Background(),
			connect.NewRequest(&managementv1.ApproveKnowledgeProposalRequest{Id: uuid.New().String()}))
		if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
			t.Fatalf("code = %v, want FailedPrecondition", got)
		}
		if !strings.Contains(err.Error(), `no wiki entry named "Waterdeep" — create it first, then approve; or reject`) {
			t.Errorf("error = %q, want the verbatim storage reason", err.Error())
		}
	})
}

func TestRejectKnowledgeProposal_ErrorMapping(t *testing.T) {
	t.Parallel()

	t.Run("bad uuid is InvalidArgument", func(t *testing.T) {
		store := newFakeStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		client := crudClient(t, store)
		_, err := client.RejectKnowledgeProposal(context.Background(),
			connect.NewRequest(&managementv1.RejectKnowledgeProposalRequest{Id: "x"}))
		if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
			t.Errorf("code = %v, want InvalidArgument", got)
		}
	})

	t.Run("not found is NotFound", func(t *testing.T) {
		store := newFakeStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		store.rejectErr = storage.ErrNotFound
		client := crudClient(t, store)
		_, err := client.RejectKnowledgeProposal(context.Background(),
			connect.NewRequest(&managementv1.RejectKnowledgeProposalRequest{Id: uuid.New().String()}))
		if got := connect.CodeOf(err); got != connect.CodeNotFound {
			t.Errorf("code = %v, want NotFound", got)
		}
	})

	t.Run("happy path forwards id", func(t *testing.T) {
		store := newFakeStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		client := crudClient(t, store)
		id := uuid.New()
		if _, err := client.RejectKnowledgeProposal(context.Background(),
			connect.NewRequest(&managementv1.RejectKnowledgeProposalRequest{Id: id.String()})); err != nil {
			t.Fatalf("reject: %v", err)
		}
		if len(store.rejectCalls) != 1 || store.rejectCalls[0] != id {
			t.Errorf("reject not forwarded: %v", store.rejectCalls)
		}
	})
}

// TestListSimilarKnowledge_EmbedderPath: with an embedder wired, the subject text
// is embedded and SimilarNodes is queried with the resulting vector.
func TestListSimilarKnowledge_EmbedderPath(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	store.getProposal = storage.KnowledgeProposal{
		ID: uuid.New(), ProposedWrite: proposalRaw(t, tool.ProposedWrite{V: 1, Kind: "fact", Subject: "Bart", Fact: "Fears dark"}),
	}
	store.similarResults = []storage.KGNode{{ID: uuid.New(), Type: storage.KGNodeNPC, Name: "Bart"}}
	emb := &fakeEmbedder{vec: make([]float32, embeddings.Dim)} // correct dimension → vector path
	client := crudClientWithEmbedder(t, store, emb)

	resp, err := client.ListSimilarKnowledge(context.Background(),
		connect.NewRequest(&managementv1.ListSimilarKnowledgeRequest{ProposalId: uuid.New().String()}))
	if err != nil {
		t.Fatalf("ListSimilarKnowledge: %v", err)
	}
	if len(resp.Msg.GetNodes()) != 1 || resp.Msg.GetNodes()[0].GetName() != "Bart" {
		t.Errorf("nodes = %+v, want [Bart]", resp.Msg.GetNodes())
	}
	if store.similarCalls != 1 {
		t.Errorf("SimilarNodes called %d times, want 1 (vector path)", store.similarCalls)
	}
	if len(emb.texts) != 1 || emb.texts[0] != "Bart: Fears dark" {
		t.Errorf("embedded texts = %v, want [\"Bart: Fears dark\"]", emb.texts)
	}
}

// TestListSimilarKnowledge_WrongDimFallsBackToFTS: an embedder returning a
// wrong-dimension vector degrades to fulltext rather than failing with CodeInternal
// on the ::vector cast.
func TestListSimilarKnowledge_WrongDimFallsBackToFTS(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	store.getProposal = storage.KnowledgeProposal{
		ID: uuid.New(), ProposedWrite: proposalRaw(t, tool.ProposedWrite{V: 1, Kind: "fact", Subject: "Bart", Fact: "Fears dark"}),
	}
	emb := &fakeEmbedder{vec: []float32{0.1, 0.2, 0.3}} // 3 dims, not embeddings.Dim
	client := crudClientWithEmbedder(t, store, emb)

	if _, err := client.ListSimilarKnowledge(context.Background(),
		connect.NewRequest(&managementv1.ListSimilarKnowledgeRequest{ProposalId: uuid.New().String()})); err != nil {
		t.Fatalf("ListSimilarKnowledge: %v", err)
	}
	if store.similarCalls != 0 {
		t.Errorf("vector path ran with a wrong-dim vector: %d", store.similarCalls)
	}
	if store.searchCalls != 1 {
		t.Errorf("fts fallback not taken: searchCalls=%d, want 1", store.searchCalls)
	}
}

// TestListSimilarKnowledge_NilEmbedderFallsBackToFTS: with no embedder, the hint
// runs SearchNodes with the subject text.
func TestListSimilarKnowledge_NilEmbedderFallsBackToFTS(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	store.getProposal = storage.KnowledgeProposal{
		ID: uuid.New(), ProposedWrite: proposalRaw(t, tool.ProposedWrite{V: 1, Kind: "node", Name: "Zhent", Body: "shadow"}),
	}
	store.searchResults = []storage.KGNode{{ID: uuid.New(), Type: storage.KGNodeFaction, Name: "Zhentarim"}}
	client := crudClient(t, store) // no embedder

	resp, err := client.ListSimilarKnowledge(context.Background(),
		connect.NewRequest(&managementv1.ListSimilarKnowledgeRequest{ProposalId: uuid.New().String()}))
	if err != nil {
		t.Fatalf("ListSimilarKnowledge: %v", err)
	}
	if store.similarCalls != 0 {
		t.Errorf("vector SimilarNodes called %d times, want 0 (no embedder)", store.similarCalls)
	}
	if store.searchCalls != 1 || store.searchQuery != "Zhent\n\nshadow" {
		t.Errorf("fts search: calls=%d query=%q, want 1 / %q", store.searchCalls, store.searchQuery, "Zhent\n\nshadow")
	}
	if len(resp.Msg.GetNodes()) != 1 {
		t.Errorf("nodes = %+v, want 1", resp.Msg.GetNodes())
	}
}

// TestListSimilarKnowledge_EmbedErrorFallsBackToFTS: an embed error degrades
// silently to fulltext search rather than failing the review.
func TestListSimilarKnowledge_EmbedErrorFallsBackToFTS(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	store.getProposal = storage.KnowledgeProposal{
		ID: uuid.New(), ProposedWrite: proposalRaw(t, tool.ProposedWrite{V: 1, Kind: "edge", Subject: "Bart", Relation: "knows", Target: "Gundren"}),
	}
	emb := &fakeEmbedder{err: errors.New("provider down")}
	client := crudClientWithEmbedder(t, store, emb)

	if _, err := client.ListSimilarKnowledge(context.Background(),
		connect.NewRequest(&managementv1.ListSimilarKnowledgeRequest{ProposalId: uuid.New().String()})); err != nil {
		t.Fatalf("ListSimilarKnowledge: %v", err)
	}
	if store.similarCalls != 0 {
		t.Errorf("vector path ran despite embed error: %d", store.similarCalls)
	}
	if store.searchCalls != 1 || store.searchQuery != "Bart knows Gundren" {
		t.Errorf("fts fallback: calls=%d query=%q", store.searchCalls, store.searchQuery)
	}
}

// TestListSimilarKnowledge_MissingProposalIsNotFound: an already-reviewed/gone
// proposal is CodeNotFound.
func TestListSimilarKnowledge_MissingProposalIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	store.getProposalErr = storage.ErrNotFound
	client := crudClient(t, store)
	_, err := client.ListSimilarKnowledge(context.Background(),
		connect.NewRequest(&managementv1.ListSimilarKnowledgeRequest{ProposalId: uuid.New().String()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}
