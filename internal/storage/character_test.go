//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestCharacterCreateListScoped is #276 AC1 + the scoping AC: a Character inserted
// into campaign A round-trips through CreateCharacter/ListCharacters, and a
// Character in another campaign is never returned for A.
func TestCharacterCreateListScoped(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignA := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	var campaignB uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name) VALUES ($1, 'Other Table') RETURNING id`,
		tenantID).Scan(&campaignB); err != nil {
		t.Fatalf("insert campaign B: %v", err)
	}

	idA, err := st.CreateCharacter(ctx, storage.NewCharacter{
		CampaignID:    campaignA,
		Name:          "Aravel",
		Aliases:       []string{"the ranger", "Ara"},
		DiscordUserID: "111111111111111111",
	})
	if err != nil {
		t.Fatalf("CreateCharacter A: %v", err)
	}
	if idA == uuid.Nil {
		t.Fatal("CreateCharacter returned nil id")
	}

	if _, err := st.CreateCharacter(ctx, storage.NewCharacter{
		CampaignID:    campaignB,
		Name:          "Borin",
		DiscordUserID: "222222222222222222",
	}); err != nil {
		t.Fatalf("CreateCharacter B: %v", err)
	}

	got, err := st.ListCharacters(ctx, campaignA)
	if err != nil {
		t.Fatalf("ListCharacters A: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListCharacters A = %d rows, want 1 (campaign B's Character must not leak)", len(got))
	}
	c := got[0]
	if c.ID != idA || c.CampaignID != campaignA {
		t.Errorf("ids not persisted: %+v", c)
	}
	if c.Name != "Aravel" {
		t.Errorf("name = %q, want Aravel", c.Name)
	}
	if len(c.Aliases) != 2 || c.Aliases[0] != "the ranger" || c.Aliases[1] != "Ara" {
		t.Errorf("aliases = %v, want [the ranger Ara]", c.Aliases)
	}
	if c.DiscordUserID != "111111111111111111" {
		t.Errorf("discord_user_id = %q, want 111111111111111111", c.DiscordUserID)
	}
	if c.LinkedUserID != nil {
		t.Errorf("linked_user_id = %v, want nil (dormant until OAuth, ADR-0003)", *c.LinkedUserID)
	}
	if c.CreatedAt.IsZero() || c.UpdatedAt.IsZero() {
		t.Errorf("timestamps not defaulted: %+v", c)
	}
}

// TestCharacterDuplicateDiscordUser is #276: the UNIQUE (campaign_id,
// discord_user_id) index — one Character per Discord User per Campaign — surfaces
// a duplicate as storage.ErrConflict, but the SAME Discord User may play a
// Character in a different Campaign.
func TestCharacterDuplicateDiscordUser(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignA := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	const discordID = "333333333333333333"
	if _, err := st.CreateCharacter(ctx, storage.NewCharacter{
		CampaignID: campaignA, Name: "First", DiscordUserID: discordID,
	}); err != nil {
		t.Fatalf("CreateCharacter first: %v", err)
	}

	_, err := st.CreateCharacter(ctx, storage.NewCharacter{
		CampaignID: campaignA, Name: "Second", DiscordUserID: discordID,
	})
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("duplicate (campaign, discord_user_id): got %v, want ErrConflict", err)
	}

	// A second campaign may bind the same Discord User to its own Character.
	var campaignB uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name) VALUES ($1, 'Other Table') RETURNING id`,
		tenantID).Scan(&campaignB); err != nil {
		t.Fatalf("insert campaign B: %v", err)
	}
	if _, err := st.CreateCharacter(ctx, storage.NewCharacter{
		CampaignID: campaignB, Name: "Elsewhere", DiscordUserID: discordID,
	}); err != nil {
		t.Fatalf("same Discord User in campaign B should be allowed: %v", err)
	}
}

