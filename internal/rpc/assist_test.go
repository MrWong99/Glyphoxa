package rpc_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/assist"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeAssistStore fakes the campaign-assist store slice (#479): agents backs
// the persona draft's pre-read; applied* record the ApplyKnowledgeDraft call.
type fakeAssistStore struct {
	*fakeActive

	agents map[uuid.UUID]storage.Agent

	appliedCampaign uuid.UUID
	appliedNodes    []storage.NewKGNode
	appliedEdges    []storage.KnowledgeDraftEdge
	applyErr        error
}

func newFakeAssistStore() *fakeAssistStore {
	return &fakeAssistStore{fakeActive: newFakeActive(), agents: map[uuid.UUID]storage.Agent{}}
}

func (f *fakeAssistStore) GetAgent(_ context.Context, id uuid.UUID) (storage.Agent, error) {
	a, ok := f.agents[id]
	if !ok {
		return storage.Agent{}, storage.ErrNotFound
	}
	return a, nil
}

func (f *fakeAssistStore) ApplyKnowledgeDraft(_ context.Context, campaignID uuid.UUID, nodes []storage.NewKGNode, edges []storage.KnowledgeDraftEdge) ([]storage.KGNode, []storage.KGEdge, error) {
	f.appliedCampaign = campaignID
	f.appliedNodes = nodes
	f.appliedEdges = edges
	if f.applyErr != nil {
		return nil, nil, f.applyErr
	}
	created := make([]storage.KGNode, len(nodes))
	for i, n := range nodes {
		created[i] = storage.KGNode{ID: uuid.New(), CampaignID: campaignID, Type: n.Type, Name: n.Name, Body: n.Body, GMPrivate: n.GMPrivate}
	}
	createdEdges := make([]storage.KGEdge, len(edges))
	for i := range edges {
		createdEdges[i] = storage.KGEdge{ID: uuid.New(), CampaignID: campaignID}
	}
	return created, createdEdges, nil
}

// fakeAssistEngine scripts the drafting surface: canned persona/draft or error,
// recording what the handlers passed in.
type fakeAssistEngine struct {
	persona    string
	personaErr error
	draft      assist.Draft
	draftErr   error

	gotCampaign storage.Campaign
	gotPersona  assist.PersonaInput
	gotPrompt   string
}

func (f *fakeAssistEngine) GeneratePersona(_ context.Context, c storage.Campaign, in assist.PersonaInput) (string, error) {
	f.gotCampaign, f.gotPersona = c, in
	return f.persona, f.personaErr
}

func (f *fakeAssistEngine) GenerateKnowledge(_ context.Context, c storage.Campaign, prompt string) (assist.Draft, error) {
	f.gotCampaign, f.gotPrompt = c, prompt
	return f.draft, f.draftErr
}

// assistClient stands up the CampaignService handler over the assist fake with
// an (optionally nil) engine.
func assistClient(t *testing.T, store *fakeAssistStore, engine rpc.AssistEngine) managementv1connect.CampaignServiceClient {
	t.Helper()
	srv := rpc.NewCampaignServerWith(rpc.CampaignStores{Active: store, Assist: store})
	if engine != nil {
		srv.SetAssist(engine)
	}
	return campaignClientServe(t, srv)
}

func TestGeneratePersona_HappyPath(t *testing.T) {
	t.Parallel()
	store := newFakeAssistStore()
	campID := uuid.New()
	store.campaign = storage.Campaign{ID: campID, Name: "Lost Mine", Language: "de"}
	agentID := uuid.New()
	store.agents[agentID] = storage.Agent{ID: agentID, CampaignID: campID, Role: storage.AgentRoleCharacter, Name: "Bart", Title: "Innkeeper"}
	eng := &fakeAssistEngine{persona: "You are Bart."}

	resp, err := assistClient(t, store, eng).GeneratePersona(context.Background(),
		connect.NewRequest(&managementv1.GeneratePersonaRequest{AgentId: agentID.String(), Prompt: "  a gruff innkeeper  "}))
	if err != nil {
		t.Fatalf("GeneratePersona: %v", err)
	}
	if resp.Msg.GetPersona() != "You are Bart." {
		t.Errorf("persona = %q", resp.Msg.GetPersona())
	}
	if eng.gotPersona.Prompt != "a gruff innkeeper" {
		t.Errorf("prompt = %q, want trimmed", eng.gotPersona.Prompt)
	}
	if eng.gotPersona.AgentName != "Bart" || eng.gotPersona.AgentTitle != "Innkeeper" {
		t.Errorf("agent fields = %+v, want Bart/Innkeeper", eng.gotPersona)
	}
	if eng.gotCampaign.ID != campID {
		t.Errorf("campaign = %s, want the active campaign", eng.gotCampaign.ID)
	}
}

