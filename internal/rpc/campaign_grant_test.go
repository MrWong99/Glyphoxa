package rpc_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// registerAgent seeds a bare Agent row so the grant handlers' existence pre-check
// (issue #215) passes; these tests only need the Agent to exist, not its fields.
func registerAgent(store *fakeCampaignStore) uuid.UUID {
	id := uuid.New()
	store.agents[id] = storage.Agent{ID: id}
	return id
}

// TestListToolGrants_CatalogWithState is the AC1 bar over the handler: the list
// shows EVERY built-in Tool with the Agent's current grant state. The catalog is
// the built-in Registry (Name-sorted): dice + the two knowledge Tools kg_query
// and transcript_search (#296). An ungranted Agent sees them present-but-off;
// granting flips one on. kg_query advertises scope support (own_node vs
// campaign, ADR-0029) so the grant editor renders its scope UI; dice and
// transcript_search do not.
func TestListToolGrants_CatalogWithState(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	agentID := registerAgent(store)
	client := crudClient(t, store)

	// No grant rows: every built-in is listed but not granted.
	resp, err := client.ListToolGrants(context.Background(),
		connect.NewRequest(&managementv1.ListToolGrantsRequest{AgentId: agentID.String()}))
	if err != nil {
		t.Fatalf("ListToolGrants: %v", err)
	}
	grants := resp.Msg.GetGrants()
	wantNames := []string{"dice", "kg_query", "transcript_search"} // Name-sorted catalog
	if len(grants) != len(wantNames) {
		t.Fatalf("catalog = %+v, want %v", grants, wantNames)
	}
	scopeByName := map[string]bool{}
	for i, g := range grants {
		if g.GetToolName() != wantNames[i] {
			t.Errorf("catalog[%d] = %q, want %q", i, g.GetToolName(), wantNames[i])
		}
		if g.GetGranted() {
			t.Errorf("%s should be ungranted for a fresh Agent", g.GetToolName())
		}
		if g.GetDescription() == "" {
			t.Errorf("%s entry should carry the Tool description as hint text", g.GetToolName())
		}
		scopeByName[g.GetToolName()] = g.GetSupportsScope()
	}
	if !scopeByName["kg_query"] {
		t.Error("kg_query must advertise scope support (ADR-0029 own_node vs campaign)")
	}
	if scopeByName["dice"] {
		t.Error("dice must not advertise scope support")
	}
	if scopeByName["transcript_search"] {
		t.Error("transcript_search must not advertise scope support (campaign-scoped for all)")
	}

	// Grant dice, then it lists as granted.
	if _, err := client.UpdateToolGrant(context.Background(),
		connect.NewRequest(&managementv1.UpdateToolGrantRequest{
			AgentId: agentID.String(), ToolName: "dice", Granted: true,
		})); err != nil {
		t.Fatalf("UpdateToolGrant(on): %v", err)
	}
	resp, err = client.ListToolGrants(context.Background(),
		connect.NewRequest(&managementv1.ListToolGrantsRequest{AgentId: agentID.String()}))
	if err != nil {
		t.Fatalf("ListToolGrants (after grant): %v", err)
	}
	if !resp.Msg.GetGrants()[0].GetGranted() {
		t.Error("dice should list as granted after UpdateToolGrant(on)")
	}
}

// TestToolGrant_GhostAgentNotFound: operating on an agent_id that doesn't exist
// (a stale second tab after the Agent was deleted) is CodeNotFound on BOTH the
// list read and the grant write. The handler pre-checks Agent existence
// (GetAgent) rather than letting the write surface the tool_agent_grant FK
// violation as a 500 (issue #215), mirroring the sibling UpdateAgent/DeleteAgent
// missing-Agent mapping.
func TestToolGrant_GhostAgentNotFound(t *testing.T) {
	t.Parallel()
	client := crudClient(t, newFakeStore()) // empty store → no Agent exists
	ghost := uuid.New().String()

	_, listErr := client.ListToolGrants(context.Background(),
		connect.NewRequest(&managementv1.ListToolGrantsRequest{AgentId: ghost}))
	if got := connect.CodeOf(listErr); got != connect.CodeNotFound {
		t.Errorf("ListToolGrants(ghost) code = %v, want NotFound", got)
	}

	_, updErr := client.UpdateToolGrant(context.Background(),
		connect.NewRequest(&managementv1.UpdateToolGrantRequest{AgentId: ghost, ToolName: "dice", Granted: true}))
	if got := connect.CodeOf(updErr); got != connect.CodeNotFound {
		t.Errorf("UpdateToolGrant(ghost) code = %v, want NotFound", got)
	}
}

