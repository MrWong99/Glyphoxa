package rpc_test

import (
	"context"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// spyInvalidator records InvalidateCampaign calls so the Character CRUD hook (#281)
// is assertable.
type spyInvalidator struct {
	mu  sync.Mutex
	ids []uuid.UUID
}

func (s *spyInvalidator) InvalidateCampaign(id uuid.UUID) {
	s.mu.Lock()
	s.ids = append(s.ids, id)
	s.mu.Unlock()
}

func (s *spyInvalidator) calls() []uuid.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uuid.UUID(nil), s.ids...)
}

// TestCharacterMutations_InvalidateSpeakers pins the in-proc invalidation hook
// (#281, ADR-0039): Create/Update/Delete each drop the campaign's cached speaker
// resolutions so the live relay re-resolves future lines with the new mapping.
func TestCharacterMutations_InvalidateSpeakers(t *testing.T) {
	campaign := storage.Campaign{ID: uuid.New(), Name: "Lost Mine"}
	store := newFakeCharacterStore()
	store.campaign = campaign
	spy := &spyInvalidator{}

	srv := rpc.NewCampaignServerWith(rpc.CampaignStores{Active: store, Characters: store})
	srv.SetSpeakerInvalidator(spy)

	ctx := context.Background()
	createResp, err := srv.CreateCharacter(ctx, connect.NewRequest(&managementv1.CreateCharacterRequest{
		Name:          "Kira",
		DiscordUserId: "111111111111111111",
	}))
	if err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}
	if _, err := srv.UpdateCharacter(ctx, connect.NewRequest(&managementv1.UpdateCharacterRequest{
		Id:            createResp.Msg.GetCharacter().GetId(),
		Name:          "Kira Reborn",
		DiscordUserId: "111111111111111111",
	})); err != nil {
		t.Fatalf("UpdateCharacter: %v", err)
	}
	if _, err := srv.DeleteCharacter(ctx, connect.NewRequest(&managementv1.DeleteCharacterRequest{
		Id: createResp.Msg.GetCharacter().GetId(),
	})); err != nil {
		t.Fatalf("DeleteCharacter: %v", err)
	}

	got := spy.calls()
	if len(got) != 3 {
		t.Fatalf("InvalidateCampaign calls = %d, want 3 (create/update/delete)", len(got))
	}
	for i, id := range got {
		if id != campaign.ID {
			t.Errorf("call %d invalidated %s, want campaign %s", i, id, campaign.ID)
		}
	}
}

// TestCharacterMutations_NilInvalidatorSafe: with no invalidator wired (feature
// off / no live resolver) the mutations must not panic.
func TestCharacterMutations_NilInvalidatorSafe(t *testing.T) {
	store := newFakeCharacterStore()
	store.campaign = storage.Campaign{ID: uuid.New(), Name: "Lost Mine"}
	srv := rpc.NewCampaignServerWith(rpc.CampaignStores{Active: store, Characters: store}) // no SetSpeakerInvalidator

	ctx := context.Background()
	resp, err := srv.CreateCharacter(ctx, connect.NewRequest(&managementv1.CreateCharacterRequest{
		Name:          "Kira",
		DiscordUserId: "111111111111111111",
	}))
	if err != nil {
		t.Fatalf("CreateCharacter with nil invalidator: %v", err)
	}
	if _, err := srv.UpdateCharacter(ctx, connect.NewRequest(&managementv1.UpdateCharacterRequest{
		Id:            resp.Msg.GetCharacter().GetId(),
		Name:          "Kira Reborn",
		DiscordUserId: "111111111111111111",
	})); err != nil {
		t.Fatalf("UpdateCharacter with nil invalidator: %v", err)
	}
	if _, err := srv.DeleteCharacter(ctx, connect.NewRequest(&managementv1.DeleteCharacterRequest{
		Id: resp.Msg.GetCharacter().GetId(),
	})); err != nil {
		t.Fatalf("DeleteCharacter with nil invalidator: %v", err)
	}
}