func TestGeneratePersona_Validation(t *testing.T) {
	t.Parallel()
	store := newFakeAssistStore()
	campID := uuid.New()
	store.campaign = storage.Campaign{ID: campID}
	agentID := uuid.New()
	store.agents[agentID] = storage.Agent{ID: agentID, CampaignID: campID, Role: storage.AgentRoleCharacter}
	// A cross-campaign agent must read back as NotFound, never leak.
	foreignID := uuid.New()
	store.agents[foreignID] = storage.Agent{ID: foreignID, CampaignID: uuid.New(), Role: storage.AgentRoleCharacter}
	client := assistClient(t, store, &fakeAssistEngine{persona: "p"})

	cases := []struct {
		name    string
		agentID string
		prompt  string
		want    connect.Code
	}{
		{"empty prompt", agentID.String(), "   ", connect.CodeInvalidArgument},
		{"oversized prompt", agentID.String(), strings.Repeat("x", 4001), connect.CodeInvalidArgument},
		{"bad agent id", "not-a-uuid", "ok", connect.CodeInvalidArgument},
		{"unknown agent", uuid.NewString(), "ok", connect.CodeNotFound},
		{"cross-campaign agent", foreignID.String(), "ok", connect.CodeNotFound},
	}
	for _, tc := range cases {
		_, err := client.GeneratePersona(context.Background(),
			connect.NewRequest(&managementv1.GeneratePersonaRequest{AgentId: tc.agentID, Prompt: tc.prompt}))
		if got := connect.CodeOf(err); got != tc.want {
			t.Errorf("%s: code = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestGeneratePersona_EngineErrors(t *testing.T) {
	t.Parallel()
	store := newFakeAssistStore()
	campID := uuid.New()
	store.campaign = storage.Campaign{ID: campID}
	agentID := uuid.New()
	store.agents[agentID] = storage.Agent{ID: agentID, CampaignID: campID, Role: storage.AgentRoleCharacter}
	req := func() *connect.Request[managementv1.GeneratePersonaRequest] {
		return connect.NewRequest(&managementv1.GeneratePersonaRequest{AgentId: agentID.String(), Prompt: "ok"})
	}

	// No engine wired (a composition that doesn't exercise assist) → Unavailable.
	if _, err := assistClient(t, store, nil).GeneratePersona(context.Background(), req()); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("nil engine: code = %v, want Unavailable", connect.CodeOf(err))
	}
	// An unusable model response → Unavailable (retry may succeed).
	unusable := &fakeAssistEngine{personaErr: assist.ErrUnusableResponse}
	if _, err := assistClient(t, store, unusable).GeneratePersona(context.Background(), req()); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("unusable: code = %v, want Unavailable", connect.CodeOf(err))
	}
	// A refused platform-key entitlement → FailedPrecondition (save a key).
	refused := &fakeAssistEngine{personaErr: llmbuild.ErrNoPlatformKeyEntitlement}
	if _, err := assistClient(t, store, refused).GeneratePersona(context.Background(), req()); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("no entitlement: code = %v, want FailedPrecondition", connect.CodeOf(err))
	}
	// Any other engine failure (provider down, bad key) → Unavailable.
	down := &fakeAssistEngine{personaErr: errors.New("connection refused")}
	if _, err := assistClient(t, store, down).GeneratePersona(context.Background(), req()); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("provider down: code = %v, want Unavailable", connect.CodeOf(err))
	}
}

