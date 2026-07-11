//go:build integration

package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestButlerDefaultGrantsSeeded is the #372 default-grant bar: the auto-Butler
// trigger (extended in 00025 then 00027) seeds the Butler's read-only Tool set —
// `dice` + `transcript_search` + `kg_query` + `recap` (#297 decisions 1 & 5) — each
// with a NULL scope config. seedCampaign creates a Campaign, whose auto-Butler
// trigger fires the grant inserts.
func TestButlerDefaultGrantsSeeded(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	butler, err := st.GetButler(ctx, campaignID)
	if err != nil {
		t.Fatalf("GetButler: %v", err)
	}

	grants, err := st.ListToolGrants(ctx, butler.ID)
	if err != nil {
		t.Fatalf("ListToolGrants(butler): %v", err)
	}
	got := map[string]storage.ToolGrant{}
	for _, g := range grants {
		got[g.ToolName] = g
		if g.AgentID != butler.ID {
			t.Errorf("grant %q agent_id = %s, want butler %s", g.ToolName, g.AgentID, butler.ID)
		}
		if g.Config != nil {
			t.Errorf("grant %q carries config %q, want nil (no scope narrowing)", g.ToolName, g.Config)
		}
	}
	want := []string{"dice", "transcript_search", "kg_query", "recap"}
	if len(grants) != len(want) {
		t.Fatalf("auto-Butler has %d grants, want %d (%v): %+v", len(grants), len(want), want, grants)
	}
	for _, name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("auto-Butler missing default grant %q; has %+v", name, grants)
		}
	}
}