// TestCharacterUpdateRebind is #276 AC2: UpdateCharacter is a full-field save that
// rebinds discord_user_id (it stays NOT NULL) and edits name/aliases; an unknown
// id yields ErrNotFound.
func TestCharacterUpdateRebind(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignA := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	id, err := st.CreateCharacter(ctx, storage.NewCharacter{
		CampaignID: campaignA, Name: "Old Name", Aliases: []string{"old"}, DiscordUserID: "444444444444444444",
	})
	if err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}

	updated, err := st.UpdateCharacter(ctx, storage.CharacterUpdate{
		ID:            id,
		CampaignID:    campaignA,
		Name:          "New Name",
		Aliases:       []string{"new", "renamed"},
		DiscordUserID: "555555555555555555", // rebind to a different Discord User
	})
	if err != nil {
		t.Fatalf("UpdateCharacter: %v", err)
	}
	if updated.Name != "New Name" {
		t.Errorf("name = %q, want New Name", updated.Name)
	}
	if len(updated.Aliases) != 2 || updated.Aliases[1] != "renamed" {
		t.Errorf("aliases = %v, want [new renamed]", updated.Aliases)
	}
	if updated.DiscordUserID != "555555555555555555" {
		t.Errorf("discord_user_id = %q, want rebound 555555555555555555", updated.DiscordUserID)
	}

	if _, err := st.UpdateCharacter(ctx, storage.CharacterUpdate{
		ID: uuid.New(), CampaignID: campaignA, Name: "Ghost", DiscordUserID: "666666666666666666",
	}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("UpdateCharacter unknown id: got %v, want ErrNotFound", err)
	}
}

// TestCharacterDelete is #276: DeleteCharacter removes the row; an unknown id is
// ErrNotFound; and deleting the owning Campaign CASCADEs its Characters away.
func TestCharacterDelete(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignA := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	id, err := st.CreateCharacter(ctx, storage.NewCharacter{
		CampaignID: campaignA, Name: "Doomed", DiscordUserID: "777777777777777777",
	})
	if err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}
	if err := st.DeleteCharacter(ctx, campaignA, id); err != nil {
		t.Fatalf("DeleteCharacter: %v", err)
	}
	if err := st.DeleteCharacter(ctx, campaignA, id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("DeleteCharacter twice: got %v, want ErrNotFound", err)
	}

	// CASCADE: a Character in a fresh campaign vanishes when the campaign is dropped.
	var campaignB uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name) VALUES ($1, 'Ephemeral') RETURNING id`,
		tenantID).Scan(&campaignB); err != nil {
		t.Fatalf("insert campaign B: %v", err)
	}
	if _, err := st.CreateCharacter(ctx, storage.NewCharacter{
		CampaignID: campaignB, Name: "Cascade", DiscordUserID: "888888888888888888",
	}); err != nil {
		t.Fatalf("CreateCharacter B: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM campaign WHERE id = $1`, campaignB); err != nil {
		t.Fatalf("delete campaign B: %v", err)
	}
	rows, err := st.ListCharacters(ctx, campaignB)
	if err != nil {
		t.Fatalf("ListCharacters after cascade: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("cascade left %d Characters, want 0", len(rows))
	}
}

