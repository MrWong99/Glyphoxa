//go:build integration

// This drives the CampaignService Edge + agent-link handlers end to end over
// Connect-JSON against a real *storage.Store (testcontainers Postgres), proving
// the wire → store → wire round-trip for #132: typed Edge create/list/delete,
// the object-side validity matrix, the cross-campaign impossibility, and the
// NPC-Node ↔ Agent link. Tag-isolated behind `integration`.

package rpc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

func TestKGEdge_Integration(t *testing.T) {
	dsn := startPostgres(t)
	store, tenantID, campaignID := seedStoreTenant(t, dsn)
	ctx := context.Background()

	mk := func(typ storage.KGNodeType, name string) storage.KGNode {
		n, err := store.CreateNode(ctx, storage.NewKGNode{CampaignID: campaignID, Type: typ, Name: name})
		if err != nil {
			t.Fatalf("CreateNode %s: %v", name, err)
		}
		return n
	}
	aldric := mk(storage.KGNodeCharacter, "Aldric")
	barrow := mk(storage.KGNodeLocation, "Barrow")
	fac := mk(storage.KGNodeFaction, "The Cult")
	bartNode := mk(storage.KGNodeNPC, "Bart the Innkeeper")

	agentID, err := store.CreateAgent(ctx, storage.NewAgent{
		CampaignID: campaignID, Role: storage.AgentRoleCharacter, Name: "Bart",
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	// Pin the active campaign to the seeded one for the whole test: it later
	// creates a SECOND campaign (the cross-campaign endpoint check), which would
	// otherwise become the most-recent → active and (correctly, #342) scope the
	// edge delete away from the seeded campaign the edge lives in.
	server := rpc.NewCampaignServer(store)
	server.SetSessions(liveMgr(campaignID))
	mux := http.NewServeMux()
	mux.Handle(server.Handler(connect.WithInterceptors(tenantOperatorInterceptor(tenantID, "operator-kg"))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := managementv1connect.NewCampaignServiceClient(
		http.DefaultClient, srv.URL, connect.WithProtoJSON(),
	)

	// Create a valid typed Edge: Aldric resides_in Barrow.
	created, err := client.CreateEdge(ctx, connect.NewRequest(&managementv1.CreateEdgeRequest{
		FromNodeId: aldric.ID.String(), ToNodeId: barrow.ID.String(),
		EdgeType: managementv1.EdgeType_EDGE_TYPE_RESIDES_IN,
	}))
	if err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}
	edgeID := created.Msg.GetEdge().GetId()
	if edgeID == "" {
		t.Fatalf("created edge has no id: %+v", created.Msg.GetEdge())
	}

	// The object-side matrix rejects resides_in → Faction.
	_, err = client.CreateEdge(ctx, connect.NewRequest(&managementv1.CreateEdgeRequest{
		FromNodeId: aldric.ID.String(), ToNodeId: fac.ID.String(),
		EdgeType: managementv1.EdgeType_EDGE_TYPE_RESIDES_IN,
	}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("invalid-combo edge code = %v, want InvalidArgument", got)
	}

	// ListNodeEdges: Aldric has one outgoing (to Barrow, joined) and no incoming.
	edges, err := client.ListNodeEdges(ctx, connect.NewRequest(&managementv1.ListNodeEdgesRequest{
		NodeId: aldric.ID.String(),
	}))
	if err != nil {
		t.Fatalf("ListNodeEdges: %v", err)
	}
	out := edges.Msg.GetOutgoing()
	if len(out) != 1 || len(edges.Msg.GetIncoming()) != 0 {
		t.Fatalf("split = %d out / %d in, want 1/0", len(out), len(edges.Msg.GetIncoming()))
	}
	if out[0].GetToNodeName() != "Barrow" || out[0].GetToNodeType() != managementv1.NodeType_NODE_TYPE_LOCATION {
		t.Errorf("outgoing endpoint not joined: %+v", out[0])
	}

	// Link the NPC Node to the Agent; a non-NPC Node is rejected.
	linked, err := client.SetNodeAgent(ctx, connect.NewRequest(&managementv1.SetNodeAgentRequest{
		NodeId: bartNode.ID.String(), AgentId: agentID.String(),
	}))
	if err != nil {
		t.Fatalf("SetNodeAgent link: %v", err)
	}
	if linked.Msg.GetNode().GetAgentId() != agentID.String() {
		t.Errorf("agent_id not persisted: %q", linked.Msg.GetNode().GetAgentId())
	}
	_, err = client.SetNodeAgent(ctx, connect.NewRequest(&managementv1.SetNodeAgentRequest{
		NodeId: aldric.ID.String(), AgentId: agentID.String(),
	}))
	if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
		t.Errorf("link on non-NPC node code = %v, want InvalidArgument", got)
	}

	// A cross-campaign endpoint makes the Edge impossible (CodeNotFound).
	seeded, err := store.GetActiveCampaign(ctx) // only "Lost Mine" exists → its tenant
	if err != nil {
		t.Fatalf("GetActiveCampaign: %v", err)
	}
	otherCampaign, err := store.CreateCampaign(ctx, storage.NewCampaign{
		TenantID: seeded.TenantID, Name: "Other", System: "dnd5e", Language: "en",
	})
	if err != nil {
		t.Fatalf("CreateCampaign other: %v", err)
	}
	foreign, err := store.CreateNode(ctx, storage.NewKGNode{CampaignID: otherCampaign, Type: storage.KGNodeLocation, Name: "Foreign Keep"})
	if err != nil {
		t.Fatalf("CreateNode foreign: %v", err)
	}
	_, err = client.CreateEdge(ctx, connect.NewRequest(&managementv1.CreateEdgeRequest{
		FromNodeId: aldric.ID.String(), ToNodeId: foreign.ID.String(),
		EdgeType: managementv1.EdgeType_EDGE_TYPE_RESIDES_IN,
	}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("cross-campaign edge code = %v, want NotFound", got)
	}

	// Delete the Edge; Aldric's outgoing list is empty; a repeat delete is NotFound.
	if _, err := client.DeleteEdge(ctx, connect.NewRequest(&managementv1.DeleteEdgeRequest{Id: edgeID})); err != nil {
		t.Fatalf("DeleteEdge: %v", err)
	}
	edges, err = client.ListNodeEdges(ctx, connect.NewRequest(&managementv1.ListNodeEdgesRequest{NodeId: aldric.ID.String()}))
	if err != nil {
		t.Fatalf("ListNodeEdges after delete: %v", err)
	}
	if len(edges.Msg.GetOutgoing()) != 0 {
		t.Errorf("edge not deleted: %+v", edges.Msg.GetOutgoing())
	}
	_, err = client.DeleteEdge(ctx, connect.NewRequest(&managementv1.DeleteEdgeRequest{Id: edgeID}))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Errorf("repeat DeleteEdge code = %v, want NotFound", got)
	}
}
