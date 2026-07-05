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

// TestListToolGrants_CatalogWithState is the AC1 bar over the handler: the list
// shows EVERY built-in Tool (dice today) with the Agent's current grant state.
// An ungranted Agent sees dice present-but-off; granting flips it on. Because the
// catalog is the built-in Registry, an Agent with no rows still lists dice.
func TestListToolGrants_CatalogWithState(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	agentID := uuid.New()
	client := crudClient(t, store)

	// No grant rows: dice is listed but not granted, and advertises no scope.
	resp, err := client.ListToolGrants(context.Background(),
		connect.NewRequest(&managementv1.ListToolGrantsRequest{AgentId: agentID.String()}))
	if err != nil {
		t.Fatalf("ListToolGrants: %v", err)
	}
	grants := resp.Msg.GetGrants()
	if len(grants) != 1 || grants[0].GetToolName() != "dice" {
		t.Fatalf("catalog = %+v, want exactly [dice]", grants)
	}
	if grants[0].GetGranted() {
		t.Error("dice should be ungranted for a fresh Agent")
	}
	if grants[0].GetSupportsScope() {
		t.Error("dice must not advertise scope support")
	}
	if grants[0].GetDescription() == "" {
		t.Error("dice entry should carry the Tool description as hint text")
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
	agentID := uuid.New()
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
	agentID := uuid.New()
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
	client := crudClient(t, newFakeStore())
	_, err := client.UpdateToolGrant(context.Background(),
		connect.NewRequest(&managementv1.UpdateToolGrantRequest{
			AgentId: uuid.New().String(), ToolName: "dice", Granted: true, Config: "{not json",
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
	client := crudClient(t, newFakeStore())
	if _, err := client.UpdateToolGrant(context.Background(),
		connect.NewRequest(&managementv1.UpdateToolGrantRequest{
			AgentId: uuid.New().String(), ToolName: "dice", Granted: false,
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
