//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

const gmSnowflake = "555000111222"

// insertCampaign adds a second campaign in the same tenant, returning its id.
func insertCampaign(t *testing.T, st *storage.Store, tenantID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	// Use a direct write through the pool the Store already holds; CreateCampaign
	// needs a NewCampaign — but the campaign-creation seam differs across tests, so
	// keep this local to the active-campaign concern.
	id, err := st.CreateCampaign(context.Background(), storage.NewCampaign{
		TenantID: tenantID, Name: name, System: "dnd5e", Language: "en",
	})
	if err != nil {
		t.Fatalf("create campaign %q: %v", name, err)
	}
	return id
}

// TestActiveCampaignRoundTrip proves the #108 durable selection against a real
// Postgres: SetActiveCampaign upserts the operator's users row (even before any
// web login) and GetActiveCampaignForUser reads the chosen campaign back;
// re-selecting a different campaign updates in place.
func TestActiveCampaignRoundTrip(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, first := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// No selection yet (and no user row at all) → ErrNotFound.
	if _, err := st.GetActiveCampaignForUserInTenant(ctx, tenantID, gmSnowflake); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetActiveCampaignForUser before any selection = %v, want ErrNotFound", err)
	}

	// SetActiveCampaign upserts the user row and records the choice.
	if err := st.SetActiveCampaign(ctx, gmSnowflake, first); err != nil {
		t.Fatalf("SetActiveCampaign: %v", err)
	}
	if _, err := st.GetUserByDiscordID(ctx, gmSnowflake); err != nil {
		t.Fatalf("SetActiveCampaign did not create the user row: %v", err)
	}
	got, err := st.GetActiveCampaignForUserInTenant(ctx, tenantID, gmSnowflake)
	if err != nil {
		t.Fatalf("GetActiveCampaignForUser: %v", err)
	}
	if got.ID != first {
		t.Errorf("active campaign = %s, want %s", got.ID, first)
	}

	// Re-selecting a different campaign updates the same row in place.
	second := insertCampaign(t, st, tenantID, "Second Campaign")
	if err := st.SetActiveCampaign(ctx, gmSnowflake, second); err != nil {
		t.Fatalf("SetActiveCampaign (re-select): %v", err)
	}
	got, err = st.GetActiveCampaignForUserInTenant(ctx, tenantID, gmSnowflake)
	if err != nil || got.ID != second {
		t.Fatalf("after re-select = %s, %v; want %s", got.ID, err, second)
	}
}

// TestActiveCampaignPersistsAcrossReopen proves AC1 "persists across a process
// restart": a fresh Store over the same DSN reads the same selection.
func TestActiveCampaignPersistsAcrossReopen(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()

	if err := storage.New(pool).SetActiveCampaign(ctx, gmSnowflake, campaignID); err != nil {
		t.Fatalf("SetActiveCampaign: %v", err)
	}

	// A brand-new pool + Store (a "restarted process") sees the persisted choice.
	reopened := storage.New(openPool(t, dsn))
	got, err := reopened.GetActiveCampaignForUserInTenant(ctx, tenantID, gmSnowflake)
	if err != nil || got.ID != campaignID {
		t.Fatalf("after reopen = %s, %v; want %s", got.ID, err, campaignID)
	}
}

// TestActiveCampaignOverridesImplicitDefault is the ADR-0009 resolution
// precedence at the storage grain: the operator's explicit selection
// (GetActiveCampaignForUser) diverges from — and is honored over — the
// most-recently-created implicit default (GetActiveCampaign). This is what lets
// the web StartSession honor the durable /glyphoxa use choice (#108) while
// keeping GetActiveCampaign as its fresh-install fallback.
func TestActiveCampaignOverridesImplicitDefault(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, older := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// A newer campaign becomes the implicit default (most-recently-created).
	newer := insertCampaign(t, st, tenantID, "Newer Campaign")
	def, err := st.GetActiveCampaign(ctx)
	if err != nil || def.ID != newer {
		t.Fatalf("implicit default = %s, %v; want the newer campaign %s", def.ID, err, newer)
	}

	// The operator explicitly selects the OLDER campaign; the per-operator read
	// must return that, not the implicit default.
	if err := st.SetActiveCampaign(ctx, gmSnowflake, older); err != nil {
		t.Fatalf("SetActiveCampaign: %v", err)
	}
	got, err := st.GetActiveCampaignForUserInTenant(ctx, tenantID, gmSnowflake)
	if err != nil || got.ID != older {
		t.Fatalf("per-operator selection = %s, %v; want the older campaign %s", got.ID, err, older)
	}
}

