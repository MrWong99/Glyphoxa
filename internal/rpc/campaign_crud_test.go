package rpc_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	ttseleven "github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
)

// errAny is an opaque storage failure the fake returns to force the Internal path.
var errAny = errors.New("kg fake failure")

// fakeCampaignStore is a small in-memory campaignStore for the CRUD handlers'
// keyless unit tests. Error hooks (campErr/butlerErr/…) force the failure paths;
// the agents map backs GetAgent/Create/Update/Delete so happy paths round-trip
// without a database.
type fakeCampaignStore struct {
	campaign  storage.Campaign
	campErr   error
	butler    storage.Agent
	butlerErr error
	chars     []storage.Agent
	charsErr  error

	// forUser/forUserErr back the durable /glyphoxa use selection the profile-first
	// resolution reads first (#222); campaign is the most-recent fallback.
	forUser    storage.Campaign
	forUserErr error
	// campaignsByID backs GetCampaign, the live-first roster path's per-id load
	// (#222); getCampaignErr forces its failure path.
	campaignsByID  map[uuid.UUID]storage.Campaign
	getCampaignErr error

	// Campaign management state (#264): campaignList backs ListCampaigns;
	// createdCampaigns records CreateCampaign inputs (created rows also land in
	// campaignsByID so a read-back resolves); updatedCampaigns records
	// UpdateCampaign inputs; setActiveCalls records SetActiveCampaign inputs. The
	// *Err hooks force each failure path.
	campaignList      []storage.Campaign
	listCampaignErr   error
	createdCampaigns  []storage.NewCampaign
	createCampaignErr error
	updatedCampaigns  []storage.CampaignUpdate
	updateCampaignErr error
	setActiveCalls    []setActiveCall
	setActiveErr      error
	// listNodesCampaign/searchNodesCampaign record the campaign id ListNodes /
	// SearchNodes resolved so the scope precedence can be asserted (#222).
	listNodesCampaign   uuid.UUID
	searchNodesCampaign uuid.UUID

	agents    map[uuid.UUID]storage.Agent
	createErr error
	updateErr error
	deleteErr error
	nextColor int

	created []storage.NewAgent

	// KG Node state (#126, #129): nodes backs ListNodes/Update/Delete; nodesCreated
	// records the storage inputs; the *Err hooks force each failure path.
	nodes         []storage.KGNode
	nodesCreated  []storage.NewKGNode
	nodeCreateErr error
	nodeListErr   error
	nodeUpdateErr error
	nodeDeleteErr error

	// KG Edge state (#132): edgesCreated records CreateEdge inputs; edgesOut/edgesIn
	// back NodeEdges; setAgentCalls records SetNodeAgent inputs; setAgentNode is the
	// happy-path node it returns (with AgentID overridden per call); the *Err hooks
	// force each failure path.
	edgesCreated  []storage.NewKGEdge
	edgeCreateErr error
	edgeDeleteErr error
	edgesOut      []storage.KGEdgeWithNodes
	edgesIn       []storage.KGEdgeWithNodes
	nodeEdgesErr  error
	setAgentCalls []setAgentCall
	setAgentNode  storage.KGNode
	setAgentErr   error

	// KG Node search state (#131): searchResults is returned verbatim so the
	// handler's 1:1 rank-order mapping is asserted; searchQuery/searchLimit/searchCalls
	// record what reached storage; nodeSearchErr forces the Internal path.
	searchResults []storage.KGNode
	searchQuery   string
	searchLimit   int
	searchCalls   int
	nodeSearchErr error

	// Tool Grant state (#117): grants maps agent_id → tool_name → config blob (nil
	// = no scope), backing the upsert/delete/list round-trip; the *Err hooks force
	// each failure path.
	grants         map[uuid.UUID]map[string]json.RawMessage
	grantListErr   error
	grantUpsertErr error
	grantDeleteErr error
}

// setAgentCall records one SetNodeAgent invocation for assertions.
type setAgentCall struct {
	nodeID  uuid.UUID
	agentID uuid.NullUUID
}

// setActiveCall records one SetActiveCampaign invocation for assertions (#264).
type setActiveCall struct {
	discordUserID string
	campaignID    uuid.UUID
}

func newFakeStore() *fakeCampaignStore {
	return &fakeCampaignStore{
		agents:        map[uuid.UUID]storage.Agent{},
		campaignsByID: map[uuid.UUID]storage.Campaign{},
	}
}

