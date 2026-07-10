//go:build integration

package storage_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// seedCampaign migrates up and inserts a tenant + campaign, returning their IDs.
func seedCampaign(t *testing.T, dsn string) (pool *pgxpool.Pool, tenantID, campaignID uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	db := openSQL(t, dsn)
	if err := storage.MigrateUp(ctx, db); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	pool = openPool(t, dsn)

	if err := pool.QueryRow(ctx,
		`INSERT INTO tenant (name) VALUES ('Acme TTRPG') RETURNING id`).
		Scan(&tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name, system, language)
		 VALUES ($1, 'Lost Mine', 'dnd5e', 'en') RETURNING id`, tenantID).
		Scan(&campaignID); err != nil {
		t.Fatalf("insert campaign: %v", err)
	}
	return pool, tenantID, campaignID
}

func TestLoadAgentWithProviderConfigs(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// Two BYOK provider configs (encrypted creds are opaque bytea here).
	var llmCfgID, ttsCfgID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO provider_config
		   (tenant_id, component, provider, model, credentials_ciphertext, credentials_last4)
		 VALUES ($1, 'llm', 'anthropic', 'claude-sonnet-4-6', '\x00', 'cd12')
		 RETURNING id`, tenantID).Scan(&llmCfgID); err != nil {
		t.Fatalf("insert llm config: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO provider_config
		   (tenant_id, component, provider, model, credentials_ciphertext, credentials_last4)
		 VALUES ($1, 'tts', 'elevenlabs', 'rachel', '\x00', 'ef34')
		 RETURNING id`, tenantID).Scan(&ttsCfgID); err != nil {
		t.Fatalf("insert tts config: %v", err)
	}

	// A Character NPC bound to both configs, with a JSONB voice and aliases.
	var agentID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO agents
		   (campaign_id, agent_role, name, persona, voice,
		    voice_provider_config_id, llm_provider_config_id, address_only, aliases)
		 VALUES ($1, 'character', 'Bart', 'A gruff innkeeper.',
		         '{"voice_id":"rachel"}'::jsonb, $2, $3, false, ARRAY['Barty','the innkeeper'])
		 RETURNING id`, campaignID, ttsCfgID, llmCfgID).Scan(&agentID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	loaded, err := st.LoadAgent(ctx, agentID)
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if loaded.Agent.Name != "Bart" {
		t.Errorf("name = %q, want Bart", loaded.Agent.Name)
	}
	if loaded.Agent.Role != storage.AgentRoleCharacter {
		t.Errorf("role = %q, want character", loaded.Agent.Role)
	}
	if got := len(loaded.Agent.Aliases); got != 2 {
		t.Errorf("aliases len = %d, want 2", got)
	}
	if loaded.LLMConfig == nil || loaded.LLMConfig.Provider != "anthropic" {
		t.Errorf("LLMConfig not resolved correctly: %+v", loaded.LLMConfig)
	}
	if loaded.TTSConfig == nil || loaded.TTSConfig.CredentialsLast4 != "ef34" {
		t.Errorf("TTSConfig not resolved correctly: %+v", loaded.TTSConfig)
	}
	// Voice JSONB must round-trip.
	if string(loaded.Agent.Voice) == "" {
		t.Error("voice JSONB empty")
	}
}

// TestAutoButlerOnCampaignCreate asserts the ADR-0009 auto-create trigger
// (migration 00002): creating a Campaign auto-inserts exactly one Butler named
// "Glyphoxa", Address-Only, with no application code involved.
func TestAutoButlerOnCampaignCreate(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn) // seed only inserts a campaign row
	ctx := context.Background()
	st := storage.New(pool)

	butler, err := st.GetButler(ctx, campaignID)
	if err != nil {
		t.Fatalf("GetButler on a freshly created campaign: %v (auto-create trigger missing?)", err)
	}
	if butler.Name != "Glyphoxa" {
		t.Errorf("auto-Butler name = %q, want Glyphoxa", butler.Name)
	}
	if butler.Role != storage.AgentRoleButler {
		t.Errorf("auto-Butler role = %q, want butler", butler.Role)
	}
	if !butler.AddressOnly {
		t.Error("auto-Butler should be Address-Only by default (ADR-0024)")
	}

	// Exactly one Butler — the trigger fires once and the unique index forbids more.
	agents, err := st.ListAgents(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	butlers := 0
	for _, a := range agents {
		if a.Role == storage.AgentRoleButler {
			butlers++
		}
	}
	if butlers != 1 {
		t.Fatalf("expected exactly 1 auto-created Butler, got %d", butlers)
	}
}

// TestOneButlerPerCampaign asserts the ADR-0009 partial unique index: a Campaign
// already has its auto-created Butler, so a *second* Butler insert is rejected;
// each Campaign gets its own Butler; and Character NPCs are uncapped.
func TestOneButlerPerCampaign(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()

	insertButler := func(campaign uuid.UUID) error {
		_, err := pool.Exec(ctx,
			`INSERT INTO agents (campaign_id, agent_role, name, address_only)
			 VALUES ($1, 'butler', 'Glyphoxa', true)`, campaign)
		return err
	}

	// campaignID already has its auto-created Butler; a second is rejected.
	if err := insertButler(campaignID); err == nil {
		t.Fatal("second butler in same campaign was accepted; partial unique index missing")
	}

	// A second campaign gets its own auto-Butler — inserting another is rejected,
	// proving the index is per-campaign, not global.
	var campaign2 uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name) VALUES ($1, 'Curse of Strahd') RETURNING id`,
		tenantID).Scan(&campaign2); err != nil {
		t.Fatalf("insert campaign2: %v", err)
	}
	if err := insertButler(campaign2); err == nil {
		t.Fatal("a manual butler in campaign2 was accepted; it already has an auto-Butler")
	}

	// Multiple Character NPCs in one campaign are fine (not capped).
	for _, name := range []string{"Bart", "Gundren"} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO agents (campaign_id, agent_role, name) VALUES ($1, 'character', $2)`,
			campaignID, name); err != nil {
			t.Fatalf("insert character %s: %v", name, err)
		}
	}
}

