package rpc_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// characterClient composes a CampaignServer from the Player Character fake —
// filling only the Active + Characters slices (#445) — and returns the
// Connect-JSON client.
func characterClient(t *testing.T, store *fakeCharacterStore) managementv1connect.CampaignServiceClient {
	t.Helper()
	return campaignClient(t, rpc.CampaignStores{Active: store, Characters: store})
}

// --- CreateCharacter ---

func TestCreateCharacter_MapsAndPersists(t *testing.T) {
	t.Parallel()
	store := newFakeCharacterStore()
	store.campaign = storage.Campaign{ID: uuid.New(), Name: "Lost Mine"}
	client := characterClient(t, store)

	resp, err := client.CreateCharacter(context.Background(),
		connect.NewRequest(&managementv1.CreateCharacterRequest{
			Name:          "  Aravel  ",
			Aliases:       []string{"the ranger"},
			DiscordUserId: "111111111111111111",
		}))
	if err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}
	got := resp.Msg.GetCharacter()
	if got.GetId() == "" {
		t.Error("response missing id")
	}
	if got.GetName() != "Aravel" {
		t.Errorf("echoed name = %q, want trimmed Aravel", got.GetName())
	}
	if got.GetDiscordUserId() != "111111111111111111" {
		t.Errorf("discord_user_id = %q", got.GetDiscordUserId())
	}
	if got.GetLinkedUserId() != "" {
		t.Errorf("linked_user_id = %q, want empty (dormant)", got.GetLinkedUserId())
	}
	// The handler forwarded the campaign-scoped, trimmed input to storage.
	if len(store.charsCreated) != 1 {
		t.Fatalf("store saw %d creates, want 1", len(store.charsCreated))
	}
	if store.charsCreated[0].CampaignID != store.campaign.ID {
		t.Errorf("create not scoped to active campaign: %+v", store.charsCreated[0])
	}
	if store.charsCreated[0].Name != "Aravel" {
		t.Errorf("stored name = %q, want trimmed", store.charsCreated[0].Name)
	}
}