// TestUpdateToolGrant_CrossCampaignIsNotFound is #342: an operator whose active
// campaign is A cannot grant/revoke Tools on an Agent that belongs to campaign B —
// the write path requires the Agent to be in the active campaign, so a mismatch is
// CodeNotFound and nothing is written.
func TestUpdateToolGrant_CrossCampaignIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	activeID := uuid.New()
	store.campaign = storage.Campaign{ID: activeID}
	// The Agent exists, but in ANOTHER campaign.
	foreignAgent := uuid.New()
	store.agents[foreignAgent] = storage.Agent{ID: foreignAgent, CampaignID: uuid.New()}
	client := crudClient(t, store)

	_, err := client.UpdateToolGrant(context.Background(),
		connect.NewRequest(&managementv1.UpdateToolGrantRequest{
			AgentId: foreignAgent.String(), ToolName: "dice", Granted: true,
		}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound (cross-campaign agent)", got)
	}
}

// TestUpdateToolGrant_NoActiveCampaignIsNotFound is #342: without an active
// campaign the grant write cannot resolve an owning campaign and is CodeNotFound.
func TestUpdateToolGrant_NoActiveCampaignIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campErr = storage.ErrNotFound
	agentID := registerAgent(store)
	client := crudClient(t, store)

	_, err := client.UpdateToolGrant(context.Background(),
		connect.NewRequest(&managementv1.UpdateToolGrantRequest{
			AgentId: agentID.String(), ToolName: "dice", Granted: true,
		}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound (no active campaign)", got)
	}
}

