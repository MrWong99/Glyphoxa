package rpc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

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

	agents    map[uuid.UUID]storage.Agent
	createErr error
	updateErr error
	deleteErr error
	nextColor int

	created []storage.NewAgent
}

func newFakeStore() *fakeCampaignStore {
	return &fakeCampaignStore{agents: map[uuid.UUID]storage.Agent{}}
}

func (f *fakeCampaignStore) GetActiveCampaign(context.Context) (storage.Campaign, error) {
	return f.campaign, f.campErr
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