func TestGenerateKnowledge_HappyPath(t *testing.T) {
	t.Parallel()
	store := newFakeAssistStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	eng := &fakeAssistEngine{draft: assist.Draft{
		Nodes: []assist.DraftNode{
			{Type: "npc", Name: "Wilhelmine", Body: "A fence.", GMPrivate: false},
			{Type: "faction", Name: "The Grey Hands", Body: "Thieves.", GMPrivate: true},
		},
		Edges: []assist.DraftEdge{{FromIndex: 0, ToIndex: 1, Type: "member_of"}},
	}}

	resp, err := assistClient(t, store, eng).GenerateKnowledge(context.Background(),
		connect.NewRequest(&managementv1.GenerateKnowledgeRequest{Prompt: "the thieves' guild"}))
	if err != nil {
		t.Fatalf("GenerateKnowledge: %v", err)
	}
	nodes, edges := resp.Msg.GetNodes(), resp.Msg.GetEdges()
	if len(nodes) != 2 || len(edges) != 1 {
		t.Fatalf("draft = %d nodes / %d edges, want 2/1", len(nodes), len(edges))
	}
	if nodes[0].GetNodeType() != managementv1.NodeType_NODE_TYPE_NPC || nodes[0].GetName() != "Wilhelmine" {
		t.Errorf("node 0 = %+v", nodes[0])
	}
	if !nodes[1].GetGmPrivate() {
		t.Errorf("gm_private lost on node 1")
	}
	if e := edges[0]; e.GetFromIndex() != 0 || e.GetToIndex() != 1 || e.GetEdgeType() != managementv1.EdgeType_EDGE_TYPE_MEMBER_OF {
		t.Errorf("edge = %+v", e)
	}
	if eng.gotPrompt != "the thieves' guild" {
		t.Errorf("prompt = %q", eng.gotPrompt)
	}
}