func TestGetButlerAndListAgents(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// The campaign already has its auto-created Butler; add one Character NPC.
	if _, err := pool.Exec(ctx,
		`INSERT INTO agents (campaign_id, agent_role, name, address_only)
		 VALUES ($1, 'character', 'Bart', false)`,
		campaignID); err != nil {
		t.Fatalf("seed character: %v", err)
	}

	butler, err := st.GetButler(ctx, campaignID)
	if err != nil {
		t.Fatalf("GetButler: %v", err)
	}
	if butler.Role != storage.AgentRoleButler || !butler.AddressOnly {
		t.Errorf("butler wrong: role=%q addressOnly=%v", butler.Role, butler.AddressOnly)
	}

	agents, err := st.ListAgents(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 2 { // auto-Butler + Bart
		t.Fatalf("ListAgents len = %d, want 2", len(agents))
	}
}

func TestGetAgentNotFound(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, _ := seedCampaign(t, dsn)
	st := storage.New(pool)
	if _, err := st.GetAgent(context.Background(), uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetAgent(random): got %v, want ErrNotFound", err)
	}
}

// TestGetActiveCampaign asserts the single-operator "active campaign" read:
// the most-recently-created campaign is returned. seedCampaign inserts the
// first campaign; a second campaign is then inserted with an explicitly later
// created_at so the ordering is deterministic (two rows inserted within the
// same statement-second would otherwise tie on created_at's now() default).
func TestGetActiveCampaign(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, firstID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// A newer campaign for the same tenant, forced strictly after the first.
	var secondID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name, system, language, created_at)
		 VALUES ($1, 'Curse of Strahd', 'dnd5e', 'en', now() + interval '1 second')
		 RETURNING id`, tenantID).Scan(&secondID); err != nil {
		t.Fatalf("insert second campaign: %v", err)
	}

	active, err := st.GetActiveCampaign(ctx)
	if err != nil {
		t.Fatalf("GetActiveCampaign: %v", err)
	}
	if active.ID != secondID {
		t.Errorf("active campaign id = %s, want newest %s (got first %s?)", active.ID, secondID, firstID)
	}
	if active.Name != "Curse of Strahd" {
		t.Errorf("active campaign name = %q, want Curse of Strahd", active.Name)
	}
	if active.TenantID != tenantID {
		t.Errorf("active campaign tenant = %s, want %s", active.TenantID, tenantID)
	}
}

// TestGetActiveCampaignTiebreaker asserts the `id DESC` secondary sort: two
// campaigns sharing one identical created_at resolve deterministically to the
// greater id, so "latest" never flaps when rows tie on the now() default.
func TestGetActiveCampaignTiebreaker(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// Two more campaigns sharing one explicit created_at (strictly after the
	// seeded one) so only the id DESC tiebreaker decides the winner.
	ts := time.Now().Add(time.Hour)
	var ids []uuid.UUID
	for _, name := range []string{"Tomb of Annihilation", "Descent into Avernus"} {
		var id uuid.UUID
		if err := pool.QueryRow(ctx,
			`INSERT INTO campaign (tenant_id, name, system, language, created_at)
			 VALUES ($1, $2, 'dnd5e', 'en', $3) RETURNING id`,
			tenantID, name, ts).Scan(&id); err != nil {
			t.Fatalf("insert campaign %q: %v", name, err)
		}
		ids = append(ids, id)
	}

	// Postgres orders uuids by their 16 big-endian bytes; bytes.Compare matches
	// that exactly, so the greater id is the expected `id DESC` winner.
	want := ids[0]
	if bytes.Compare(ids[1][:], ids[0][:]) > 0 {
		want = ids[1]
	}

	active, err := st.GetActiveCampaign(ctx)
	if err != nil {
		t.Fatalf("GetActiveCampaign: %v", err)
	}
	if active.ID != want {
		t.Errorf("tiebreak id = %s, want greater id %s", active.ID, want)
	}
}

// TestGetActiveCampaignEmpty asserts a freshly migrated DB with no campaign
// yields ErrNotFound (mapped to Connect CodeNotFound by the RPC layer).
func TestGetActiveCampaignEmpty(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	db := openSQL(t, dsn)
	if err := storage.MigrateUp(ctx, db); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	pool := openPool(t, dsn)
	st := storage.New(pool)

	if _, err := st.GetActiveCampaign(ctx); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("GetActiveCampaign on empty DB: got %v, want ErrNotFound", err)
	}
}

// TestInTx_FlattensOnTxBoundStore proves the #291 amendment: calling InTx on a
// Store already bound to a transaction runs the fn in that AMBIENT transaction
// (no error, no nested Begin), so a method that uses InTx internally (CreateEdge)
// composes inside a larger import transaction. Inner atomicity is the outer tx's:
// an error after an inner-InTx write rolls the whole outer tx back.
func TestInTx_FlattensOnTxBoundStore(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, _ := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	sentinel := errors.New("boom")

	// The outer InTx runs a nested InTx (flatten) that writes a campaign, then
	// fails. The whole thing must roll back — the campaign must not persist.
	err := st.InTx(ctx, func(tx *storage.Store) error {
		var innerID uuid.UUID
		if err := tx.InTx(ctx, func(tx2 *storage.Store) error {
			id, err := tx2.CreateCampaign(ctx, storage.NewCampaign{
				TenantID: tenantID, Name: "Flatten Probe", System: "dnd5e", Language: "en",
			})
			innerID = id
			return err
		}); err != nil {
			return err
		}
		if innerID == uuid.Nil {
			t.Fatal("inner InTx did not run / returned nil id")
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("outer InTx err = %v, want sentinel", err)
	}

	// The flattened inner write must have been discarded by the outer rollback.
	if _, err := st.FindCampaignByName(ctx, tenantID, "Flatten Probe"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("flattened write survived rollback: %v", err)
	}
}
