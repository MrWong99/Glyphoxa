//go:build integration

// This drives the CampaignService management RPCs (#264) end to end over
// Connect-JSON against a real *storage.Store (testcontainers Postgres): list,
// create (auto-Butler + dice grant invariant, ADR-0009), the opaque
// name/system/language update, and the durable Active Campaign selection shared
// with `/glyphoxa use` (migration 00014). Only the SQL-shaped behavior lives
// here (#445) — the triggers, seed migrations, ORDER BY, and the shared
// selection row. The live-first precedence is unit coverage
// (TestSetActiveCampaignLiveFirstWins), and the #268 language-edit/voice
// separation is covered twice elsewhere: structurally at the handler layer (the
// management store slice cannot even name an Agent write) and at the SQL layer
// by internal/storage's TestUpdateCampaignLanguageLeavesAgentVoiceUntouched.
// Tag-isolated behind `integration`; reuses startPostgres/seedStore from
// campaign_integration_test.go.

package rpc_test

import (
	"context"
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

// mgmtIntegrationClient mounts a CampaignServer over a real store behind an
// interceptor that injects the resolved tenant + operator (ADR-0039), optionally
// wiring a live Voice Session source so the live-first rule can be exercised.
func mgmtIntegrationClient(t *testing.T, store *storage.Store, tenantID uuid.UUID, discordUserID string, sessions *fakeSessionManager) managementv1connect.CampaignServiceClient {
	t.Helper()
	srv := rpc.NewCampaignServer(store)
	if sessions != nil {
		srv.SetSessions(sessions)
	}
	inject := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			ctx = auth.WithTenant(ctx, tenantID)
			ctx = auth.WithUser(ctx, storage.User{ID: uuid.New(), DiscordUserID: discordUserID})
			return next(ctx, req)
		}
	})
	mux := http.NewServeMux()
	mux.Handle(srv.Handler(connect.WithInterceptors(inject)))
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return managementv1connect.NewCampaignServiceClient(
		http.DefaultClient, s.URL, connect.WithProtoJSON(),
	)
}

