package rpc_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Per-feature fakes for the CampaignServer feature modules (#445). Each fake
// implements exactly ONE per-feature store slice (plus the shared 3-method
// active-campaign resolver via the embedded *fakeActive), so a test composes
// rpc.CampaignStores from only the slices its feature exercises. State fields
// and semantics are carried over from the former 39-method fakeCampaignStore.

// errAny is an opaque storage failure a fake returns to force the Internal path.
var errAny = errors.New("fake storage failure")

// fakeActive is the keyless, deterministic active-campaign resolver every
// feature fake embeds. forUser/forUserErr back the durable /glyphoxa use
// selection the profile-first resolution reads first (#222); campaign is the
// most-recent fallback; campaignsByID backs GetCampaign, the live-first path's
// per-id load (also the management pre-write validation and read-back).
type fakeActive struct {
	campaign   storage.Campaign
	campErr    error
	forUser    storage.Campaign
	forUserErr error

	campaignsByID  map[uuid.UUID]storage.Campaign
	getCampaignErr error

	// resolveTenant records the last tenant id threaded into a scoped resolver read
	// (#473), so a test asserts the ctx tenant reached storage.
	resolveTenant uuid.UUID
}

func newFakeActive() *fakeActive {
	return &fakeActive{campaignsByID: map[uuid.UUID]storage.Campaign{}}
}

// The resolver reads are tenant-scoped (#473). resolveTenant records the LAST
// tenant id the handler threaded down, so a test asserts the ctx tenant reached
// storage; foreignTenant, when set, makes the scoped reads enforce isolation —
// a campaign whose TenantID differs from the requested (non-nil) tenant reads back
// as ErrNotFound, exactly as the real WHERE tenant_id guard would.
func (f *fakeActive) scopedOut(c storage.Campaign, tenantID uuid.UUID) (storage.Campaign, error) {
	f.resolveTenant = tenantID
	if tenantID != uuid.Nil && c.TenantID != uuid.Nil && c.TenantID != tenantID {
		return storage.Campaign{}, storage.ErrNotFound
	}
	return c, nil
}

func (f *fakeActive) GetActiveCampaignInTenant(_ context.Context, tenantID uuid.UUID) (storage.Campaign, error) {
	f.resolveTenant = tenantID
	if f.campErr != nil {
		return storage.Campaign{}, f.campErr
	}
	return f.scopedOut(f.campaign, tenantID)
}

func (f *fakeActive) GetActiveCampaignForUserInTenant(_ context.Context, tenantID uuid.UUID, _ string) (storage.Campaign, error) {
	f.resolveTenant = tenantID
	if f.forUserErr != nil {
		return storage.Campaign{}, f.forUserErr
	}
	if f.forUser.ID == uuid.Nil {
		return storage.Campaign{}, storage.ErrNotFound
	}
	return f.scopedOut(f.forUser, tenantID)
}

func (f *fakeActive) GetCampaignInTenant(_ context.Context, tenantID, id uuid.UUID) (storage.Campaign, error) {
	f.resolveTenant = tenantID
	if f.getCampaignErr != nil {
		return storage.Campaign{}, f.getCampaignErr
	}
	c, ok := f.campaignsByID[id]
	if !ok {
		return storage.Campaign{}, storage.ErrNotFound
	}
	return f.scopedOut(c, tenantID)
}

// setActiveCall records one SetActiveCampaign invocation for assertions (#264).
type setActiveCall struct {
	discordUserID string
	campaignID    uuid.UUID
}

// fakeManagementStore fakes the campaign-management slice (#264): campaignList
// backs ListCampaigns; createdCampaigns records CreateCampaign inputs (created
// rows also land in campaignsByID so a read-back resolves); updatedCampaigns
// records UpdateCampaign inputs; setActiveCalls records SetActiveCampaign
// inputs; allCampaignList backs the archive-inclusive list (#269). The *Err
// hooks force each failure path.
type fakeManagementStore struct {
	*fakeActive

	campaignList      []storage.Campaign
	listCampaignErr   error
	listTenant        uuid.UUID
	createdCampaigns  []storage.NewCampaign
	createCampaignErr error
	updatedCampaigns  []storage.CampaignUpdate
	updateCampaignErr error
	setActiveCalls    []setActiveCall
	setActiveErr      error
	allCampaignList   []storage.Campaign
	listAllErr        error
}

func newFakeManagementStore() *fakeManagementStore {
	return &fakeManagementStore{fakeActive: newFakeActive()}
}

func (f *fakeManagementStore) ListCampaignsInTenant(_ context.Context, tenantID uuid.UUID) ([]storage.Campaign, error) {
	f.listTenant = tenantID
	return f.campaignList, f.listCampaignErr
}

func (f *fakeManagementStore) CreateCampaign(_ context.Context, c storage.NewCampaign) (uuid.UUID, error) {
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

func (f *fakeManagementStore) UpdateCampaign(_ context.Context, c storage.CampaignUpdate) (storage.Campaign, error) {
	if f.updateCampaignErr != nil {
		return storage.Campaign{}, f.updateCampaignErr
	}
	f.updatedCampaigns = append(f.updatedCampaigns, c)
	existing, ok := f.campaignsByID[c.ID]
	if !ok {
		return storage.Campaign{}, storage.ErrNotFound
	}
	// Tenant scoping (#473): a foreign-tenant update is invisible → ErrNotFound.
	if c.TenantID != uuid.Nil && existing.TenantID != uuid.Nil && existing.TenantID != c.TenantID {
		return storage.Campaign{}, storage.ErrNotFound
	}
	existing.Name = c.Name
	existing.System = c.System
	existing.Language = c.Language
	if c.TapeArmed != nil { // optional: nil leaves it unchanged (COALESCE semantics)
		existing.TapeArmed = *c.TapeArmed
	}
	f.campaignsByID[c.ID] = existing
	return existing, nil
}

func (f *fakeManagementStore) SetActiveCampaign(_ context.Context, discordUserID string, campaignID uuid.UUID) error {
	if f.setActiveErr != nil {
		return f.setActiveErr
	}
	f.setActiveCalls = append(f.setActiveCalls, setActiveCall{discordUserID: discordUserID, campaignID: campaignID})
	return nil
}

func (f *fakeManagementStore) ListAllCampaignsInTenant(_ context.Context, tenantID uuid.UUID) ([]storage.Campaign, error) {
	f.listTenant = tenantID
	return f.allCampaignList, f.listAllErr
}

// fakeArchiveStore fakes the archive-lifecycle slice (#269) on top of the full
// management fake (the archive panel drives list + archive RPCs through one
// store): archiveCalls/unarchiveCalls/deleteCalls record the ids each handler
// passed; the *Result campaigns are returned on the happy path; the *Err hooks
// force each failure path (e.g. storage.ErrNotFound, storage.ErrNotArchived);
// deleteJobKind/deleteJobPayload capture the DeleteCampaignWithJob enqueue (#308).
type fakeArchiveStore struct {
	*fakeManagementStore

	archiveCalls      []uuid.UUID
	archiveTenant     uuid.UUID
	archiveResult     storage.Campaign
	archiveErr        error
	unarchiveCalls    []uuid.UUID
	unarchiveTenant   uuid.UUID
	unarchiveResult   storage.Campaign
	unarchiveErr      error
	deleteCalls       []uuid.UUID
	deleteTenant      uuid.UUID
	deleteCampaignErr error
	deleteJobKind     string
	deleteJobPayload  []byte
}

func newFakeArchiveStore() *fakeArchiveStore {
	return &fakeArchiveStore{fakeManagementStore: newFakeManagementStore()}
}

func (f *fakeArchiveStore) ArchiveCampaign(_ context.Context, tenantID, id uuid.UUID) (storage.Campaign, error) {
	f.archiveTenant = tenantID
	f.archiveCalls = append(f.archiveCalls, id)
	if f.archiveErr != nil {
		return storage.Campaign{}, f.archiveErr
	}
	return f.archiveResult, nil
}

func (f *fakeArchiveStore) UnarchiveCampaign(_ context.Context, tenantID, id uuid.UUID) (storage.Campaign, error) {
	f.unarchiveTenant = tenantID
	f.unarchiveCalls = append(f.unarchiveCalls, id)
	if f.unarchiveErr != nil {
		return storage.Campaign{}, f.unarchiveErr
	}
	return f.unarchiveResult, nil
}

func (f *fakeArchiveStore) DeleteCampaign(_ context.Context, tenantID, id uuid.UUID) error {
	f.deleteTenant = tenantID
	f.deleteCalls = append(f.deleteCalls, id)
	return f.deleteCampaignErr
}

func (f *fakeArchiveStore) DeleteCampaignWithJob(_ context.Context, tenantID, id uuid.UUID, jobKind string, jobPayload []byte) error {
	f.deleteTenant = tenantID
	f.deleteCalls = append(f.deleteCalls, id)
	if f.deleteCampaignErr != nil {
		// Delete refused inside the tx: nothing committed, so no job is recorded — the
		// real store enqueues in the SAME tx that rolls back.
		return f.deleteCampaignErr
	}
	f.deleteJobKind = jobKind
	f.deleteJobPayload = jobPayload
	return nil
}

// fakeAgentStore fakes the Agent roster/CRUD slice (#71): the agents map backs
// GetAgent/Create/Update/Delete so happy paths round-trip without a database;
// butler/chars back the roster read; the *Err hooks force the failure paths.
// updateAgentCampaign/deleteAgentCampaign record the campaign id the mutations
// were scoped to (#342).
type fakeAgentStore struct {
	*fakeActive

	butler    storage.Agent
	butlerErr error
	chars     []storage.Agent
	charsErr  error

	agents    map[uuid.UUID]storage.Agent
	createErr error
	updateErr error
	deleteErr error
	nextColor int

	updateAgentCampaign uuid.UUID
	deleteAgentCampaign uuid.UUID

	created []storage.NewAgent
}

func newFakeAgentStore() *fakeAgentStore {
	return &fakeAgentStore{fakeActive: newFakeActive(), agents: map[uuid.UUID]storage.Agent{}}
}

func (f *fakeAgentStore) GetButler(context.Context, uuid.UUID) (storage.Agent, error) {
	return f.butler, f.butlerErr
}

func (f *fakeAgentStore) CharacterAgents(context.Context, uuid.UUID) ([]storage.Agent, error) {
	return f.chars, f.charsErr
}

func (f *fakeAgentStore) GetAgent(_ context.Context, id uuid.UUID) (storage.Agent, error) {
	a, ok := f.agents[id]
	if !ok {
		return storage.Agent{}, storage.ErrNotFound
	}
	return a, nil
}

func (f *fakeAgentStore) CreateAgent(_ context.Context, a storage.NewAgent) (uuid.UUID, error) {
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

func (f *fakeAgentStore) UpdateAgent(_ context.Context, u storage.AgentUpdate) (storage.Agent, error) {
	f.updateAgentCampaign = u.CampaignID
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

func (f *fakeAgentStore) DeleteAgent(_ context.Context, campaignID, id uuid.UUID) error {
	f.deleteAgentCampaign = campaignID
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.agents[id]; !ok {
		return storage.ErrNotFound
	}
	delete(f.agents, id)
	return nil
}

// fakeCharacterStore fakes the Player Character slice (#276): characters backs
// List/Update/Delete; charsCreated records CreateCharacter inputs;
// charsListCampaign records the campaign id ListCharacters resolved (scope
// assertion); the *Err hooks force each failure path (e.g. storage.ErrConflict,
// storage.ErrNotFound). charUpdateCampaign/charDeleteCampaign record the
// campaign id the mutations were scoped to (#342).
type fakeCharacterStore struct {
	*fakeActive

	characters        []storage.Character
	charsCreated      []storage.NewCharacter
	charsListCampaign uuid.UUID
	charCreateErr     error
	charListErr       error
	charUpdateErr     error
	charDeleteErr     error

	charUpdateCampaign uuid.UUID
	charDeleteCampaign uuid.UUID
}

func newFakeCharacterStore() *fakeCharacterStore {
	return &fakeCharacterStore{fakeActive: newFakeActive()}
}

func (f *fakeCharacterStore) ListCharacters(_ context.Context, campaignID uuid.UUID) ([]storage.Character, error) {
	f.charsListCampaign = campaignID
	return f.characters, f.charListErr
}

func (f *fakeCharacterStore) CreateCharacter(_ context.Context, c storage.NewCharacter) (uuid.UUID, error) {
	if f.charCreateErr != nil {
		return uuid.Nil, f.charCreateErr
	}
	f.charsCreated = append(f.charsCreated, c)
	id := uuid.New()
	f.characters = append(f.characters, storage.Character{
		ID:            id,
		CampaignID:    c.CampaignID,
		Name:          c.Name,
		Aliases:       c.Aliases,
		DiscordUserID: c.DiscordUserID,
	})
	return id, nil
}

func (f *fakeCharacterStore) UpdateCharacter(_ context.Context, u storage.CharacterUpdate) (storage.Character, error) {
	f.charUpdateCampaign = u.CampaignID
	if f.charUpdateErr != nil {
		return storage.Character{}, f.charUpdateErr
	}
	for i := range f.characters {
		if f.characters[i].ID == u.ID {
			f.characters[i].Name = u.Name
			f.characters[i].Aliases = u.Aliases
			f.characters[i].DiscordUserID = u.DiscordUserID
			return f.characters[i], nil
		}
	}
	return storage.Character{}, storage.ErrNotFound
}

func (f *fakeCharacterStore) DeleteCharacter(_ context.Context, campaignID, id uuid.UUID) error {
	f.charDeleteCampaign = campaignID
	if f.charDeleteErr != nil {
		return f.charDeleteErr
	}
	for i := range f.characters {
		if f.characters[i].ID == id {
			f.characters = append(f.characters[:i], f.characters[i+1:]...)
			return nil
		}
	}
	return storage.ErrNotFound
}

// fakeKGNodeStore fakes the KG-Node slice (#126, #131): nodes backs
// ListNodes/Update/Delete; nodesCreated records the storage inputs; the search
// fields record what reached storage (#131) and searchResults is returned
// verbatim so the handler's 1:1 rank-order mapping is asserted; the *Err hooks
// force each failure path. listNodesCampaign/searchNodesCampaign and the
// mutation-campaign fields record the resolved scope (#222/#342).
type fakeKGNodeStore struct {
	*fakeActive

	nodes         []storage.KGNode
	nodesCreated  []storage.NewKGNode
	nodeCreateErr error
	nodeListErr   error
	nodeUpdateErr error
	nodeDeleteErr error

	listNodesCampaign  uuid.UUID
	updateNodeCampaign uuid.UUID
	deleteNodeCampaign uuid.UUID

	searchResults       []storage.KGNode
	searchQuery         string
	searchLimit         int
	searchCalls         int
	searchNodesCampaign uuid.UUID
	nodeSearchErr       error
}

func newFakeKGNodeStore() *fakeKGNodeStore {
	return &fakeKGNodeStore{fakeActive: newFakeActive()}
}

func (f *fakeKGNodeStore) CreateNode(_ context.Context, n storage.NewKGNode) (storage.KGNode, error) {
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

func (f *fakeKGNodeStore) ListNodes(_ context.Context, campaignID uuid.UUID) ([]storage.KGNode, error) {
	f.listNodesCampaign = campaignID
	return f.nodes, f.nodeListErr
}

func (f *fakeKGNodeStore) UpdateNode(_ context.Context, u storage.KGNodeUpdate) (storage.KGNode, error) {
	f.updateNodeCampaign = u.CampaignID
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

func (f *fakeKGNodeStore) DeleteNode(_ context.Context, campaignID, id uuid.UUID) error {
	f.deleteNodeCampaign = campaignID
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

func (f *fakeKGNodeStore) SearchNodes(_ context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.KGNode, error) {
	f.searchCalls++
	f.searchNodesCampaign = campaignID
	f.searchQuery = query
	f.searchLimit = limit
	if f.nodeSearchErr != nil {
		return nil, f.nodeSearchErr
	}
	return f.searchResults, nil
}

// setAgentCall records one SetNodeAgent invocation for assertions.
type setAgentCall struct {
	nodeID  uuid.UUID
	agentID uuid.NullUUID
}

// fakeKGEdgeStore fakes the KG-Edge slice (#132): edgesCreated records
// CreateEdge inputs; edgesOut/edgesIn back NodeEdges; setAgentCalls records
// SetNodeAgent inputs; setAgentNode is the happy-path node it returns (with
// AgentID overridden per call); the *Err hooks force each failure path. The
// *Campaign fields record the resolved scope (#342/#356).
type fakeKGEdgeStore struct {
	*fakeActive

	edgesCreated  []storage.NewKGEdge
	edgeCreateErr error
	edgeDeleteErr error
	edgesOut      []storage.KGEdgeWithNodes
	edgesIn       []storage.KGEdgeWithNodes
	nodeEdgesErr  error

	nodeEdgesCampaign  uuid.UUID
	deleteEdgeCampaign uuid.UUID
	setAgentCampaign   uuid.UUID

	setAgentCalls []setAgentCall
	setAgentNode  storage.KGNode
	setAgentErr   error
}

func newFakeKGEdgeStore() *fakeKGEdgeStore {
	return &fakeKGEdgeStore{fakeActive: newFakeActive()}
}

func (f *fakeKGEdgeStore) CreateEdge(_ context.Context, e storage.NewKGEdge) (storage.KGEdge, error) {
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

func (f *fakeKGEdgeStore) DeleteEdge(_ context.Context, campaignID, _ uuid.UUID) error {
	f.deleteEdgeCampaign = campaignID
	return f.edgeDeleteErr
}

func (f *fakeKGEdgeStore) NodeEdges(_ context.Context, campaignID, _ uuid.UUID) ([]storage.KGEdgeWithNodes, []storage.KGEdgeWithNodes, error) {
	f.nodeEdgesCampaign = campaignID
	return f.edgesOut, f.edgesIn, f.nodeEdgesErr
}

func (f *fakeKGEdgeStore) SetNodeAgent(_ context.Context, campaignID, nodeID uuid.UUID, agentID uuid.NullUUID) (storage.KGNode, error) {
	f.setAgentCampaign = campaignID
	if f.setAgentErr != nil {
		return storage.KGNode{}, f.setAgentErr
	}
	f.setAgentCalls = append(f.setAgentCalls, setAgentCall{nodeID: nodeID, agentID: agentID})
	n := f.setAgentNode
	n.ID = nodeID
	n.AgentID = agentID
	return n, nil
}

// fakeProposalStore fakes the Knowledge Proposal review slice (#300):
// pendingProposals backs the list read; getProposal/getProposalErr back
// GetPendingKnowledgeProposal; approve/reject record calls and force error
// paths; similarResults/similarErr back SimilarNodes and similarQueryVec
// records the vector it was queried with; the search fields back the fulltext
// fallback of the similarity hint (ADR-0011).
type fakeProposalStore struct {
	*fakeActive

	pendingProposals      []storage.KnowledgeProposal
	listProposalsErr      error
	listProposalsCampaign uuid.UUID
	getProposal           storage.KnowledgeProposal
	getProposalErr        error
	getProposalCampaign   uuid.UUID
	approveErr            error
	approveCalls          []uuid.UUID
	approveCampaign       uuid.UUID
	rejectErr             error
	rejectCalls           []uuid.UUID
	rejectCampaign        uuid.UUID
	similarResults        []storage.KGNode
	similarErr            error
	similarCalls          int
	similarQueryVec       []float32

	searchResults       []storage.KGNode
	searchQuery         string
	searchLimit         int
	searchCalls         int
	searchNodesCampaign uuid.UUID
	nodeSearchErr       error
}

func newFakeProposalStore() *fakeProposalStore {
	return &fakeProposalStore{fakeActive: newFakeActive()}
}

func (f *fakeProposalStore) ListPendingKnowledgeProposals(_ context.Context, campaignID uuid.UUID) ([]storage.KnowledgeProposal, error) {
	f.listProposalsCampaign = campaignID
	return f.pendingProposals, f.listProposalsErr
}

func (f *fakeProposalStore) GetPendingKnowledgeProposal(_ context.Context, campaignID, _ uuid.UUID) (storage.KnowledgeProposal, error) {
	f.getProposalCampaign = campaignID
	if f.getProposalErr != nil {
		return storage.KnowledgeProposal{}, f.getProposalErr
	}
	return f.getProposal, nil
}

func (f *fakeProposalStore) ApproveKnowledgeProposal(_ context.Context, campaignID, id uuid.UUID) error {
	f.approveCampaign = campaignID
	f.approveCalls = append(f.approveCalls, id)
	return f.approveErr
}

func (f *fakeProposalStore) RejectKnowledgeProposal(_ context.Context, campaignID, id uuid.UUID) error {
	f.rejectCampaign = campaignID
	f.rejectCalls = append(f.rejectCalls, id)
	return f.rejectErr
}

func (f *fakeProposalStore) SimilarNodes(_ context.Context, _ uuid.UUID, query []float32, _ int) ([]storage.KGNode, error) {
	f.similarCalls++
	f.similarQueryVec = query
	if f.similarErr != nil {
		return nil, f.similarErr
	}
	return f.similarResults, nil
}

func (f *fakeProposalStore) SearchNodes(_ context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.KGNode, error) {
	f.searchCalls++
	f.searchNodesCampaign = campaignID
	f.searchQuery = query
	f.searchLimit = limit
	if f.nodeSearchErr != nil {
		return nil, f.nodeSearchErr
	}
	return f.searchResults, nil
}

// fakeGrantStore fakes the Tool Grant slice (#117): grants maps agent_id →
// tool_name → config blob (nil = no scope), backing the upsert/delete/list
// round-trip; agents backs the campaign-ownership check's GetAgent (#342/#356);
// the *Err hooks force each failure path.
type fakeGrantStore struct {
	*fakeActive

	agents map[uuid.UUID]storage.Agent

	grants         map[uuid.UUID]map[string]json.RawMessage
	grantListErr   error
	grantUpsertErr error
	grantDeleteErr error
}

func newFakeGrantStore() *fakeGrantStore {
	return &fakeGrantStore{fakeActive: newFakeActive(), agents: map[uuid.UUID]storage.Agent{}}
}

func (f *fakeGrantStore) GetAgent(_ context.Context, id uuid.UUID) (storage.Agent, error) {
	a, ok := f.agents[id]
	if !ok {
		return storage.Agent{}, storage.ErrNotFound
	}
	return a, nil
}

func (f *fakeGrantStore) ListToolGrants(_ context.Context, agentID uuid.UUID) ([]storage.ToolGrant, error) {
	if f.grantListErr != nil {
		return nil, f.grantListErr
	}
	var out []storage.ToolGrant
	for tool, cfg := range f.grants[agentID] {
		out = append(out, storage.ToolGrant{AgentID: agentID, ToolName: tool, Config: cfg})
	}
	return out, nil
}

func (f *fakeGrantStore) UpsertToolGrant(_ context.Context, g storage.NewToolGrant) error {
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

func (f *fakeGrantStore) DeleteToolGrant(_ context.Context, agentID uuid.UUID, toolName string) error {
	if f.grantDeleteErr != nil {
		return f.grantDeleteErr
	}
	if _, ok := f.grants[agentID][toolName]; !ok {
		return storage.ErrNotFound
	}
	delete(f.grants[agentID], toolName)
	return nil
}

// liveMgr returns a fakeSessionManager reporting an active Voice Session bound to
// campaignID — the live-first input every CampaignService surface resolves through
// (#222). The Manager enforces single-active, so one live campaign is enough.
func liveMgr(campaignID uuid.UUID) *fakeSessionManager {
	return &fakeSessionManager{active: true, current: storage.VoiceSession{ID: uuid.New(), CampaignID: campaignID}}
}

// campaignClient stands up the CampaignService handler composed from the given
// per-feature stores over an httptest server and returns a Connect-JSON client.
// WithProtoJSON forces the JSON codec on the wire, so this also asserts the RPC
// is reachable over Connect-JSON.
func campaignClient(t *testing.T, stores rpc.CampaignStores) managementv1connect.CampaignServiceClient {
	t.Helper()
	return campaignClientServe(t, rpc.NewCampaignServerWith(stores))
}

// campaignClientWithEmbedder is campaignClient with a wired Embedder so the
// ListSimilarKnowledge vector path (#300) is exercised.
func campaignClientWithEmbedder(t *testing.T, stores rpc.CampaignStores, emb rpc.Embedder) managementv1connect.CampaignServiceClient {
	t.Helper()
	srv := rpc.NewCampaignServerWith(stores)
	srv.SetEmbedder(emb)
	return campaignClientServe(t, srv)
}

// campaignClientAs is campaignClient plus an injected authenticated operator +
// tenant (the auth interceptor stack's resolved principal, ADR-0039) and an
// optional live Voice Session source (so the live-first scope can be
// exercised). A zero user/tenant injects nothing; a nil sessions leaves the
// server with no live source.
func campaignClientAs(t *testing.T, stores rpc.CampaignStores, user storage.User, tenantID uuid.UUID, sessions *fakeSessionManager) managementv1connect.CampaignServiceClient {
	t.Helper()
	srv := rpc.NewCampaignServerWith(stores)
	if sessions != nil {
		srv.SetSessions(sessions)
	}
	inject := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if user.DiscordUserID != "" {
				ctx = auth.WithUser(ctx, user)
			}
			if tenantID != uuid.Nil {
				ctx = auth.WithTenant(ctx, tenantID)
			}
			return next(ctx, req)
		}
	})
	return campaignClientServe(t, srv, connect.WithInterceptors(inject))
}

// campaignClientServe mounts an already-composed CampaignServer behind an
// httptest server and returns the Connect-JSON client.
func campaignClientServe(t *testing.T, srv *rpc.CampaignServer, opts ...connect.HandlerOption) managementv1connect.CampaignServiceClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(srv.Handler(opts...))
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return managementv1connect.NewCampaignServiceClient(
		http.DefaultClient, s.URL, connect.WithProtoJSON(),
	)
}
