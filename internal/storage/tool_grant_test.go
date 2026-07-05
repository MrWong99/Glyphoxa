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

// TestButlerDiceGrantSeeded is the AC1 bar: the migration seeds the auto-created
// Butler's dice grant. seedCampaign creates a Campaign, whose auto-Butler
// trigger (extended in 00013) also inserts the Butler's dice grant — so a fresh
// Butler comes with exactly one grant, `dice`, carrying no scope config.
func TestButlerDiceGrantSeeded(t *testing.T) {
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
	if len(grants) != 1 {
		t.Fatalf("auto-Butler has %d grants, want 1 (dice): %+v", len(grants), grants)
	}
	if grants[0].ToolName != "dice" {
		t.Errorf("Butler grant = %q, want dice", grants[0].ToolName)
	}
	if grants[0].Config != nil {
		t.Errorf("dice grant carries config %q, want nil (no scope narrowing)", grants[0].Config)
	}
	if grants[0].AgentID != butler.ID {
		t.Errorf("grant agent_id = %s, want butler %s", grants[0].AgentID, butler.ID)
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
	if err := st.DeleteAgent(ctx, charID); err != nil {
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