// TestGetOperatorActiveCampaign is the #323 standalone-voice durable read (no
// logged-in user context): the sole operator's /glyphoxa use selection is
// returned even when a NEWER campaign is the most-recently-created implicit
// default — so the standalone voice node and the web tier voice the SAME
// campaign. With no durable selection at all it is ErrNotFound, so the caller
// falls through to GetActiveCampaign.
func TestGetOperatorActiveCampaign(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, older := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// No operator has selected anything yet → ErrNotFound (falls through to recent).
	if _, err := st.GetOperatorActiveCampaign(ctx); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetOperatorActiveCampaign before any selection = %v, want ErrNotFound", err)
	}

	// A newer campaign is the implicit recent default...
	newer := insertCampaign(t, st, tenantID, "Newer Campaign")
	if def, err := st.GetActiveCampaign(ctx); err != nil || def.ID != newer {
		t.Fatalf("implicit default = %s, %v; want the newer campaign %s", def.ID, err, newer)
	}
	// ...but the operator durably selected the OLDER one via /glyphoxa use.
	if err := st.SetActiveCampaign(ctx, gmSnowflake, older); err != nil {
		t.Fatalf("SetActiveCampaign: %v", err)
	}

	got, err := st.GetOperatorActiveCampaign(ctx)
	if err != nil {
		t.Fatalf("GetOperatorActiveCampaign: %v", err)
	}
	if got.ID != older {
		t.Errorf("operator active campaign = %s, want the durable older selection %s (not the recent default)", got.ID, older)
	}
}

// TestGetActiveCampaignForUserClearedOnDeletedCampaign proves the FK's ON DELETE
// SET NULL clears a stale selection: deleting the chosen campaign makes
// GetActiveCampaignForUser return ErrNotFound (so the slash surface then fails
// with the /use hint, and the web tier falls back to GetActiveCampaign), and a
// user with a NULL selection is likewise ErrNotFound.
func TestGetActiveCampaignForUserClearedOnDeletedCampaign(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// A user with a row but no selection → ErrNotFound.
	if _, err := st.UpsertUser(ctx, storage.UpsertUserParams{DiscordUserID: gmSnowflake}); err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if _, err := st.GetActiveCampaignForUserInTenant(ctx, tenantID, gmSnowflake); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("NULL selection = %v, want ErrNotFound", err)
	}

	// Select a throwaway campaign, then delete it: the FK nulls the pointer.
	victim := insertCampaign(t, st, tenantID, "Doomed Campaign")
	if err := st.SetActiveCampaign(ctx, gmSnowflake, victim); err != nil {
		t.Fatalf("SetActiveCampaign: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM campaign WHERE id = $1`, victim); err != nil {
		t.Fatalf("delete campaign: %v", err)
	}
	if _, err := st.GetActiveCampaignForUserInTenant(ctx, tenantID, gmSnowflake); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("selection after campaign delete = %v, want ErrNotFound (FK SET NULL)", err)
	}
}

// TestListAndGetCampaign covers the /glyphoxa use autocomplete + resolution
// reads: ListCampaigns returns every campaign name-ordered, GetCampaign fetches
// one by id, and an unknown id is ErrNotFound.
func TestListAndGetCampaign(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, first := seedCampaign(t, dsn) // seeded name: "Lost Mine"
	ctx := context.Background()
	st := storage.New(pool)

	second := insertCampaign(t, st, tenantID, "Alpha Quest")

	list, err := st.ListCampaigns(ctx)
	if err != nil {
		t.Fatalf("ListCampaigns: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListCampaigns len = %d, want 2", len(list))
	}
	// Ordered by name: "Alpha Quest" before "Lost Mine".
	if list[0].ID != second || list[1].ID != first {
		t.Errorf("ListCampaigns order = [%s %s], want name-ordered [%s %s]", list[0].ID, list[1].ID, second, first)
	}

	got, err := st.GetCampaign(ctx, first)
	if err != nil || got.ID != first {
		t.Fatalf("GetCampaign(%s) = %s, %v", first, got.ID, err)
	}
	if _, err := st.GetCampaign(ctx, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("GetCampaign(random) = %v, want ErrNotFound", err)
	}
}