// TestButlerKnowledgeGrantBackfillIdempotent pins the 00025 backfill's
// idempotence (ON CONFLICT DO NOTHING): re-running the two backfill INSERTs never
// errors and never duplicates a grant, so an existing Butler keeps exactly one
// row per Tool.
func TestButlerKnowledgeGrantBackfillIdempotent(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	butler, err := st.GetButler(ctx, campaignID)
	if err != nil {
		t.Fatalf("GetButler: %v", err)
	}

	// Re-run the migration's backfill statements: they must be no-ops now.
	for _, tool := range []string{"transcript_search", "kg_query", "recap"} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO tool_agent_grant (agent_id, tool_name)
			 SELECT id, $1 FROM agents WHERE agent_role = 'butler'
			 ON CONFLICT (agent_id, tool_name) DO NOTHING`, tool); err != nil {
			t.Fatalf("re-run backfill %q: %v", tool, err)
		}
	}

	grants, err := st.ListToolGrants(ctx, butler.ID)
	if err != nil {
		t.Fatalf("ListToolGrants(butler): %v", err)
	}
	if len(grants) != 4 {
		t.Fatalf("after re-running the backfill the Butler has %d grants, want 4 (no duplicates): %+v", len(grants), grants)
	}
}

// TestToolGrantRoundTrip round-trips a grant with a jsonb scope config and a
// nil-config grant, proves the per-grant config survives Postgres (AC4's
// persistence half), and that delete + Agent-cascade remove them.
func TestToolGrantRoundTrip(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	charID, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID: campaignID,
		Role:       storage.AgentRoleCharacter,
		Name:       "Bart",
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	// A grant WITH a scope config — the shape a future per-Agent-scoped Tool uses.
	scope := json.RawMessage(`{"scope":"self","topics":["rumors"]}`)
	if _, err := st.CreateToolGrant(ctx, storage.NewToolGrant{
		AgentID:  charID,
		ToolName: "remember_knowledge",
		Config:   scope,
	}); err != nil {
		t.Fatalf("CreateToolGrant (scoped): %v", err)
	}
	// A grant with NO config — the dice shape.
	if _, err := st.CreateToolGrant(ctx, storage.NewToolGrant{
		AgentID:  charID,
		ToolName: "dice",
	}); err != nil {
		t.Fatalf("CreateToolGrant (dice): %v", err)
	}

	grants, err := st.ListToolGrants(ctx, charID)
	if err != nil {
		t.Fatalf("ListToolGrants: %v", err)
	}
	if len(grants) != 2 {
		t.Fatalf("got %d grants, want 2", len(grants))
	}
	// ORDER BY tool_name: "dice" then "remember_knowledge".
	if grants[0].ToolName != "dice" || grants[1].ToolName != "remember_knowledge" {
		t.Fatalf("grant order = [%q %q], want [dice remember_knowledge]", grants[0].ToolName, grants[1].ToolName)
	}
	if grants[0].Config != nil {
		t.Errorf("dice grant config = %q, want nil", grants[0].Config)
	}
	// jsonb reserializes (whitespace/key order), so compare semantically.
	var got, want map[string]any
	if err := json.Unmarshal(grants[1].Config, &got); err != nil {
		t.Fatalf("scoped grant config not valid JSON: %v (%q)", err, grants[1].Config)
	}
	if err := json.Unmarshal(scope, &want); err != nil {
		t.Fatalf("want scope not valid JSON: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scoped grant config = %+v, want %+v", got, want)
	}

	// Deleting a present grant succeeds; deleting it again is ErrNotFound.
	if err := st.DeleteToolGrant(ctx, charID, "dice"); err != nil {
		t.Fatalf("DeleteToolGrant(dice): %v", err)
	}
	if err := st.DeleteToolGrant(ctx, charID, "dice"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("DeleteToolGrant(dice) second time = %v, want ErrNotFound", err)
	}
	remaining, err := st.ListToolGrants(ctx, charID)
	if err != nil {
		t.Fatalf("ListToolGrants after delete: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ToolName != "remember_knowledge" {
		t.Fatalf("after delete grants = %+v, want [remember_knowledge]", remaining)
	}

	// Deleting the Agent cascades its grants away (ON DELETE CASCADE) — no
	// explicit cleanup code.
	if err := st.DeleteAgent(ctx, campaignID, charID); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	cascaded, err := st.ListToolGrants(ctx, charID)
	if err != nil {
		t.Fatalf("ListToolGrants after agent delete: %v", err)
	}
	if len(cascaded) != 0 {
		t.Errorf("grants survived Agent delete: %+v (FK CASCADE not honored)", cascaded)
	}
}

// TestUpsertToolGrant covers the #117 grant-mutation path: UpsertToolGrant both
// GRANTS a Tool (inserts a row) and EDITS an existing grant's scope config
// (updates in place, no UNIQUE violation), so the RPC toggle-on and scope-edit
// share one storage call. The nil-config insert is the dice shape.
func TestUpsertToolGrant(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	charID, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID: campaignID,
		Role:       storage.AgentRoleCharacter,
		Name:       "Toggler",
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	// First upsert INSERTS the grant (no row existed) with no scope config.
	if err := st.UpsertToolGrant(ctx, storage.NewToolGrant{AgentID: charID, ToolName: "dice"}); err != nil {
		t.Fatalf("UpsertToolGrant (insert): %v", err)
	}
	grants, err := st.ListToolGrants(ctx, charID)
	if err != nil {
		t.Fatalf("ListToolGrants: %v", err)
	}
	if len(grants) != 1 || grants[0].ToolName != "dice" || grants[0].Config != nil {
		t.Fatalf("after insert = %+v, want exactly [dice] with nil config", grants)
	}

	// Second upsert of the SAME (agent, tool) must UPDATE in place, not violate
	// the UNIQUE index — this is the RPC's scope-edit path.
	scope := json.RawMessage(`{"scope":"self"}`)
	if err := st.UpsertToolGrant(ctx, storage.NewToolGrant{AgentID: charID, ToolName: "dice", Config: scope}); err != nil {
		t.Fatalf("UpsertToolGrant (update): %v", err)
	}
	grants, err = st.ListToolGrants(ctx, charID)
	if err != nil {
		t.Fatalf("ListToolGrants after update: %v", err)
	}
	if len(grants) != 1 {
		t.Fatalf("upsert created a duplicate row: %+v", grants)
	}
	var got map[string]any
	if err := json.Unmarshal(grants[0].Config, &got); err != nil {
		t.Fatalf("updated config not valid JSON: %v (%q)", err, grants[0].Config)
	}
	if got["scope"] != "self" {
		t.Errorf("scope config not updated: %+v", got)
	}

	// Upserting nil config back clears the scope (SQL NULL) in place.
	if err := st.UpsertToolGrant(ctx, storage.NewToolGrant{AgentID: charID, ToolName: "dice"}); err != nil {
		t.Fatalf("UpsertToolGrant (clear): %v", err)
	}
	grants, err = st.ListToolGrants(ctx, charID)
	if err != nil {
		t.Fatalf("ListToolGrants after clear: %v", err)
	}
	if len(grants) != 1 || grants[0].Config != nil {
		t.Fatalf("after clear = %+v, want [dice] with nil config", grants)
	}
}

// TestToolGrantUniquePerTool asserts the UNIQUE(agent_id, tool_name) index: an
// Agent grants a Tool at most once (ADR-0029).
func TestToolGrantUniquePerTool(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	charID, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID: campaignID,
		Role:       storage.AgentRoleCharacter,
		Name:       "Mira",
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	if _, err := st.CreateToolGrant(ctx, storage.NewToolGrant{AgentID: charID, ToolName: "dice"}); err != nil {
		t.Fatalf("first CreateToolGrant: %v", err)
	}
	if _, err := st.CreateToolGrant(ctx, storage.NewToolGrant{AgentID: charID, ToolName: "dice"}); err == nil {
		t.Fatal("second identical grant succeeded; UNIQUE(agent_id, tool_name) not enforced")
	}
}

// TestListToolGrantsEmpty: an Agent with no grant rows yields an empty slice
// (least-privilege) — the hydration path builds a GrantSet that declares no Tool.
func TestListToolGrantsEmpty(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	charID, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID: campaignID,
		Role:       storage.AgentRoleCharacter,
		Name:       "Silent",
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	grants, err := st.ListToolGrants(ctx, charID)
	if err != nil {
		t.Fatalf("ListToolGrants: %v", err)
	}
	if len(grants) != 0 {
		t.Errorf("agent with no grant rows has %d grants, want 0", len(grants))
	}
	// A random (nonexistent) agent id is also just empty, not an error.
	if g, err := st.ListToolGrants(ctx, uuid.New()); err != nil || len(g) != 0 {
		t.Errorf("ListToolGrants(random) = (%+v, %v), want ([], nil)", g, err)
	}
}

// TestBackfillGrantsExistingButler covers the 00013 backfill statement in
// isolation: a Butler that already existed BEFORE the migration must gain its
// dice grant when 00013 applies. Every other test creates Butlers via the
// already-extended trigger and TestMigrateUpDown runs an empty DB, so this is
// the only coverage of the backfill. It migrates through 00012 (the schema
// before tool_agent_grant exists, where the unextended 00002 trigger inserts a
// grantless Butler), creates a Campaign to get such a Butler, then applies 00013
// and asserts the backfill granted it dice.
func TestBackfillGrantsExistingButler(t *testing.T) {
	dsn := startPostgres(t)
	db := openSQL(t, dsn)
	pool := openPool(t, dsn)
	ctx := context.Background()

	provider, err := storage.NewMigrationProvider(db)
	if err != nil {
		t.Fatalf("NewMigrationProvider: %v", err)
	}

	// Apply through 00012 only — tool_agent_grant does not exist yet, and the
	// auto-Butler trigger here is the unextended 00002 version (Butler, no grant).
	if _, err := provider.UpTo(ctx, 12); err != nil {
		t.Fatalf("migrate up to 00012: %v", err)
	}

	st := storage.New(pool)
	tenantID, err := st.CreateTenant(ctx, "Backfill Co")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	campaignID, err := st.CreateCampaign(ctx, storage.NewCampaign{TenantID: tenantID, Name: "Old Campaign"})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	butler, err := st.GetButler(ctx, campaignID)
	if err != nil {
		t.Fatalf("GetButler at v12: %v", err)
	}

	// Apply 00013: creates the table, extends the trigger, AND backfills the
	// pre-existing Butler created above.
	if _, err := provider.UpTo(ctx, 13); err != nil {
		t.Fatalf("migrate up to 00013: %v", err)
	}

	grants, err := st.ListToolGrants(ctx, butler.ID)
	if err != nil {
		t.Fatalf("ListToolGrants after backfill: %v", err)
	}
	if len(grants) != 1 || grants[0].ToolName != "dice" {
		t.Fatalf("pre-existing Butler has grants %+v after backfill, want exactly [dice]", grants)
	}
	if grants[0].Config != nil {
		t.Errorf("backfilled dice grant config = %q, want nil", grants[0].Config)
	}
}

// TestBackfillGrantsKnowledgeToolsExistingButler covers the 00025 backfill in
// isolation (#299): a Butler that already existed with only its dice grant must
// gain `transcript_search` + `kg_query` when 00025 applies. It migrates through
// 00024 (before this migration; the trigger there is the 00013 dice-only body),
// creates a Campaign to get a dice-only Butler, then applies 00025 and asserts the
// backfill added the two knowledge grants.
func TestBackfillGrantsKnowledgeToolsExistingButler(t *testing.T) {
	dsn := startPostgres(t)
	db := openSQL(t, dsn)
	pool := openPool(t, dsn)
	ctx := context.Background()

	provider, err := storage.NewMigrationProvider(db)
	if err != nil {
		t.Fatalf("NewMigrationProvider: %v", err)
	}

	// Apply through 00024 only: the auto-Butler trigger here is the 00013 dice-only
	// body, so the Campaign's Butler comes with exactly [dice].
	if _, err := provider.UpTo(ctx, 24); err != nil {
		t.Fatalf("migrate up to 00024: %v", err)
	}

	st := storage.New(pool)
	tenantID, err := st.CreateTenant(ctx, "Knowledge Co")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	campaignID, err := st.CreateCampaign(ctx, storage.NewCampaign{TenantID: tenantID, Name: "Old Campaign"})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	butler, err := st.GetButler(ctx, campaignID)
	if err != nil {
		t.Fatalf("GetButler at v24: %v", err)
	}

	// Apply 00025: extends the trigger AND backfills the pre-existing Butler.
	if _, err := provider.UpTo(ctx, 25); err != nil {
		t.Fatalf("migrate up to 00025: %v", err)
	}

	grants, err := st.ListToolGrants(ctx, butler.ID)
	if err != nil {
		t.Fatalf("ListToolGrants after backfill: %v", err)
	}
	names := map[string]bool{}
	for _, g := range grants {
		names[g.ToolName] = true
	}
	for _, want := range []string{"dice", "transcript_search", "kg_query"} {
		if !names[want] {
			t.Errorf("pre-existing Butler missing %q after 00025 backfill; has %+v", want, grants)
		}
	}
	if len(grants) != 3 {
		t.Fatalf("pre-existing Butler has %d grants after backfill, want 3: %+v", len(grants), grants)
	}
}