func (f *fakeCampaignStore) GetActiveCampaign(context.Context) (storage.Campaign, error) {
	return f.campaign, f.campErr
}

func (f *fakeCampaignStore) GetActiveCampaignForUser(context.Context, string) (storage.Campaign, error) {
	if f.forUserErr != nil {
		return storage.Campaign{}, f.forUserErr
	}
	if f.forUser.ID == uuid.Nil {
		return storage.Campaign{}, storage.ErrNotFound
	}
	return f.forUser, nil
}

func (f *fakeCampaignStore) GetCampaign(_ context.Context, id uuid.UUID) (storage.Campaign, error) {
	if f.getCampaignErr != nil {
		return storage.Campaign{}, f.getCampaignErr
	}
	c, ok := f.campaignsByID[id]
	if !ok {
		return storage.Campaign{}, storage.ErrNotFound
	}
	return c, nil
}

func (f *fakeCampaignStore) ListCampaigns(context.Context) ([]storage.Campaign, error) {
	return f.campaignList, f.listCampaignErr
}

func (f *fakeCampaignStore) CreateCampaign(_ context.Context, c storage.NewCampaign) (uuid.UUID, error) {
	if f.createCampaignErr != nil {
		return uuid.Nil, f.createCampaignErr
	}
	f.createdCampaigns = append(f.createdCampaigns, c)
	id := uuid.New()
	// Land the row so CreateCampaign's read-back (GetCampaign) resolves it.
	f.campaignsByID[id] = storage.Campaign{
		ID:       id,
		TenantID: c.TenantID,
		Name:     c.Name,
		System:   c.System,
		Language: c.Language,
	}
	return id, nil
}

func (f *fakeCampaignStore) UpdateCampaign(_ context.Context, c storage.CampaignUpdate) (storage.Campaign, error) {
	if f.updateCampaignErr != nil {
		return storage.Campaign{}, f.updateCampaignErr
	}
	f.updatedCampaigns = append(f.updatedCampaigns, c)
	existing, ok := f.campaignsByID[c.ID]
	if !ok {
		return storage.Campaign{}, storage.ErrNotFound
	}
	existing.Name = c.Name
	existing.System = c.System
	existing.Language = c.Language
	f.campaignsByID[c.ID] = existing
	return existing, nil
}

func (f *fakeCampaignStore) SetActiveCampaign(_ context.Context, discordUserID string, campaignID uuid.UUID) error {
	if f.setActiveErr != nil {
		return f.setActiveErr
	}
	f.setActiveCalls = append(f.setActiveCalls, setActiveCall{discordUserID: discordUserID, campaignID: campaignID})
	return nil
}

func (f *fakeCampaignStore) GetButler(context.Context, uuid.UUID) (storage.Agent, error) {
	return f.butler, f.butlerErr
}

func (f *fakeCampaignStore) CharacterAgents(context.Context, uuid.UUID) ([]storage.Agent, error) {
	return f.chars, f.charsErr
}

func (f *fakeCampaignStore) GetAgent(_ context.Context, id uuid.UUID) (storage.Agent, error) {
	a, ok := f.agents[id]
	if !ok {
		return storage.Agent{}, storage.ErrNotFound
	}
	return a, nil
}

func (f *fakeCampaignStore) CreateAgent(_ context.Context, a storage.NewAgent) (uuid.UUID, error) {
	if f.createErr != nil {
		return uuid.Nil, f.createErr
	}
	f.created = append(f.created, a)
	id := uuid.New()
	f.agents[id] = storage.Agent{
		ID:           id,
		CampaignID:   a.CampaignID,
		Role:         a.Role,
		Name:         a.Name,
		Title:        a.Title,
		Persona:      a.Persona,
		Voice:        a.Voice,
		AddressOnly:  a.AddressOnly,
		SpeakerColor: f.nextColor,
		Aliases:      a.Aliases,
	}
	f.nextColor++
	return id, nil
}

func (f *fakeCampaignStore) UpdateAgent(_ context.Context, u storage.AgentUpdate) (storage.Agent, error) {
	if f.updateErr != nil {
		return storage.Agent{}, f.updateErr
	}
	a, ok := f.agents[u.ID]
	if !ok {
		return storage.Agent{}, storage.ErrNotFound
	}
	a.Name = u.Name
	a.Title = u.Title
	a.Persona = u.Persona
	a.Voice = u.Voice
	a.AddressOnly = u.AddressOnly
	a.Aliases = u.Aliases
	f.agents[u.ID] = a
	return a, nil
}