func TestCreateCharacter_EmptyNameIsInvalidArgument(t *testing.T) {
	t.Parallel()
	store := newFakeCharacterStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	client := characterClient(t, store)

	_, err := client.CreateCharacter(context.Background(),
		connect.NewRequest(&managementv1.CreateCharacterRequest{
			Name: "   ", DiscordUserId: "111111111111111111",
		}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

func TestCreateCharacter_NonSnowflakeIsInvalidArgument(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "not-a-number", "123abc", "-5", "12 34", "12.3"} {
		store := newFakeCharacterStore()
		store.campaign = storage.Campaign{ID: uuid.New()}
		client := characterClient(t, store)

		_, err := client.CreateCharacter(context.Background(),
			connect.NewRequest(&managementv1.CreateCharacterRequest{
				Name: "Aravel", DiscordUserId: bad,
			}))
		if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
			t.Errorf("discord_user_id %q: code = %v, want InvalidArgument", bad, got)
		}
		if len(store.charsCreated) != 0 {
			t.Errorf("discord_user_id %q reached storage; validation must gate it", bad)
		}
	}
}

func TestCreateCharacter_DuplicateIsAlreadyExists(t *testing.T) {
	t.Parallel()
	store := newFakeCharacterStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	store.charCreateErr = storage.ErrConflict
	client := characterClient(t, store)

	_, err := client.CreateCharacter(context.Background(),
		connect.NewRequest(&managementv1.CreateCharacterRequest{
			Name: "Aravel", DiscordUserId: "111111111111111111",
		}))
	if got := connect.CodeOf(err); got != connect.CodeAlreadyExists {
		t.Errorf("code = %v, want AlreadyExists", got)
	}
}

func TestCreateCharacter_NoCampaignIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeCharacterStore()
	store.campErr = storage.ErrNotFound
	client := characterClient(t, store)

	_, err := client.CreateCharacter(context.Background(),
		connect.NewRequest(&managementv1.CreateCharacterRequest{
			Name: "Aravel", DiscordUserId: "111111111111111111",
		}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

// --- ListCharacters ---

func TestListCharacters_ScopedToActiveCampaign(t *testing.T) {
	t.Parallel()
	store := newFakeCharacterStore()
	activeID := uuid.New()
	store.campaign = storage.Campaign{ID: activeID}
	store.characters = []storage.Character{
		{ID: uuid.New(), CampaignID: activeID, Name: "Aravel", DiscordUserID: "1"},
	}
	client := characterClient(t, store)

	resp, err := client.ListCharacters(context.Background(),
		connect.NewRequest(&managementv1.ListCharactersRequest{}))
	if err != nil {
		t.Fatalf("ListCharacters: %v", err)
	}
	if len(resp.Msg.GetCharacters()) != 1 {
		t.Fatalf("got %d characters, want 1", len(resp.Msg.GetCharacters()))
	}
	if store.charsListCampaign != activeID {
		t.Errorf("list scoped to %s, want active %s", store.charsListCampaign, activeID)
	}
}

func TestListCharacters_NoCampaignIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeCharacterStore()
	store.campErr = storage.ErrNotFound
	client := characterClient(t, store)

	_, err := client.ListCharacters(context.Background(),
		connect.NewRequest(&managementv1.ListCharactersRequest{}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

// --- UpdateCharacter ---

func TestUpdateCharacter_Rebinds(t *testing.T) {
	t.Parallel()
	store := newFakeCharacterStore()
	activeID := uuid.New()
	store.campaign = storage.Campaign{ID: activeID}
	id := uuid.New()
	store.characters = []storage.Character{
		{ID: id, CampaignID: activeID, Name: "Old", DiscordUserID: "111111111111111111"},
	}
	client := characterClient(t, store)

	resp, err := client.UpdateCharacter(context.Background(),
		connect.NewRequest(&managementv1.UpdateCharacterRequest{
			Id:            id.String(),
			Name:          "New",
			Aliases:       []string{"renamed"},
			DiscordUserId: "222222222222222222",
		}))
	if err != nil {
		t.Fatalf("UpdateCharacter: %v", err)
	}
	got := resp.Msg.GetCharacter()
	if got.GetName() != "New" || got.GetDiscordUserId() != "222222222222222222" {
		t.Errorf("rebind not applied: %+v", got)
	}
}

func TestUpdateCharacter_InvalidIdIsInvalidArgument(t *testing.T) {
	t.Parallel()
	store := newFakeCharacterStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	client := characterClient(t, store)

	_, err := client.UpdateCharacter(context.Background(),
		connect.NewRequest(&managementv1.UpdateCharacterRequest{
			Id: "not-a-uuid", Name: "New", DiscordUserId: "111111111111111111",
		}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

func TestUpdateCharacter_UnknownIdIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeCharacterStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	client := characterClient(t, store)

	_, err := client.UpdateCharacter(context.Background(),
		connect.NewRequest(&managementv1.UpdateCharacterRequest{
			Id: uuid.NewString(), Name: "New", DiscordUserId: "111111111111111111",
		}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

func TestUpdateCharacter_ConflictIsAlreadyExists(t *testing.T) {
	t.Parallel()
	store := newFakeCharacterStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	store.charUpdateErr = storage.ErrConflict
	client := characterClient(t, store)

	_, err := client.UpdateCharacter(context.Background(),
		connect.NewRequest(&managementv1.UpdateCharacterRequest{
			Id: uuid.NewString(), Name: "New", DiscordUserId: "111111111111111111",
		}))
	if got := connect.CodeOf(err); got != connect.CodeAlreadyExists {
		t.Errorf("code = %v, want AlreadyExists", got)
	}
}

func TestUpdateCharacter_NonSnowflakeIsInvalidArgument(t *testing.T) {
	t.Parallel()
	store := newFakeCharacterStore()
	store.campaign = storage.Campaign{ID: uuid.New()}
	client := characterClient(t, store)

	_, err := client.UpdateCharacter(context.Background(),
		connect.NewRequest(&managementv1.UpdateCharacterRequest{
			Id: uuid.NewString(), Name: "New", DiscordUserId: "nope",
		}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

// TestUpdateCharacter_NoActiveCampaignIsNotFound is #342: UpdateCharacter gained
// active-campaign resolution to scope the write; with no active campaign it returns
// CodeNotFound rather than mutating by bare id.
func TestUpdateCharacter_NoActiveCampaignIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeCharacterStore()
	store.campErr = storage.ErrNotFound
	client := characterClient(t, store)

	_, err := client.UpdateCharacter(context.Background(),
		connect.NewRequest(&managementv1.UpdateCharacterRequest{
			Id: uuid.NewString(), Name: "New", DiscordUserId: "111111111111111111",
		}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

// --- DeleteCharacter ---

func TestDeleteCharacter_Removes(t *testing.T) {
	t.Parallel()
	store := newFakeCharacterStore()
	id := uuid.New()
	store.characters = []storage.Character{{ID: id, Name: "Doomed", DiscordUserID: "1"}}
	client := characterClient(t, store)

	if _, err := client.DeleteCharacter(context.Background(),
		connect.NewRequest(&managementv1.DeleteCharacterRequest{Id: id.String()})); err != nil {
		t.Fatalf("DeleteCharacter: %v", err)
	}
	if len(store.characters) != 0 {
		t.Errorf("character not removed: %+v", store.characters)
	}
}

func TestDeleteCharacter_InvalidIdIsInvalidArgument(t *testing.T) {
	t.Parallel()
	store := newFakeCharacterStore()
	client := characterClient(t, store)

	_, err := client.DeleteCharacter(context.Background(),
		connect.NewRequest(&managementv1.DeleteCharacterRequest{Id: "bad"}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

func TestDeleteCharacter_UnknownIdIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeCharacterStore()
	client := characterClient(t, store)

	_, err := client.DeleteCharacter(context.Background(),
		connect.NewRequest(&managementv1.DeleteCharacterRequest{Id: uuid.NewString()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}

// TestUpdateCharacter_ScopesToActiveCampaign is #342: the handler resolves the
// active campaign and passes its id down, so the store's UPDATE matches (id,
// campaign_id) and a cross-campaign write is refused server-side.
func TestUpdateCharacter_ScopesToActiveCampaign(t *testing.T) {
	t.Parallel()
	store := newFakeCharacterStore()
	activeID := uuid.New()
	store.campaign = storage.Campaign{ID: activeID}
	id := uuid.New()
	store.characters = []storage.Character{
		{ID: id, CampaignID: activeID, Name: "Old", DiscordUserID: "111111111111111111"},
	}
	client := characterClient(t, store)

	if _, err := client.UpdateCharacter(context.Background(),
		connect.NewRequest(&managementv1.UpdateCharacterRequest{
			Id: id.String(), Name: "New", DiscordUserId: "111111111111111111",
		})); err != nil {
		t.Fatalf("UpdateCharacter: %v", err)
	}
	if store.charUpdateCampaign != activeID {
		t.Errorf("UpdateCharacter scoped to %s, want active %s", store.charUpdateCampaign, activeID)
	}
}

// TestDeleteCharacter_ScopesToActiveCampaign is #342: the delete is scoped to the
// resolved active campaign, so another campaign's Character is never removable.
func TestDeleteCharacter_ScopesToActiveCampaign(t *testing.T) {
	t.Parallel()
	store := newFakeCharacterStore()
	activeID := uuid.New()
	store.campaign = storage.Campaign{ID: activeID}
	id := uuid.New()
	store.characters = []storage.Character{{ID: id, CampaignID: activeID, Name: "Doomed", DiscordUserID: "1"}}
	client := characterClient(t, store)

	if _, err := client.DeleteCharacter(context.Background(),
		connect.NewRequest(&managementv1.DeleteCharacterRequest{Id: id.String()})); err != nil {
		t.Fatalf("DeleteCharacter: %v", err)
	}
	if store.charDeleteCampaign != activeID {
		t.Errorf("DeleteCharacter scoped to %s, want active %s", store.charDeleteCampaign, activeID)
	}
}

// TestDeleteCharacter_NoActiveCampaignIsNotFound is #342: without an active
// campaign the scoped delete cannot resolve an owner and returns CodeNotFound.
func TestDeleteCharacter_NoActiveCampaignIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeCharacterStore()
	store.campErr = storage.ErrNotFound
	client := characterClient(t, store)

	_, err := client.DeleteCharacter(context.Background(),
		connect.NewRequest(&managementv1.DeleteCharacterRequest{Id: uuid.NewString()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound", got)
	}
}