// TestGetCharacterByDiscordUser is #276 (consumed by #281): a hit returns the
// Character; a miss and a cross-campaign lookup both yield ErrNotFound.
func TestGetCharacterByDiscordUser(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignA := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	const discordID = "999999999999999999"
	id, err := st.CreateCharacter(ctx, storage.NewCharacter{
		CampaignID: campaignA, Name: "Findable", DiscordUserID: discordID,
	})
	if err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}

	got, err := st.GetCharacterByDiscordUser(ctx, campaignA, discordID)
	if err != nil {
		t.Fatalf("GetCharacterByDiscordUser hit: %v", err)
	}
	if got.ID != id || got.Name != "Findable" {
		t.Errorf("hit = %+v, want id %s name Findable", got, id)
	}

	if _, err := st.GetCharacterByDiscordUser(ctx, campaignA, "000000000000000000"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("miss: got %v, want ErrNotFound", err)
	}

	// Cross-campaign: the same Discord User is not found under a different campaign.
	var campaignB uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name) VALUES ($1, 'Other') RETURNING id`,
		tenantID).Scan(&campaignB); err != nil {
		t.Fatalf("insert campaign B: %v", err)
	}
	if _, err := st.GetCharacterByDiscordUser(ctx, campaignB, discordID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cross-campaign: got %v, want ErrNotFound", err)
	}
}

// TestCharacterMutationsAreCampaignScoped is #342: UpdateCharacter/DeleteCharacter
// match (id, campaign_id), so passing another Campaign's id refuses the mutation
// with ErrNotFound and leaves the Character untouched.
func TestCharacterMutationsAreCampaignScoped(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignA := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	var campaignB uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name) VALUES ($1, 'Other Table') RETURNING id`,
		tenantID).Scan(&campaignB); err != nil {
		t.Fatalf("insert campaign B: %v", err)
	}

	id, err := st.CreateCharacter(ctx, storage.NewCharacter{
		CampaignID: campaignA, Name: "Aravel", DiscordUserID: "111111111111111111",
	})
	if err != nil {
		t.Fatalf("CreateCharacter A: %v", err)
	}

	// Update scoped to campaign B must refuse and change nothing.
	if _, err := st.UpdateCharacter(ctx, storage.CharacterUpdate{
		ID: id, CampaignID: campaignB, Name: "Hijacked", DiscordUserID: "222222222222222222",
	}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cross-campaign UpdateCharacter = %v, want ErrNotFound", err)
	}
	// Delete scoped to campaign B must refuse and leave the row.
	if err := st.DeleteCharacter(ctx, campaignB, id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("cross-campaign DeleteCharacter = %v, want ErrNotFound", err)
	}

	// The Character is intact under campaign A with its original fields.
	got, err := st.GetCharacterByDiscordUser(ctx, campaignA, "111111111111111111")
	if err != nil {
		t.Fatalf("GetCharacterByDiscordUser after refused cross-campaign mutations: %v", err)
	}
	if got.ID != id || got.Name != "Aravel" {
		t.Errorf("cross-campaign mutation leaked through: %+v", got)
	}
}

// TestCharacterRebindConflict is #342 (the review's untested path): rebinding a
// Character's discord_user_id onto another Character's (campaign, discord_user_id)
// trips the UNIQUE index (Postgres 23505) and UpdateCharacter maps it to
// ErrConflict — now proven against a real DB, not just a fake. The first row is
// left unchanged.
func TestCharacterRebindConflict(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignA := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	const takenID = "111111111111111111"
	if _, err := st.CreateCharacter(ctx, storage.NewCharacter{
		CampaignID: campaignA, Name: "Aravel", DiscordUserID: takenID,
	}); err != nil {
		t.Fatalf("CreateCharacter first: %v", err)
	}
	second, err := st.CreateCharacter(ctx, storage.NewCharacter{
		CampaignID: campaignA, Name: "Borin", DiscordUserID: "222222222222222222",
	})
	if err != nil {
		t.Fatalf("CreateCharacter second: %v", err)
	}

	// Rebind the second Character onto the first's Discord User → 23505 → ErrConflict.
	if _, err := st.UpdateCharacter(ctx, storage.CharacterUpdate{
		ID: second, CampaignID: campaignA, Name: "Borin", DiscordUserID: takenID,
	}); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("rebind onto a taken Discord User = %v, want ErrConflict", err)
	}

	// The second Character keeps its original binding; the conflicting write rolled
	// back cleanly.
	got, err := st.GetCharacterByDiscordUser(ctx, campaignA, "222222222222222222")
	if err != nil {
		t.Fatalf("GetCharacterByDiscordUser after conflict: %v", err)
	}
	if got.ID != second {
		t.Errorf("conflicting rebind changed the row: %+v", got)
	}
}