func (f *fakeCampaignStore) DeleteAgent(_ context.Context, id uuid.UUID) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.agents[id]; !ok {
		return storage.ErrNotFound
	}
	delete(f.agents, id)
	return nil
}

func (f *fakeCampaignStore) CreateNode(_ context.Context, n storage.NewKGNode) (storage.KGNode, error) {
	if f.nodeCreateErr != nil {
		return storage.KGNode{}, f.nodeCreateErr
	}
	f.nodesCreated = append(f.nodesCreated, n)
	created := storage.KGNode{
		ID:         uuid.New(),
		CampaignID: n.CampaignID,
		Type:       n.Type,
		Name:       n.Name,
		Body:       n.Body,
		GMPrivate:  n.GMPrivate,
	}
	f.nodes = append(f.nodes, created)
	return created, nil
}

func (f *fakeCampaignStore) ListNodes(_ context.Context, campaignID uuid.UUID) ([]storage.KGNode, error) {
	f.listNodesCampaign = campaignID
	return f.nodes, f.nodeListErr
}

func (f *fakeCampaignStore) UpdateNode(_ context.Context, u storage.KGNodeUpdate) (storage.KGNode, error) {
	if f.nodeUpdateErr != nil {
		return storage.KGNode{}, f.nodeUpdateErr
	}
	for i := range f.nodes {
		if f.nodes[i].ID == u.ID {
			f.nodes[i].Name = u.Name
			f.nodes[i].Body = u.Body
			f.nodes[i].GMPrivate = u.GMPrivate
			return f.nodes[i], nil
		}
	}
	return storage.KGNode{}, storage.ErrNotFound
}

func (f *fakeCampaignStore) DeleteNode(_ context.Context, id uuid.UUID) error {
	if f.nodeDeleteErr != nil {
		return f.nodeDeleteErr
	}
	for i := range f.nodes {
		if f.nodes[i].ID == id {
			f.nodes = append(f.nodes[:i], f.nodes[i+1:]...)
			return nil
		}
	}
	return storage.ErrNotFound
}

func (f *fakeCampaignStore) CreateEdge(_ context.Context, e storage.NewKGEdge) (storage.KGEdge, error) {
	if f.edgeCreateErr != nil {
		return storage.KGEdge{}, f.edgeCreateErr
	}
	f.edgesCreated = append(f.edgesCreated, e)
	return storage.KGEdge{
		ID:         uuid.New(),
		CampaignID: e.CampaignID,
		FromNodeID: e.FromNodeID,
		ToNodeID:   e.ToNodeID,
		Type:       e.Type,
	}, nil
}

func (f *fakeCampaignStore) DeleteEdge(_ context.Context, _ uuid.UUID) error {
	return f.edgeDeleteErr
}

func (f *fakeCampaignStore) NodeEdges(_ context.Context, _ uuid.UUID) ([]storage.KGEdgeWithNodes, []storage.KGEdgeWithNodes, error) {
	return f.edgesOut, f.edgesIn, f.nodeEdgesErr
}

func (f *fakeCampaignStore) SetNodeAgent(_ context.Context, nodeID uuid.UUID, agentID uuid.NullUUID) (storage.KGNode, error) {
	if f.setAgentErr != nil {
		return storage.KGNode{}, f.setAgentErr
	}
	f.setAgentCalls = append(f.setAgentCalls, setAgentCall{nodeID: nodeID, agentID: agentID})
	n := f.setAgentNode
	n.ID = nodeID
	n.AgentID = agentID
	return n, nil
}

func (f *fakeCampaignStore) SearchNodes(_ context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.KGNode, error) {
	f.searchCalls++
	f.searchNodesCampaign = campaignID
	f.searchQuery = query
	f.searchLimit = limit
	if f.nodeSearchErr != nil {
		return nil, f.nodeSearchErr
	}
	return f.searchResults, nil
}

func (f *fakeCampaignStore) ListToolGrants(_ context.Context, agentID uuid.UUID) ([]storage.ToolGrant, error) {
	if f.grantListErr != nil {
		return nil, f.grantListErr
	}
	var out []storage.ToolGrant
	for tool, cfg := range f.grants[agentID] {
		out = append(out, storage.ToolGrant{AgentID: agentID, ToolName: tool, Config: cfg})
	}
	return out, nil
}

