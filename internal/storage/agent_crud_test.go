//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestCreateUpdateDeleteCharacterAgent round-trips the editor CRUD (#71): create
// a Character with a title, read it back, update its editable fields, then delete
// it. Each step is verified through a fresh GetAgent so the assertions reflect
// what actually landed in Postgres.
func TestCreateUpdateDeleteCharacterAgent(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	id, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID:  campaignID,
		Role:        storage.AgentRoleCharacter,
		Name:        "Bart",
		Title:       "Gruff innkeeper",
		Persona:     "Wipes the bar and grumbles.",
		Voice:       []byte(`{"voice_id":"rachel"}`),
		AddressOnly: false,
		Aliases:     []string{"Barty"},
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	got, err := st.GetAgent(ctx, id)
	if err != nil {
		t.Fatalf("GetAgent after create: %v", err)
	}
	if got.Role != storage.AgentRoleCharacter {
		t.Errorf("role = %q, want character", got.Role)
	}
	if got.Title != "Gruff innkeeper" {
		t.Errorf("title = %q, want %q", got.Title, "Gruff innkeeper")
	}
	if got.Name != "Bart" || got.Persona != "Wipes the bar and grumbles." {
		t.Errorf("name/persona not persisted: %+v", got)
	}
	// First Character in the campaign → slot 0 (round-robin index, see CreateAgent).
	if got.SpeakerColor != 0 {
		t.Errorf("speaker_color = %d, want 0 for the first character", got.SpeakerColor)
	}

	// Update the editable fields.
	updated, err := st.UpdateAgent(ctx, storage.AgentUpdate{
		ID:          id,
		Name:        "Bartholomew",
		Title:       "Keeper of the Stonehill Inn",
		Persona:     "Now eloquent and grandiose.",
		Voice:       []byte(`{"voice_id":"adam"}`),
		AddressOnly: true,
		Aliases:     []string{"Bart", "the keeper"},
	})
	if err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}
	if updated.Name != "Bartholomew" || updated.Title != "Keeper of the Stonehill Inn" {
		t.Errorf("update did not return new name/title: %+v", updated)
	}
	if !updated.AddressOnly {
		t.Error("address_only did not persist true on a Character")
	}
	// speaker_color is immutable across an update.
	if updated.SpeakerColor != 0 {
		t.Errorf("speaker_color changed on update = %d, want 0", updated.SpeakerColor)
	}

	reloaded, err := st.GetAgent(ctx, id)
	if err != nil {
		t.Fatalf("GetAgent after update: %v", err)
	}
	if reloaded.Name != "Bartholomew" || !reloaded.AddressOnly || len(reloaded.Aliases) != 2 {
		t.Errorf("update did not round-trip to DB: %+v", reloaded)
	}

	// Delete it; it must then be gone.
	if err := st.DeleteAgent(ctx, id); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if _, err := st.GetAgent(ctx, id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetAgent after delete: got %v, want ErrNotFound", err)
	}
}

// TestDeleteButlerRejected asserts the Butler cannot be deleted (ADR-0009): the
// auto-created Butler stays, and DeleteAgent returns the distinct
// ErrButlerUndeletable (the RPC maps it to CodeFailedPrecondition).
func TestDeleteButlerRejected(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	butler, err := st.GetButler(ctx, campaignID)
	if err != nil {
		t.Fatalf("GetButler: %v", err)
	}

	err = st.DeleteAgent(ctx, butler.ID)
	if !errors.Is(err, storage.ErrButlerUndeletable) {
		t.Fatalf("DeleteAgent(butler) = %v, want ErrButlerUndeletable", err)
	}
	// The Butler must still be there.
	if _, err := st.GetButler(ctx, campaignID); err != nil {
		t.Fatalf("Butler gone after a rejected delete: %v", err)
	}
}

// TestUpdateButlerKeepsAddressOnly asserts editing the Butler can neither demote
// its role nor turn off Address-Only (ADR-0009 / ADR-0024): even with
// AddressOnly:false in the request, the stored Butler stays Address-Only and a
// 'butler' role.
func TestUpdateButlerKeepsAddressOnly(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	butler, err := st.GetButler(ctx, campaignID)
	if err != nil {
		t.Fatalf("GetButler: %v", err)
	}

	updated, err := st.UpdateAgent(ctx, storage.AgentUpdate{
		ID:          butler.ID,
		Name:        "Glyphoxa the Wise",
		Title:       "Game Master's Familiar",
		Persona:     "A patient arcane butler.",
		AddressOnly: false, // try to turn Address-Only OFF — must be ignored
	})
	if err != nil {
		t.Fatalf("UpdateAgent(butler): %v", err)
	}
	if !updated.AddressOnly {
		t.Error("Butler address_only was turned off; it must stay true")
	}
	if updated.Role != storage.AgentRoleButler {
		t.Errorf("Butler role changed to %q; it must stay butler", updated.Role)
	}
	if updated.Name != "Glyphoxa the Wise" || updated.Title != "Game Master's Familiar" {
		t.Errorf("Butler editable fields did not persist: %+v", updated)
	}

	// And it remains the campaign's one Butler, still Address-Only on reload.
	reloaded, err := st.GetButler(ctx, campaignID)
	if err != nil {
		t.Fatalf("GetButler after update: %v", err)
	}
	if !reloaded.AddressOnly || reloaded.Role != storage.AgentRoleButler {
		t.Errorf("Butler invariant broke on reload: %+v", reloaded)
	}
}

// TestCreateAgentAssignsStableSpeakerColors asserts Characters get distinct,
// round-robin speaker-colour slots in creation order, and that the slot is
// stable across reloads (#71).
func TestCreateAgentAssignsStableSpeakerColors(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	names := []string{"Ana", "Borin", "Cora"}
	ids := make([]uuid.UUID, len(names))
	for i, n := range names {
		id, err := st.CreateAgent(ctx, storage.NewAgent{
			CampaignID: campaignID,
			Role:       storage.AgentRoleCharacter,
			Name:       n,
		})
		if err != nil {
			t.Fatalf("CreateAgent(%s): %v", n, err)
		}
		ids[i] = id
	}

	for i, id := range ids {
		a, err := st.GetAgent(ctx, id)
		if err != nil {
			t.Fatalf("GetAgent(%s): %v", names[i], err)
		}
		if a.SpeakerColor != i {
			t.Errorf("%s speaker_color = %d, want %d (round-robin index)", names[i], a.SpeakerColor, i)
		}
	}
}

// TestDeleteAndUpdateAgentNotFound asserts a random id is ErrNotFound for both
// mutating store ops (the RPC maps it to CodeNotFound).
func TestDeleteAndUpdateAgentNotFound(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	if err := st.DeleteAgent(ctx, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("DeleteAgent(random) = %v, want ErrNotFound", err)
	}
	if _, err := st.UpdateAgent(ctx, storage.AgentUpdate{ID: uuid.New(), Name: "Nobody"}); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("UpdateAgent(random) = %v, want ErrNotFound", err)
	}
}