// TestUpdateCampaign covers the #264 write against a real Postgres: name/system/
// language are written verbatim (opaque free-text), updated_at is bumped, and an
// unknown id is ErrNotFound.
func TestUpdateCampaign(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, first := seedCampaign(t, dsn) // seeded: name "Lost Mine", system dnd5e, language en
	ctx := context.Background()
	st := storage.New(pool)

	before, err := st.GetCampaign(ctx, first)
	if err != nil {
		t.Fatalf("GetCampaign(before): %v", err)
	}

	updated, err := st.UpdateCampaign(ctx, storage.CampaignUpdate{
		TenantID: tenantID, ID: first, Name: "Renamed Quest", System: "Homebrew: 3d6 ⚔️", Language: "Draconic (made up)",
	})
	if err != nil {
		t.Fatalf("UpdateCampaign: %v", err)
	}
	if updated.Name != "Renamed Quest" || updated.System != "Homebrew: 3d6 ⚔️" || updated.Language != "Draconic (made up)" {
		t.Errorf("update did not write the opaque fields verbatim: %+v", updated)
	}
	if !updated.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("updated_at not bumped: before %v, after %v", before.UpdatedAt, updated.UpdatedAt)
	}
	// The write persists — a fresh read reflects it.
	reread, err := st.GetCampaign(ctx, first)
	if err != nil || reread.Name != "Renamed Quest" || reread.System != "Homebrew: 3d6 ⚔️" {
		t.Errorf("re-read after update = %+v, %v", reread, err)
	}

	// An unknown id is ErrNotFound (the RPC layer maps it to CodeNotFound).
	if _, err := st.UpdateCampaign(ctx, storage.CampaignUpdate{TenantID: tenantID, ID: uuid.New(), Name: "x"}); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("UpdateCampaign(random) = %v, want ErrNotFound", err)
	}
}

// TestUpdateCampaignLanguageLeavesAgentVoiceUntouched is the DB half of the #268
// decision: a Campaign Language edit mutates NOTHING downstream — no statement or
// trigger on the campaign UPDATE rewrites agents.voice, so an Agent's persisted
// voice blob stays byte-identical. The handler half is structural since #445 (the
// rpc management store slice cannot even name an Agent write); this pins the SQL
// layer, guarding against a future migration/trigger that would re-derive every
// Agent's voice from the new language (the first-save seeding lives ONLY on the
// agent-write path, #224).
func TestUpdateCampaignLanguageLeavesAgentVoiceUntouched(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campID := seedCampaign(t, dsn) // seeded in language "en", auto-Butler fired
	ctx := context.Background()
	st := storage.New(pool)

	// Give the auto-Butler a concrete voice via the agent-write path — the one
	// place a language may legitimately seed voice defaults (#224).
	butler, err := st.GetButler(ctx, campID)
	if err != nil {
		t.Fatalf("GetButler: %v", err)
	}
	seeded, err := st.UpdateAgent(ctx, storage.AgentUpdate{
		ID: butler.ID, CampaignID: campID, Name: butler.Name, Title: butler.Title,
		Persona: butler.Persona, AddressOnly: butler.AddressOnly, Aliases: butler.Aliases,
		Voice: []byte(`{"provider_id":"elevenlabs","voice_id":"v-268","language":"en"}`),
	})
	if err != nil {
		t.Fatalf("UpdateAgent(seed voice): %v", err)
	}
	if len(seeded.Voice) == 0 {
		t.Fatalf("precondition: Butler voice not seeded, got %q", string(seeded.Voice))
	}

	// The change under test: Campaign Language en -> de.
	if _, err := st.UpdateCampaign(ctx, storage.CampaignUpdate{
		TenantID: tenantID, ID: campID, Name: "Lost Mine", System: "dnd5e", Language: "de",
	}); err != nil {
		t.Fatalf("UpdateCampaign(lang en->de): %v", err)
	}

	after, err := st.GetAgent(ctx, butler.ID)
	if err != nil {
		t.Fatalf("GetAgent(after): %v", err)
	}
	if string(after.Voice) != string(seeded.Voice) {
		t.Errorf("Butler voice changed on a language edit:\n before = %s\n after  = %s",
			string(seeded.Voice), string(after.Voice))
	}
}