func (f *fakeCampaignStore) UpsertToolGrant(_ context.Context, g storage.NewToolGrant) error {
	if f.grantUpsertErr != nil {
		return f.grantUpsertErr
	}
	if f.grants == nil {
		f.grants = map[uuid.UUID]map[string]json.RawMessage{}
	}
	if f.grants[g.AgentID] == nil {
		f.grants[g.AgentID] = map[string]json.RawMessage{}
	}
	f.grants[g.AgentID][g.ToolName] = g.Config
	return nil
}

func (f *fakeCampaignStore) DeleteToolGrant(_ context.Context, agentID uuid.UUID, toolName string) error {
	if f.grantDeleteErr != nil {
		return f.grantDeleteErr
	}
	if _, ok := f.grants[agentID][toolName]; !ok {
		return storage.ErrNotFound
	}
	delete(f.grants[agentID], toolName)
	return nil
}

// crudClient stands up the full CampaignService handler over an httptest server
// and returns a Connect-JSON client.
func crudClient(t *testing.T, store *fakeCampaignStore) managementv1connect.CampaignServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(rpc.NewCampaignServer(store).Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return managementv1connect.NewCampaignServiceClient(
		http.DefaultClient, srv.URL, connect.WithProtoJSON(),
	)
}

// crudClientAs is crudClient plus an injected authenticated operator (so the
// profile-first resolution's durable-selection lookup sees a Discord identity,
// #222) and an optional live Voice Session source (so the roster/mute panel's
// live-first scope can be exercised). A zero user injects nothing; a nil
// sessions leaves the server with no live source.
func crudClientAs(t *testing.T, store *fakeCampaignStore, user storage.User, sessions *fakeSessionManager) managementv1connect.CampaignServiceClient {
	t.Helper()
	srv := rpc.NewCampaignServer(store)
	if sessions != nil {
		srv.SetSessions(sessions)
	}
	inject := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if user.DiscordUserID != "" {
				ctx = auth.WithUser(ctx, user)
			}
			return next(ctx, req)
		}
	})
	mux := http.NewServeMux()
	mux.Handle(srv.Handler(connect.WithInterceptors(inject)))
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return managementv1connect.NewCampaignServiceClient(
		http.DefaultClient, s.URL, connect.WithProtoJSON(),
	)
}

func TestGetCampaignRoster_Order(t *testing.T) {
	t.Parallel()

	campID := uuid.New()
	store := newFakeStore()
	store.campaign = storage.Campaign{ID: campID, Name: "Lost Mine", System: "dnd5e", Language: "en"}
	store.butler = storage.Agent{ID: uuid.New(), CampaignID: campID, Role: storage.AgentRoleButler, Name: "Glyphoxa", AddressOnly: true, SpeakerColor: 0}
	store.chars = []storage.Agent{
		{ID: uuid.New(), CampaignID: campID, Role: storage.AgentRoleCharacter, Name: "Ana", Title: "Scout", SpeakerColor: 0},
		{ID: uuid.New(), CampaignID: campID, Role: storage.AgentRoleCharacter, Name: "Borin", Title: "Smith", SpeakerColor: 1},
	}

	client := crudClient(t, store)
	resp, err := client.GetCampaignRoster(context.Background(),
		connect.NewRequest(&managementv1.GetCampaignRosterRequest{}))
	if err != nil {
		t.Fatalf("GetCampaignRoster: %v", err)
	}
	if got := resp.Msg.GetCampaign().GetName(); got != "Lost Mine" {
		t.Errorf("campaign name = %q, want Lost Mine", got)
	}
	roster := resp.Msg.GetRoster()
	if len(roster) != 3 {
		t.Fatalf("roster len = %d, want 3 (butler + 2)", len(roster))
	}
	if roster[0].GetRole() != "butler" || !roster[0].GetAddressOnly() {
		t.Errorf("roster[0] should be the Address-Only Butler: %+v", roster[0])
	}
	if roster[1].GetName() != "Ana" || roster[2].GetName() != "Borin" {
		t.Errorf("character order not preserved: %q, %q", roster[1].GetName(), roster[2].GetName())
	}
	if roster[1].GetTitle() != "Scout" {
		t.Errorf("title not mapped: %q", roster[1].GetTitle())
	}
	if roster[2].GetSpeakerColor() != 1 {
		t.Errorf("speaker_color not mapped: %d", roster[2].GetSpeakerColor())
	}
}