func TestCampaignManagement_Integration(t *testing.T) {
	dsn := startPostgres(t)
	store, seededID := seedStore(t, dsn) // seeds a "Lost Mine" campaign (+ its auto-Butler)
	ctx := context.Background()

	// The tenant is the seeded campaign's; the management RPCs resolve it from the
	// context, so inject it (and an operator) via the interceptor.
	seeded, err := store.GetCampaign(ctx, seededID)
	if err != nil {
		t.Fatalf("GetCampaign(seeded): %v", err)
	}
	const operator = "operator-264"
	client := mgmtIntegrationClient(t, store, seeded.TenantID, operator, nil)

	// --- Create two campaigns; the tenant comes from the context, never the wire.
	created1, err := client.CreateCampaign(ctx, connect.NewRequest(&managementv1.CreateCampaignRequest{
		Name: "Zeta Reach", System: "pf2e", Language: "de",
	}))
	if err != nil {
		t.Fatalf("CreateCampaign(Zeta): %v", err)
	}
	zeta := created1.Msg.GetCampaign()
	if zeta.GetName() != "Zeta Reach" || zeta.GetSystem() != "pf2e" || zeta.GetLanguage() != "de" {
		t.Errorf("created Zeta fields wrong: %+v", zeta)
	}
	if zeta.GetTenantId() != seeded.TenantID.String() {
		t.Errorf("created Zeta tenant = %q, want the server-resolved %q", zeta.GetTenantId(), seeded.TenantID)
	}

	created2, err := client.CreateCampaign(ctx, connect.NewRequest(&managementv1.CreateCampaignRequest{
		Name: "Alpha Quest",
	}))
	if err != nil {
		t.Fatalf("CreateCampaign(Alpha): %v", err)
	}
	alpha := created2.Msg.GetCampaign()

	// --- Create fires the ADR-0009 auto-Butler trigger: the new campaign has
	// exactly one Butler, and it carries the dice grant (migration 00013).
	butler, err := store.GetButler(ctx, uuid.MustParse(zeta.GetId()))
	if err != nil {
		t.Fatalf("auto-Butler missing for created campaign: %v", err)
	}
	if butler.Role != storage.AgentRoleButler || !butler.AddressOnly {
		t.Errorf("auto-Butler wrong: %+v", butler)
	}
	grants, err := store.ListToolGrants(ctx, butler.ID)
	if err != nil {
		t.Fatalf("ListToolGrants(butler): %v", err)
	}
	gotGrants := map[string]bool{}
	for _, g := range grants {
		gotGrants[g.ToolName] = true
	}
	for _, want := range []string{"dice", "transcript_search", "kg_query", "recap"} {
		if !gotGrants[want] {
			t.Errorf("auto-Butler missing default grant %q; has %+v", want, grants)
		}
	}
	if len(grants) != 4 {
		t.Errorf("auto-Butler grants = %+v, want dice + transcript_search + kg_query + recap (#372)", grants)
	}

	// --- List returns every campaign name-ordered: Alpha Quest, Lost Mine, Zeta Reach.
	list, err := client.ListCampaigns(ctx, connect.NewRequest(&managementv1.ListCampaignsRequest{}))
	if err != nil {
		t.Fatalf("ListCampaigns: %v", err)
	}
	names := make([]string, 0, len(list.Msg.GetCampaigns()))
	for _, c := range list.Msg.GetCampaigns() {
		names = append(names, c.GetName())
	}
	want := []string{"Alpha Quest", "Lost Mine", "Zeta Reach"}
	if len(names) != len(want) {
		t.Fatalf("ListCampaigns = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("ListCampaigns order = %v, want %v", names, want)
		}
	}

	// --- Update writes the three columns opaquely (arbitrary free-text accepted).
	const opaqueSystem = "Homebrew: 3d6-in-order ⚔️"
	const opaqueLang = "Middle Draconic (invented)"
	upd, err := client.UpdateCampaign(ctx, connect.NewRequest(&managementv1.UpdateCampaignRequest{
		Id: zeta.GetId(), Name: "Zeta Reach II", System: opaqueSystem, Language: opaqueLang,
	}))
	if err != nil {
		t.Fatalf("UpdateCampaign: %v", err)
	}
	if upd.Msg.GetCampaign().GetName() != "Zeta Reach II" ||
		upd.Msg.GetCampaign().GetSystem() != opaqueSystem ||
		upd.Msg.GetCampaign().GetLanguage() != opaqueLang {
		t.Errorf("update did not write opaquely: %+v", upd.Msg.GetCampaign())
	}
	// The write persists — a re-read reflects it.
	reread, err := store.GetCampaign(ctx, uuid.MustParse(zeta.GetId()))
	if err != nil || reread.System != opaqueSystem {
		t.Errorf("re-read after update = %+v, %v", reread, err)
	}
	// An unknown id is CodeNotFound.
	_, err = client.UpdateCampaign(ctx, connect.NewRequest(&managementv1.UpdateCampaignRequest{
		Id: uuid.New().String(), Name: "ghost",
	}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("UpdateCampaign(unknown) code = %v, want NotFound", got)
	}

	// --- SetActiveCampaign to Alpha; with no live session the resolved Active
	// Campaign flips to it across the header, roster, and node reads.
	setResp, err := client.SetActiveCampaign(ctx, connect.NewRequest(&managementv1.SetActiveCampaignRequest{
		CampaignId: alpha.GetId(),
	}))
	if err != nil {
		t.Fatalf("SetActiveCampaign(Alpha): %v", err)
	}
	if setResp.Msg.GetCampaign().GetId() != alpha.GetId() {
		t.Errorf("SetActiveCampaign resolved = %s, want Alpha %s", setResp.Msg.GetCampaign().GetId(), alpha.GetId())
	}

	active, err := client.GetActiveCampaign(ctx, connect.NewRequest(&managementv1.GetActiveCampaignRequest{}))
	if err != nil {
		t.Fatalf("GetActiveCampaign: %v", err)
	}
	if active.Msg.GetCampaign().GetId() != alpha.GetId() {
		t.Errorf("GetActiveCampaign = %s, want the selected Alpha %s (not the newest)", active.Msg.GetCampaign().GetId(), alpha.GetId())
	}
	roster, err := client.GetCampaignRoster(ctx, connect.NewRequest(&managementv1.GetCampaignRosterRequest{}))
	if err != nil {
		t.Fatalf("GetCampaignRoster: %v", err)
	}
	if roster.Msg.GetCampaign().GetId() != alpha.GetId() {
		t.Errorf("GetCampaignRoster = %s, want Alpha %s", roster.Msg.GetCampaign().GetId(), alpha.GetId())
	}
	// ListNodes resolves the same selection: an entry created now lands in Alpha
	// and lists back, proving the read scoped to the durable selection.
	if _, err := client.CreateNode(ctx, connect.NewRequest(&managementv1.CreateNodeRequest{
		NodeType: managementv1.NodeType_NODE_TYPE_NOTE, Name: "Alpha-scoped note",
	})); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	nodes, err := client.ListNodes(ctx, connect.NewRequest(&managementv1.ListNodesRequest{}))
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes.Msg.GetNodes()) != 1 || nodes.Msg.GetNodes()[0].GetName() != "Alpha-scoped note" {
		t.Errorf("ListNodes did not resolve the Alpha selection: %+v", nodes.Msg.GetNodes())
	}
	// The durable selection is the SAME row /glyphoxa use reads — both surfaces in
	// lockstep (migration 00014).
	forUser, err := store.GetActiveCampaignForUser(ctx, operator)
	if err != nil || forUser.ID.String() != alpha.GetId() {
		t.Errorf("GetActiveCampaignForUser = %+v, %v, want Alpha %s", forUser, err, alpha.GetId())
	}

	// An unknown campaign_id is CodeNotFound and persists nothing.
	_, err = client.SetActiveCampaign(ctx, connect.NewRequest(&managementv1.SetActiveCampaignRequest{
		CampaignId: uuid.New().String(),
	}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("SetActiveCampaign(unknown) code = %v, want NotFound", got)
	}
}