func TestGenerateKnowledge_EmptyPromptAndEngineError(t *testing.T) {
	t.Parallel()
	store := newFakeAssistStore()
	store.campaign = storage.Campaign{ID: uuid.New()}

	if _, err := assistClient(t, store, &fakeAssistEngine{}).GenerateKnowledge(context.Background(),
		connect.NewRequest(&managementv1.GenerateKnowledgeRequest{Prompt: " "})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("empty prompt: code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	unusable := &fakeAssistEngine{draftErr: assist.ErrUnusableResponse}
	if _, err := assistClient(t, store, unusable).GenerateKnowledge(context.Background(),
		connect.NewRequest(&managementv1.GenerateKnowledgeRequest{Prompt: "ok"})); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Errorf("unusable: code = %v, want Unavailable", connect.CodeOf(err))
	}
}

func TestApplyGeneratedKnowledge_HappyPath(t *testing.T) {
	t.Parallel()
	store := newFakeAssistStore()
	campID := uuid.New()
	store.campaign = storage.Campaign{ID: campID}

	resp, err := assistClient(t, store, nil).ApplyGeneratedKnowledge(context.Background(),
		connect.NewRequest(&managementv1.ApplyGeneratedKnowledgeRequest{
			Nodes: []*managementv1.DraftNode{
				{NodeType: managementv1.NodeType_NODE_TYPE_NPC, Name: "  Wilhelmine  ", Body: "A fence."},
				{NodeType: managementv1.NodeType_NODE_TYPE_FACTION, Name: "The Grey Hands", GmPrivate: true},
			},
			Edges: []*managementv1.DraftEdge{
				{FromIndex: 0, ToIndex: 1, EdgeType: managementv1.EdgeType_EDGE_TYPE_MEMBER_OF},
			},
		}))
	if err != nil {
		t.Fatalf("ApplyGeneratedKnowledge: %v", err)
	}
	if store.appliedCampaign != campID {
		t.Errorf("applied campaign = %s, want %s", store.appliedCampaign, campID)
	}
	if len(store.appliedNodes) != 2 || store.appliedNodes[0].Name != "Wilhelmine" {
		t.Errorf("applied nodes = %+v, want trimmed Wilhelmine first", store.appliedNodes)
	}
	if len(store.appliedEdges) != 1 || store.appliedEdges[0].Type != storage.KGEdgeMemberOf {
		t.Errorf("applied edges = %+v", store.appliedEdges)
	}
	if len(resp.Msg.GetNodes()) != 2 || resp.Msg.GetEdgesCreated() != 1 {
		t.Errorf("response = %d nodes / %d edges", len(resp.Msg.GetNodes()), resp.Msg.GetEdgesCreated())
	}
}

func TestApplyGeneratedKnowledge_Validation(t *testing.T) {
	t.Parallel()
	store := newFakeAssistStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	client := assistClient(t, store, nil)

	ok := func() []*managementv1.DraftNode {
		return []*managementv1.DraftNode{
			{NodeType: managementv1.NodeType_NODE_TYPE_NPC, Name: "A"},
			{NodeType: managementv1.NodeType_NODE_TYPE_LOCATION, Name: "B"},
		}
	}
	cases := []struct {
		name string
		req  *managementv1.ApplyGeneratedKnowledgeRequest
	}{
		{"empty draft", &managementv1.ApplyGeneratedKnowledgeRequest{}},
		{"unspecified node type", &managementv1.ApplyGeneratedKnowledgeRequest{
			Nodes: []*managementv1.DraftNode{{Name: "A"}},
		}},
		{"blank name", &managementv1.ApplyGeneratedKnowledgeRequest{
			Nodes: []*managementv1.DraftNode{{NodeType: managementv1.NodeType_NODE_TYPE_NPC, Name: "  "}},
		}},
		{"unspecified edge type", &managementv1.ApplyGeneratedKnowledgeRequest{
			Nodes: ok(),
			Edges: []*managementv1.DraftEdge{{FromIndex: 0, ToIndex: 1}},
		}},
		{"edge index out of range", &managementv1.ApplyGeneratedKnowledgeRequest{
			Nodes: ok(),
			Edges: []*managementv1.DraftEdge{{FromIndex: 0, ToIndex: 9, EdgeType: managementv1.EdgeType_EDGE_TYPE_KNOWS}},
		}},
		{"self edge", &managementv1.ApplyGeneratedKnowledgeRequest{
			Nodes: ok(),
			Edges: []*managementv1.DraftEdge{{FromIndex: 1, ToIndex: 1, EdgeType: managementv1.EdgeType_EDGE_TYPE_KNOWS}},
		}},
	}
	for _, tc := range cases {
		_, err := client.ApplyGeneratedKnowledge(context.Background(), connect.NewRequest(tc.req))
		if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
			t.Errorf("%s: code = %v, want InvalidArgument", tc.name, got)
		}
	}
}

func TestApplyGeneratedKnowledge_StoreErrors(t *testing.T) {
	t.Parallel()
	req := func() *connect.Request[managementv1.ApplyGeneratedKnowledgeRequest] {
		return connect.NewRequest(&managementv1.ApplyGeneratedKnowledgeRequest{
			Nodes: []*managementv1.DraftNode{{NodeType: managementv1.NodeType_NODE_TYPE_NPC, Name: "A"}},
		})
	}
	for name, tc := range map[string]struct {
		err  error
		want connect.Code
	}{
		"matrix-invalid edge": {storage.ErrInvalidEdge, connect.CodeFailedPrecondition},
		"duplicate edge":      {storage.ErrConflict, connect.CodeFailedPrecondition},
		"other failure":       {errors.New("boom"), connect.CodeInternal},
	} {
		store := newFakeAssistStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		store.applyErr = tc.err
		_, err := assistClient(t, store, nil).ApplyGeneratedKnowledge(context.Background(), req())
		if got := connect.CodeOf(err); got != tc.want {
			t.Errorf("%s: code = %v, want %v", name, got, tc.want)
		}
	}
}