func TestGetCampaignRoster_NoCampaign(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campErr = storage.ErrNotFound
	client := crudClient(t, store)

	_, err := client.GetCampaignRoster(context.Background(),
		connect.NewRequest(&managementv1.GetCampaignRosterRequest{}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

func TestGetCampaignRoster_ButlerMissingIsInternal(t *testing.T) {
	t.Parallel()
	// A campaign with no Butler is an ADR-0009 invariant violation, not a client
	// error: it maps to Internal, not NotFound.
	store := newFakeStore()
	store.campaign = storage.Campaign{ID: uuid.New(), Name: "Lost Mine"}
	store.butlerErr = storage.ErrNotFound
	client := crudClient(t, store)

	_, err := client.GetCampaignRoster(context.Background(),
		connect.NewRequest(&managementv1.GetCampaignRosterRequest{}))
	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Errorf("code = %v, want Internal", got)
	}
}

// rosterStore builds a fake whose Butler + one Character resolve for ANY campaign
// id (the fake ignores the id on those reads), so the roster tests can assert
// purely on which campaign GetCampaignRoster resolved.
func rosterStore() *fakeCampaignStore {
	store := newFakeStore()
	store.butler = storage.Agent{ID: uuid.New(), Role: storage.AgentRoleButler, Name: "Glyphoxa", AddressOnly: true}
	store.chars = []storage.Agent{{ID: uuid.New(), Role: storage.AgentRoleCharacter, Name: "Ana"}}
	return store
}

// TestGetCampaignRosterHonorsLiveSession is #222's roster/mute-panel core: while a
// Voice Session is live (bound to campaign L), the roster resolves L even when the
// durable selection (D) and the most-recent default (N) are different campaigns —
// so the GM mutes the NPCs actually in the channel.
func TestGetCampaignRosterHonorsLiveSession(t *testing.T) {
	t.Parallel()
	live := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Live L"}
	durable := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Durable D"}
	newer := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Newer N"}
	store := rosterStore()
	store.forUser = durable
	store.campaign = newer
	store.campaignsByID = map[uuid.UUID]storage.Campaign{live.ID: live}

	mgr := &fakeSessionManager{active: true, current: storage.VoiceSession{ID: uuid.New(), CampaignID: live.ID}}
	client := crudClientAs(t, store, storage.User{DiscordUserID: "999"}, mgr)

	resp, err := client.GetCampaignRoster(context.Background(),
		connect.NewRequest(&managementv1.GetCampaignRosterRequest{}))
	if err != nil {
		t.Fatalf("GetCampaignRoster: %v", err)
	}
	if got := resp.Msg.GetCampaign().GetId(); got != live.ID.String() {
		t.Errorf("roster campaign = %s, want the LIVE session campaign %s (not durable %s / newer %s)",
			got, live.ID, durable.ID, newer.ID)
	}
}

// TestGetCampaignRosterHonorsDurableSelectionIdle is the middle precedence: with no
// live session, the roster resolves the durable /glyphoxa use selection (D), not
// the most-recent default (N).
func TestGetCampaignRosterHonorsDurableSelectionIdle(t *testing.T) {
	t.Parallel()
	durable := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Durable D"}
	newer := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Newer N"}
	store := rosterStore()
	store.forUser = durable
	store.campaign = newer

	// An inactive manager still exercises the SetSessions wiring (live branch skipped).
	client := crudClientAs(t, store, storage.User{DiscordUserID: "999"}, &fakeSessionManager{})

	resp, err := client.GetCampaignRoster(context.Background(),
		connect.NewRequest(&managementv1.GetCampaignRosterRequest{}))
	if err != nil {
		t.Fatalf("GetCampaignRoster: %v", err)
	}
	if got := resp.Msg.GetCampaign().GetId(); got != durable.ID.String() {
		t.Errorf("roster campaign = %s, want the durable selection %s (not the newer %s)", got, durable.ID, newer.ID)
	}
}

// TestGetCampaignRosterFallsBackWithoutSelection pins the fallback tail: no live
// session and no durable selection resolves the most-recently-created campaign.
func TestGetCampaignRosterFallsBackWithoutSelection(t *testing.T) {
	t.Parallel()
	newer := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Newer N"}
	store := rosterStore()
	store.forUserErr = storage.ErrNotFound
	store.campaign = newer
	client := crudClientAs(t, store, storage.User{DiscordUserID: "999"}, nil)

	resp, err := client.GetCampaignRoster(context.Background(),
		connect.NewRequest(&managementv1.GetCampaignRosterRequest{}))
	if err != nil {
		t.Fatalf("GetCampaignRoster: %v", err)
	}
	if got := resp.Msg.GetCampaign().GetId(); got != newer.ID.String() {
		t.Errorf("roster campaign = %s, want the fallback %s", got, newer.ID)
	}
}

// TestCreateAgentHonorsDurableSelection is #222 for the campaign CRUD write: a new
// NPC lands in the durable /glyphoxa use selection (D), not the most-recent
// default (N).
func TestCreateAgentHonorsDurableSelection(t *testing.T) {
	t.Parallel()
	durable := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Durable D"}
	newer := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Newer N"}
	store := newFakeStore()
	store.forUser = durable
	store.campaign = newer
	client := crudClientAs(t, store, storage.User{DiscordUserID: "999"}, nil)

	if _, err := client.CreateAgent(context.Background(),
		connect.NewRequest(&managementv1.CreateAgentRequest{Name: "New NPC"})); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if len(store.created) != 1 {
		t.Fatalf("created %d agents, want 1", len(store.created))
	}
	if got := store.created[0].CampaignID; got != durable.ID {
		t.Errorf("new NPC landed in campaign %s, want the durable selection %s (not the newer %s)", got, durable.ID, newer.ID)
	}
}

// liveMgr returns a fakeSessionManager reporting an active Voice Session bound to
// campaignID — the live-first input every CampaignService surface resolves through
// (#222). The Manager enforces single-active, so one live campaign is enough.
func liveMgr(campaignID uuid.UUID) *fakeSessionManager {
	return &fakeSessionManager{active: true, current: storage.VoiceSession{ID: uuid.New(), CampaignID: campaignID}}
}

// TestGetActiveCampaignHonorsLiveSession is #222 finding 2: the header resolves the
// LIVE Voice Session's campaign (L), not the durable selection (D) or the newest
// (N), so it never names a different campaign than the roster/transcript on the
// same Session screen.
func TestGetActiveCampaignHonorsLiveSession(t *testing.T) {
	t.Parallel()
	live := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Live L"}
	durable := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Durable D"}
	newer := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Newer N"}
	store := newFakeStore()
	store.forUser = durable
	store.campaign = newer
	store.campaignsByID = map[uuid.UUID]storage.Campaign{live.ID: live}
	client := crudClientAs(t, store, storage.User{DiscordUserID: "999"}, liveMgr(live.ID))

	resp, err := client.GetActiveCampaign(context.Background(),
		connect.NewRequest(&managementv1.GetActiveCampaignRequest{}))
	if err != nil {
		t.Fatalf("GetActiveCampaign: %v", err)
	}
	if got := resp.Msg.GetCampaign().GetId(); got != live.ID.String() {
		t.Errorf("header campaign = %s, want the LIVE session campaign %s (not durable %s / newer %s)",
			got, live.ID, durable.ID, newer.ID)
	}
}

// TestCreateAgentHonorsLiveSession is #222 finding 1: mid-session the new NPC lands
// in the LIVE session's campaign (L) — the SAME campaign the roster read shows — so
// the NPC appears where the GM is looking, never a silent cross-campaign write.
func TestCreateAgentHonorsLiveSession(t *testing.T) {
	t.Parallel()
	live := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Live L"}
	durable := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Durable D"}
	newer := storage.Campaign{ID: uuid.New(), TenantID: uuid.New(), Name: "Newer N"}
	store := newFakeStore()
	store.forUser = durable
	store.campaign = newer
	store.campaignsByID = map[uuid.UUID]storage.Campaign{live.ID: live}
	client := crudClientAs(t, store, storage.User{DiscordUserID: "999"}, liveMgr(live.ID))

	if _, err := client.CreateAgent(context.Background(),
		connect.NewRequest(&managementv1.CreateAgentRequest{Name: "New NPC"})); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if len(store.created) != 1 {
		t.Fatalf("created %d agents, want 1", len(store.created))
	}
	if got := store.created[0].CampaignID; got != live.ID {
		t.Errorf("new NPC landed in campaign %s, want the LIVE session campaign %s (not durable %s / newer %s)",
			got, live.ID, durable.ID, newer.ID)
	}
}

// TestGetCampaignRosterLiveLoadErrorIsInternal is #222 finding 3: a non-ErrNotFound
// failure loading the live session's campaign row maps to CodeInternal (the raw
// cause is logged, not returned).
func TestGetCampaignRosterLiveLoadErrorIsInternal(t *testing.T) {
	t.Parallel()
	live := uuid.New()
	store := rosterStore()
	store.getCampaignErr = errAny
	client := crudClientAs(t, store, storage.User{DiscordUserID: "999"}, liveMgr(live))

	_, err := client.GetCampaignRoster(context.Background(),
		connect.NewRequest(&managementv1.GetCampaignRosterRequest{}))
	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Errorf("code = %v, want Internal", got)
	}
}

// TestGetCampaignRosterLiveCampaignMissingIsNotFound is #222 finding 3: if the live
// session's campaign row is gone (ErrNotFound from GetCampaign), the roster surfaces
// CodeNotFound like any no-campaign state.
func TestGetCampaignRosterLiveCampaignMissingIsNotFound(t *testing.T) {
	t.Parallel()
	live := uuid.New()
	store := rosterStore()
	store.campaignsByID = map[uuid.UUID]storage.Campaign{} // live id absent → GetCampaign ErrNotFound
	client := crudClientAs(t, store, storage.User{DiscordUserID: "999"}, liveMgr(live))

	_, err := client.GetCampaignRoster(context.Background(),
		connect.NewRequest(&managementv1.GetCampaignRosterRequest{}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

func TestCreateAgent_IsCharacterWithColor(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campaign = storage.Campaign{ID: uuid.New(), Name: "Lost Mine"}
	store.nextColor = 2
	client := crudClient(t, store)

	resp, err := client.CreateAgent(context.Background(),
		connect.NewRequest(&managementv1.CreateAgentRequest{
			Name: "New NPC", Title: "Wanderer", Persona: "Mysterious.", Voice: "adam",
		}))
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	agent := resp.Msg.GetAgent()
	if agent.GetRole() != "character" {
		t.Errorf("created role = %q, want character", agent.GetRole())
	}
	if agent.GetName() != "New NPC" || agent.GetTitle() != "Wanderer" {
		t.Errorf("fields not mapped: %+v", agent)
	}
	if agent.GetVoice() != "adam" {
		t.Errorf("voice did not round-trip: %q, want adam", agent.GetVoice())
	}
	if agent.GetSpeakerColor() != 2 {
		t.Errorf("speaker_color = %d, want the assigned 2", agent.GetSpeakerColor())
	}
	// The handler must force role 'character' regardless of any client intent.
	if len(store.created) != 1 || store.created[0].Role != storage.AgentRoleCharacter {
		t.Errorf("store was not asked to create a character: %+v", store.created)
	}
}

func TestCreateAgent_NoCampaign(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campErr = storage.ErrNotFound
	client := crudClient(t, store)

	_, err := client.CreateAgent(context.Background(),
		connect.NewRequest(&managementv1.CreateAgentRequest{Name: "x"}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

func TestUpdateAgent_InvalidID(t *testing.T) {
	t.Parallel()
	client := crudClient(t, newFakeStore())

	_, err := client.UpdateAgent(context.Background(),
		connect.NewRequest(&managementv1.UpdateAgentRequest{Id: "not-a-uuid", Name: "x"}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

func TestUpdateAgent_NotFound(t *testing.T) {
	t.Parallel()
	client := crudClient(t, newFakeStore())

	_, err := client.UpdateAgent(context.Background(),
		connect.NewRequest(&managementv1.UpdateAgentRequest{Id: uuid.New().String(), Name: "x"}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

func TestUpdateAgent_HappyPathRoundTrips(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	id := uuid.New()
	store.agents[id] = storage.Agent{ID: id, Role: storage.AgentRoleCharacter, Name: "Old", SpeakerColor: 3}
	client := crudClient(t, store)

	resp, err := client.UpdateAgent(context.Background(),
		connect.NewRequest(&managementv1.UpdateAgentRequest{
			Id: id.String(), Name: "New", Title: "Renamed", Persona: "Changed.",
			Voice: "rachel", AddressOnly: true, Aliases: []string{"alt"},
		}))
	if err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}
	a := resp.Msg.GetAgent()
	if a.GetName() != "New" || a.GetTitle() != "Renamed" || a.GetPersona() != "Changed." {
		t.Errorf("editor fields not applied: %+v", a)
	}
	if a.GetVoice() != "rachel" || !a.GetAddressOnly() || len(a.GetAliases()) != 1 {
		t.Errorf("voice/address_only/aliases not applied: %+v", a)
	}
	if a.GetSpeakerColor() != 3 {
		t.Errorf("speaker_color must be immutable across update: %d", a.GetSpeakerColor())
	}
}

// TestUpdateAgent_PreservesExistingVoiceTuning proves the #224 wiring end-to-end
// through the handler: re-saving the editor with the SAME voice id must leave the
// persisted ProviderID/Language/Settings untouched (the editor only sends a bare
// id, ADR-0039), and switching to a NEW id keeps that tuning while swapping the
// id. Without the GetAgent+GetCampaign read the handler now does, the old writer
// clobbered the whole blob to {"voice_id":…} and the NPC went silent.
func TestUpdateAgent_PreservesExistingVoiceTuning(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campaign = storage.Campaign{ID: uuid.New(), Language: "de"}
	id := uuid.New()

	tuned := ttseleven.DefaultVoice("v1", "en")
	tuned.Name = "Custom"
	blob, err := storage.VoiceToJSON(tuned)
	if err != nil {
		t.Fatalf("VoiceToJSON: %v", err)
	}
	store.agents[id] = storage.Agent{ID: id, Role: storage.AgentRoleCharacter, Name: "Old", Voice: blob}
	client := crudClient(t, store)

	// Same id: the persisted bytes must be byte-identical afterward.
	if _, err := client.UpdateAgent(context.Background(),
		connect.NewRequest(&managementv1.UpdateAgentRequest{Id: id.String(), Name: "Renamed", Voice: "v1"})); err != nil {
		t.Fatalf("UpdateAgent(same voice): %v", err)
	}
	if got := store.agents[id].Voice; !bytes.Equal(got, blob) {
		t.Errorf("same-id re-save changed persisted voice:\n got %s\nwant %s", got, blob)
	}

	// New id: keep Settings/ProviderID, swap VoiceID.
	if _, err := client.UpdateAgent(context.Background(),
		connect.NewRequest(&managementv1.UpdateAgentRequest{Id: id.String(), Name: "Renamed", Voice: "v2"})); err != nil {
		t.Fatalf("UpdateAgent(new voice): %v", err)
	}
	after, err := storage.VoiceFromJSON(store.agents[id].Voice)
	if err != nil {
		t.Fatalf("VoiceFromJSON: %v", err)
	}
	if after.VoiceID != "v2" || after.ProviderID != ttseleven.ProviderID || len(after.Settings) == 0 {
		t.Errorf("voice change dropped tuning: %+v", after)
	}
}

func TestDeleteAgent_ButlerIsFailedPrecondition(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.deleteErr = storage.ErrButlerUndeletable
	client := crudClient(t, store)

	_, err := client.DeleteAgent(context.Background(),
		connect.NewRequest(&managementv1.DeleteAgentRequest{Id: uuid.New().String()}))
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Errorf("code = %v, want FailedPrecondition", got)
	}
}

func TestDeleteAgent_NotFound(t *testing.T) {
	t.Parallel()
	client := crudClient(t, newFakeStore())

	_, err := client.DeleteAgent(context.Background(),
		connect.NewRequest(&managementv1.DeleteAgentRequest{Id: uuid.New().String()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

func TestDeleteAgent_InvalidID(t *testing.T) {
	t.Parallel()
	client := crudClient(t, newFakeStore())

	_, err := client.DeleteAgent(context.Background(),
		connect.NewRequest(&managementv1.DeleteAgentRequest{Id: "nope"}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

func TestDeleteAgent_HappyPath(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	id := uuid.New()
	store.agents[id] = storage.Agent{ID: id, Role: storage.AgentRoleCharacter, Name: "Doomed"}
	client := crudClient(t, store)

	if _, err := client.DeleteAgent(context.Background(),
		connect.NewRequest(&managementv1.DeleteAgentRequest{Id: id.String()})); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if _, ok := store.agents[id]; ok {
		t.Error("agent was not removed from the store")
	}
}