// TestListToolGrants_CrossCampaignIsNotFound is #356: a READ is scoped too. An
// operator whose active campaign is A cannot read the grant config of an Agent
// that belongs to campaign B by id — the list requires the Agent to be in the
// active campaign, so a mismatch is CodeNotFound, not a leaked catalog.
func TestListToolGrants_CrossCampaignIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	activeID := uuid.New()
	store.campaign = storage.Campaign{ID: activeID}
	// The Agent exists, but in ANOTHER campaign.
	foreignAgent := uuid.New()
	store.agents[foreignAgent] = storage.Agent{ID: foreignAgent, CampaignID: uuid.New()}
	client := crudClient(t, store)

	_, err := client.ListToolGrants(context.Background(),
		connect.NewRequest(&managementv1.ListToolGrantsRequest{AgentId: foreignAgent.String()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound (cross-campaign agent)", got)
	}
}

// TestListToolGrants_NoActiveCampaignIsNotFound is #356: without an active
// campaign the read cannot resolve an owning campaign and is CodeNotFound.
func TestListToolGrants_NoActiveCampaignIsNotFound(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	store.campErr = storage.ErrNotFound
	agentID := registerAgent(store)
	client := crudClient(t, store)

	_, err := client.ListToolGrants(context.Background(),
		connect.NewRequest(&managementv1.ListToolGrantsRequest{AgentId: agentID.String()}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("code = %v, want NotFound (no active campaign)", got)
	}
}

func TestListToolGrants_InvalidAgentID(t *testing.T) {
	t.Parallel()
	client := crudClient(t, newFakeStore())
	_, err := client.ListToolGrants(context.Background(),
		connect.NewRequest(&managementv1.ListToolGrantsRequest{AgentId: "not-a-uuid"}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

// TestUpdateToolGrant_ToggleRoundTrips: grant on persists a row and the response
// reflects granted=true; toggling off removes it and the next list drops it — the
// AC2 persist-and-reload contract at the handler seam.
func TestUpdateToolGrant_ToggleRoundTrips(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	agentID := registerAgent(store)
	client := crudClient(t, store)

	on, err := client.UpdateToolGrant(context.Background(),
		connect.NewRequest(&managementv1.UpdateToolGrantRequest{
			AgentId: agentID.String(), ToolName: "dice", Granted: true,
		}))
	if err != nil {
		t.Fatalf("UpdateToolGrant(on): %v", err)
	}
	if g := on.Msg.GetGrant(); !g.GetGranted() || g.GetToolName() != "dice" {
		t.Fatalf("on response = %+v, want granted dice", g)
	}
	if _, ok := store.grants[agentID]["dice"]; !ok {
		t.Error("dice grant row not persisted to the store")
	}

	off, err := client.UpdateToolGrant(context.Background(),
		connect.NewRequest(&managementv1.UpdateToolGrantRequest{
			AgentId: agentID.String(), ToolName: "dice", Granted: false,
		}))
	if err != nil {
		t.Fatalf("UpdateToolGrant(off): %v", err)
	}
	if off.Msg.GetGrant().GetGranted() {
		t.Error("off response should report granted=false")
	}
	if _, ok := store.grants[agentID]["dice"]; ok {
		t.Error("dice grant row should be removed after toggle off")
	}
}

// TestUpdateToolGrant_ConfigRoundTrips proves the per-grant scope config
// round-trips through UpdateToolGrant → ListToolGrants even though dice's UI shows
// no scope editor (AC3): the API is scope-capable regardless of the Tool's editor.
func TestUpdateToolGrant_ConfigRoundTrips(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	agentID := registerAgent(store)
	client := crudClient(t, store)

	scope := `{"scope":"self"}`
	resp, err := client.UpdateToolGrant(context.Background(),
		connect.NewRequest(&managementv1.UpdateToolGrantRequest{
			AgentId: agentID.String(), ToolName: "dice", Granted: true, Config: scope,
		}))
	if err != nil {
		t.Fatalf("UpdateToolGrant(config): %v", err)
	}
	assertScopeSelf(t, resp.Msg.GetGrant().GetConfig())

	list, err := client.ListToolGrants(context.Background(),
		connect.NewRequest(&managementv1.ListToolGrantsRequest{AgentId: agentID.String()}))
	if err != nil {
		t.Fatalf("ListToolGrants: %v", err)
	}
	assertScopeSelf(t, list.Msg.GetGrants()[0].GetConfig())
}

// assertScopeSelf checks a config JSON string carries scope=self (semantic, since
// jsonb/JSON reserialization may reorder or respace).
func assertScopeSelf(t *testing.T, cfg string) {
	t.Helper()
	if cfg == "" {
		t.Fatal("config round-trip lost the scope blob")
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(cfg), &got); err != nil {
		t.Fatalf("config not valid JSON: %v (%q)", err, cfg)
	}
	if got["scope"] != "self" {
		t.Errorf("config = %+v, want scope=self", got)
	}
}

func TestUpdateToolGrant_UnknownToolRejected(t *testing.T) {
	t.Parallel()
	client := crudClient(t, newFakeStore())
	_, err := client.UpdateToolGrant(context.Background(),
		connect.NewRequest(&managementv1.UpdateToolGrantRequest{
			AgentId: uuid.New().String(), ToolName: "not_a_tool", Granted: true,
		}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument for an unregistered tool", got)
	}
}

func TestUpdateToolGrant_InvalidConfigRejected(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	agentID := registerAgent(store)
	client := crudClient(t, store)
	_, err := client.UpdateToolGrant(context.Background(),
		connect.NewRequest(&managementv1.UpdateToolGrantRequest{
			AgentId: agentID.String(), ToolName: "dice", Granted: true, Config: "{not json",
		}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument for malformed config", got)
	}
}

func TestUpdateToolGrant_InvalidAgentID(t *testing.T) {
	t.Parallel()
	client := crudClient(t, newFakeStore())
	_, err := client.UpdateToolGrant(context.Background(),
		connect.NewRequest(&managementv1.UpdateToolGrantRequest{AgentId: "nope", ToolName: "dice", Granted: true}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", got)
	}
}

// TestUpdateToolGrant_RevokeIsIdempotent: revoking a Tool the Agent never held
// succeeds (the desired end state already holds), so a double-toggle-off never
// errors.
func TestUpdateToolGrant_RevokeIsIdempotent(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	agentID := registerAgent(store)
	client := crudClient(t, store)
	if _, err := client.UpdateToolGrant(context.Background(),
		connect.NewRequest(&managementv1.UpdateToolGrantRequest{
			AgentId: agentID.String(), ToolName: "dice", Granted: false,
		})); err != nil {
		t.Errorf("revoking an ungranted Tool should succeed, got %v", err)
	}
}

// denyAuth rejects every session — mounts NewAuthInterceptor into a deny-all gate.
type denyAuth struct{}

func (denyAuth) AuthenticateSession(context.Context, string) (storage.User, error) {
	return storage.User{}, storage.ErrNotFound
}

// TestToolGrant_AuthGatesBothLikeSiblings (AC5, auth half): with the auth
// interceptor mounted and no valid session, BOTH grant RPCs — the read and the
// write — are rejected Unauthenticated, exactly like the sibling UpdateAgent
// mutation. The whole management API is gated (ADR-0016); the list read being
// side-effect-free exempts it from CSRF, not from auth.
func TestToolGrant_AuthGatesBothLikeSiblings(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.Handle(rpc.NewCampaignServer(newFakeStore()).Handler(
		connect.WithInterceptors(auth.NewAuthInterceptor(denyAuth{})),
	))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := managementv1connect.NewCampaignServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())
	ctx := context.Background()

	_, listErr := client.ListToolGrants(ctx, connect.NewRequest(&managementv1.ListToolGrantsRequest{AgentId: uuid.New().String()}))
	_, updateErr := client.UpdateToolGrant(ctx, connect.NewRequest(&managementv1.UpdateToolGrantRequest{AgentId: uuid.New().String(), ToolName: "dice", Granted: true}))
	_, siblingErr := client.UpdateAgent(ctx, connect.NewRequest(&managementv1.UpdateAgentRequest{Id: uuid.New().String()}))

	for name, err := range map[string]error{"ListToolGrants": listErr, "UpdateToolGrant": updateErr, "UpdateAgent(sibling)": siblingErr} {
		if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
			t.Errorf("%s code = %v, want Unauthenticated (whole API is auth-gated)", name, got)
		}
	}
}

// TestToolGrant_CSRFGuardsMutationNotRead (AC5, CSRF half): with the CSRF
// interceptor mounted and no double-submit token, the state-changing
// UpdateToolGrant is rejected PermissionDenied exactly like the sibling
// UpdateAgent mutation, while the side-effect-free ListToolGrants (NO_SIDE_EFFECTS)
// is exempt and reaches the handler.
func TestToolGrant_CSRFGuardsMutationNotRead(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.Handle(rpc.NewCampaignServer(newFakeStore()).Handler(
		connect.WithInterceptors(auth.NewCSRFInterceptor()),
	))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := managementv1connect.NewCampaignServiceClient(http.DefaultClient, srv.URL, connect.WithProtoJSON())
	ctx := context.Background()

	// The write is CSRF-guarded — no token → PermissionDenied, like the sibling.
	_, updateErr := client.UpdateToolGrant(ctx, connect.NewRequest(&managementv1.UpdateToolGrantRequest{AgentId: uuid.New().String(), ToolName: "dice", Granted: true}))
	if got := connect.CodeOf(updateErr); got != connect.CodePermissionDenied {
		t.Errorf("UpdateToolGrant code = %v, want PermissionDenied (CSRF-guarded mutation)", got)
	}
	_, siblingErr := client.UpdateAgent(ctx, connect.NewRequest(&managementv1.UpdateAgentRequest{Id: uuid.New().String()}))
	if got := connect.CodeOf(siblingErr); got != connect.CodePermissionDenied {
		t.Errorf("UpdateAgent(sibling) code = %v, want PermissionDenied — parity check", got)
	}

	// The read is exempt — no token still reaches the handler (returns its own
	// result, never PermissionDenied).
	if _, err := client.ListToolGrants(ctx, connect.NewRequest(&managementv1.ListToolGrantsRequest{AgentId: uuid.New().String()})); connect.CodeOf(err) == connect.CodePermissionDenied {
		t.Error("ListToolGrants must be CSRF-exempt (NO_SIDE_EFFECTS read)")
	}
}
