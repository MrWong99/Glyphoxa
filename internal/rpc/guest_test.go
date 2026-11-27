package rpc_test

// Campaign Guest handler hardening (ADR-0065). The policy layer's guest gate
// decides whether a guest may call a procedure at all (internal/auth); these
// tests cover what a handler a guest DID reach may hand back. A guest's
// request carries the HOST Tenant, so the failure mode under test everywhere
// is the same: reading or changing something outside their one Campaign, or
// something their rights do not cover.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1/managementv1connect"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/planchat"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/search"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

var guestUser = storage.User{ID: uuid.New(), DiscordUserID: "888", Name: "Mika"}

// asGuest is the context the policy builds for a Campaign Guest: their user,
// the HOST tenant, and the guest scope.
func asGuest(tenantID, campaignID uuid.UUID, rights ...auth.GuestRight) context.Context {
	ctx := auth.WithUser(context.Background(), guestUser)
	ctx = auth.WithTenant(ctx, tenantID)
	return auth.WithGuest(ctx, auth.GuestScope{CampaignID: campaignID, Rights: auth.NewRightSet(rights...)})
}

// asGM is the context of the Tenant's own operator.
func asGM(tenantID uuid.UUID) context.Context {
	ctx := auth.WithUser(context.Background(), storage.User{ID: uuid.New(), DiscordUserID: "42", Name: "GM Gil"})
	return auth.WithTenant(ctx, tenantID)
}

// fakeSecrets is the private-node reader.
type fakeSecrets struct {
	private  map[uuid.UUID]struct{}
	err      error
	calls    int
	campaign uuid.UUID
}

func secretsOf(ids ...uuid.UUID) *fakeSecrets {
	f := &fakeSecrets{private: map[uuid.UUID]struct{}{}}
	for _, id := range ids {
		f.private[id] = struct{}{}
	}
	return f
}

func (f *fakeSecrets) PrivateNodeIDs(_ context.Context, campaignID uuid.UUID) (map[uuid.UUID]struct{}, error) {
	f.calls++
	f.campaign = campaignID
	return f.private, f.err
}

// hostWorld is a host Tenant with the guest's Campaign and a SECOND Campaign
// of the same Tenant, which the fake resolver would hand to a GM.
type hostWorld struct {
	tenant        uuid.UUID
	guestCampaign storage.Campaign
	otherCampaign storage.Campaign
	active        *fakeActive
}

func newHostWorld() hostWorld {
	tenant := uuid.New()
	w := hostWorld{
		tenant:        tenant,
		guestCampaign: storage.Campaign{ID: uuid.New(), TenantID: tenant, Name: "Saltmarsh"},
		otherCampaign: storage.Campaign{ID: uuid.New(), TenantID: tenant, Name: "The GM's other table"},
		active:        newFakeActive(),
	}
	// Everything the three-step walk could resolve points at the OTHER
	// campaign: the durable selection and the most-recent fallback.
	w.active.campaign = w.otherCampaign
	w.active.forUser = w.otherCampaign
	w.active.campaignsByID[w.guestCampaign.ID] = w.guestCampaign
	w.active.campaignsByID[w.otherCampaign.ID] = w.otherCampaign
	return w
}

// TestGuest_ActiveCampaignIsPinned: for a guest the Active Campaign is the
// guest Campaign on every CampaignService surface — the live session, the
// durable selection and the most-recent fallback all point elsewhere and none
// of them is followed.
func TestGuest_ActiveCampaignIsPinned(t *testing.T) {
	t.Parallel()
	w := newHostWorld()
	nodes := newFakeKGNodeStore()
	nodes.fakeActive = w.active
	srv := rpc.NewCampaignServerWith(rpc.CampaignStores{Active: w.active, KGNodes: nodes, Secrets: secretsOf()})
	// ANOTHER campaign of the host tenant is live right now.
	srv.SetSessions(liveMgr(w.otherCampaign.ID))

	ctx := asGuest(w.tenant, w.guestCampaign.ID, auth.RightWikiRead)
	got, err := srv.GetActiveCampaign(ctx, connect.NewRequest(&managementv1.GetActiveCampaignRequest{}))
	if err != nil || got.Msg.GetCampaign().GetId() != w.guestCampaign.ID.String() {
		t.Fatalf("guest's active campaign = %v, %v; want the guest campaign %s", got, err, w.guestCampaign.ID)
	}
	if _, err := srv.ListNodes(ctx, connect.NewRequest(&managementv1.ListNodesRequest{})); err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if nodes.listNodesCampaign != w.guestCampaign.ID {
		t.Fatalf("ListNodes read campaign %s, want the guest campaign %s", nodes.listNodesCampaign, w.guestCampaign.ID)
	}

	// The same server resolves the live campaign for the GM — the fixture
	// really does point everywhere else.
	gm, err := srv.GetActiveCampaign(asGM(w.tenant), connect.NewRequest(&managementv1.GetActiveCampaignRequest{}))
	if err != nil || gm.Msg.GetCampaign().GetId() != w.otherCampaign.ID.String() {
		t.Fatalf("GM's active campaign = %v, %v; want the live one", gm, err)
	}

	// A guest scope naming a campaign that is not in the resolved host tenant
	// resolves to nothing — never to one of the host's campaigns.
	stray := asGuest(uuid.New(), w.guestCampaign.ID, auth.RightWikiRead)
	if _, err := srv.GetActiveCampaign(stray, connect.NewRequest(&managementv1.GetActiveCampaignRequest{})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("guest scope outside the tenant: err = %v, want CodeNotFound", err)
	}
	gone := asGuest(w.tenant, uuid.New(), auth.RightWikiRead)
	if _, err := srv.GetActiveCampaign(gone, connect.NewRequest(&managementv1.GetActiveCampaignRequest{})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("guest scope naming a vanished campaign: err = %v, want CodeNotFound", err)
	}
}

// TestGuest_NodesWithheldWithoutSecrets: ListNodes drops gm_private Nodes and
// private Aspects for a guest without wiki.secrets, and nobody else.
func TestGuest_NodesWithheldWithoutSecrets(t *testing.T) {
	t.Parallel()
	w := newHostWorld()
	store := newFakeKGNodeStore()
	store.fakeActive = w.active
	publicID, secretID := uuid.New(), uuid.New()
	store.nodes = []storage.KGNode{
		{ID: publicID, CampaignID: w.guestCampaign.ID, Type: storage.KGNodeNPC, Name: "Bart", Body: "Innkeeper.",
			Aspects: []storage.KGNodeAspect{
				{ID: uuid.New(), Key: "Trade", Value: "Runs the Empty Net"},
				{ID: uuid.New(), Key: "Secret", Value: "Took the smugglers' bribe", GMPrivate: true},
			}},
		{ID: secretID, CampaignID: w.guestCampaign.ID, Type: storage.KGNodeNPC, Name: "The cult leader", GMPrivate: true,
			Aspects: []storage.KGNodeAspect{{ID: uuid.New(), Key: "Public-looking", Value: "but on a private entry"}}},
	}
	srv := rpc.NewCampaignServerWith(rpc.CampaignStores{Active: w.active, KGNodes: store})
	list := func(ctx context.Context) []*managementv1.Node {
		t.Helper()
		resp, err := srv.ListNodes(ctx, connect.NewRequest(&managementv1.ListNodesRequest{}))
		if err != nil {
			t.Fatalf("ListNodes: %v", err)
		}
		return resp.Msg.GetNodes()
	}

	got := list(asGuest(w.tenant, w.guestCampaign.ID, auth.RightWikiRead))
	if len(got) != 1 || got[0].GetId() != publicID.String() {
		t.Fatalf("guest without secrets got %d nodes (%v), want only the public one", len(got), got)
	}
	if len(got[0].GetAspects()) != 1 || got[0].GetAspects()[0].GetKey() != "Trade" {
		t.Errorf("guest without secrets got aspects %v, want only the public one", got[0].GetAspects())
	}

	for name, ctx := range map[string]context.Context{
		"a guest with wiki.secrets": asGuest(w.tenant, w.guestCampaign.ID, auth.RightWikiRead, auth.RightWikiSecrets),
		"a guest with wiki.edit":    asGuest(w.tenant, w.guestCampaign.ID, auth.RightWikiEdit),
		"a guest with planning.use": asGuest(w.tenant, w.guestCampaign.ID, auth.RightPlanningUse),
		"the GM":                    asGM(w.tenant),
	} {
		got := list(ctx)
		if len(got) != 2 || len(got[0].GetAspects()) != 2 {
			t.Errorf("%s got %d nodes / %d aspects on the first, want everything", name, len(got), len(got[0].GetAspects()))
		}
	}
	// The stored fixture was not mutated by the redaction.
	if len(store.nodes[0].Aspects) != 2 {
		t.Fatal("redaction mutated the store's own slice")
	}
}

// TestGuest_SearchNodesUsesThePublicSearch: filtering a GM search's results
// afterwards would still leak a secret through the fact of the hit, so a guest
// without wiki.secrets must be served by the public search itself.
func TestGuest_SearchNodesUsesThePublicSearch(t *testing.T) {
	t.Parallel()
	w := newHostWorld()
	bart := storage.KGNode{ID: uuid.New(), Type: storage.KGNodeNPC, Name: "Bart"}
	secret := storage.KGNode{ID: uuid.New(), Type: storage.KGNodeNPC, Name: "The cult leader", GMPrivate: true}
	mk := func() (*rpc.CampaignServer, *fakeKGNodeStore) {
		store := newFakeKGNodeStore()
		store.fakeActive = w.active
		// The GM search finds Bart through his PRIVATE aspect, plus the secret
		// node; the public search finds nothing for this query.
		store.searchResults = []storage.KGNode{bart, secret}
		store.publicSearchResults = nil
		return rpc.NewCampaignServerWith(rpc.CampaignStores{Active: w.active, KGNodes: store}), store
	}
	req := func() *connect.Request[managementv1.SearchNodesRequest] {
		return connect.NewRequest(&managementv1.SearchNodesRequest{Query: "bribe"})
	}

	srv, store := mk()
	resp, err := srv.SearchNodes(asGuest(w.tenant, w.guestCampaign.ID, auth.RightWikiRead), req())
	if err != nil {
		t.Fatalf("SearchNodes: %v", err)
	}
	if len(resp.Msg.GetNodes()) != 0 {
		t.Errorf("guest without secrets got %v for a query that only matches secrets", resp.Msg.GetNodes())
	}
	if store.searchCalls != 0 || store.publicSearchCalls != 1 || store.searchNodesCampaign != w.guestCampaign.ID {
		t.Errorf("GM search calls = %d, public = %d, campaign = %s; want only the public search, on the guest campaign",
			store.searchCalls, store.publicSearchCalls, store.searchNodesCampaign)
	}

	// Belt and braces: even a public search that wrongly returned a private
	// node would not get it past the handler.
	srv, store = mk()
	store.publicSearchResults = []storage.KGNode{bart, secret}
	resp, _ = srv.SearchNodes(asGuest(w.tenant, w.guestCampaign.ID, auth.RightWikiRead), req())
	if len(resp.Msg.GetNodes()) != 1 || resp.Msg.GetNodes()[0].GetId() != bart.ID.String() {
		t.Errorf("handler let a private node through: %v", resp.Msg.GetNodes())
	}

	for name, ctx := range map[string]context.Context{
		"a guest with wiki.secrets": asGuest(w.tenant, w.guestCampaign.ID, auth.RightWikiRead, auth.RightWikiSecrets),
		"the GM":                    asGM(w.tenant),
	} {
		srv, store := mk()
		resp, err := srv.SearchNodes(ctx, req())
		if err != nil || len(resp.Msg.GetNodes()) != 2 || store.searchCalls != 1 || store.publicSearchCalls != 0 {
			t.Errorf("%s: %d nodes, GM search calls %d, public %d, err %v; want the GM search and both hits",
				name, len(resp.Msg.GetNodes()), store.searchCalls, store.publicSearchCalls, err)
		}
	}
}

// TestGuest_GraphWithheldWithoutSecrets: no private Node, no Edge touching
// one, and no aspect count that betrays private Aspects.
func TestGuest_GraphWithheldWithoutSecrets(t *testing.T) {
	t.Parallel()
	w := newHostWorld()
	store := newFakeKGGraphStore()
	store.fakeActive = w.active
	bart, town, cult := uuid.New(), uuid.New(), uuid.New()
	store.graphNodes = []storage.KGGraphNode{
		{ID: bart, Type: storage.KGNodeNPC, Name: "Bart", AspectCount: 3, PublicAspectCount: 1},
		{ID: town, Type: storage.KGNodeLocation, Name: "Saltmarsh", AspectCount: 2, PublicAspectCount: 2},
		{ID: cult, Type: storage.KGNodeFaction, Name: "The Drowned Cult", GMPrivate: true, AspectCount: 4},
	}
	publicEdge := uuid.New()
	store.edges = []storage.KGEdge{
		{ID: publicEdge, FromNodeID: bart, ToNodeID: town, Type: storage.KGEdgeResidesIn},
		{ID: uuid.New(), FromNodeID: bart, ToNodeID: cult, Type: storage.KGEdgeMemberOf, Note: "secretly"},
		{ID: uuid.New(), FromNodeID: cult, ToNodeID: town, Type: storage.KGEdgeResidesIn},
	}
	srv := rpc.NewCampaignServerWith(rpc.CampaignStores{Active: w.active, KGGraph: store})

	resp, err := srv.GetKnowledgeGraph(asGuest(w.tenant, w.guestCampaign.ID, auth.RightWikiRead),
		connect.NewRequest(&managementv1.GetKnowledgeGraphRequest{}))
	if err != nil {
		t.Fatalf("GetKnowledgeGraph: %v", err)
	}
	if len(resp.Msg.GetNodes()) != 2 {
		t.Fatalf("guest got %d nodes, want the 2 public ones", len(resp.Msg.GetNodes()))
	}
	for _, n := range resp.Msg.GetNodes() {
		if n.GetId() == cult.String() || n.GetGmPrivate() {
			t.Errorf("guest got the private node %+v", n)
		}
		if n.GetId() == bart.String() && (n.GetAspectCount() != 1 || n.GetPublicAspectCount() != 1) {
			t.Errorf("Bart's aspect counts = %d/%d, want 1/1 (the private two must not be countable)",
				n.GetAspectCount(), n.GetPublicAspectCount())
		}
	}
	if len(resp.Msg.GetEdges()) != 1 || resp.Msg.GetEdges()[0].GetId() != publicEdge.String() {
		t.Errorf("guest got edges %v, want only the edge between two public nodes", resp.Msg.GetEdges())
	}
	if store.graphCampaign != w.guestCampaign.ID || store.edgeCampaign != w.guestCampaign.ID {
		t.Errorf("graph read campaigns %s/%s, want the guest campaign", store.graphCampaign, store.edgeCampaign)
	}

	full, err := srv.GetKnowledgeGraph(asGuest(w.tenant, w.guestCampaign.ID, auth.RightWikiEdit),
		connect.NewRequest(&managementv1.GetKnowledgeGraphRequest{}))
	if err != nil || len(full.Msg.GetNodes()) != 3 || len(full.Msg.GetEdges()) != 3 || full.Msg.GetNodes()[0].GetAspectCount() != 3 {
		t.Errorf("guest with secrets got %d nodes / %d edges, err %v; want the whole graph", len(full.Msg.GetNodes()), len(full.Msg.GetEdges()), err)
	}
}

// TestGuest_EdgesTagsBoardsWithheldWithoutSecrets covers everything that
// merely POINTS at a withheld Node.
func TestGuest_EdgesTagsBoardsWithheldWithoutSecrets(t *testing.T) {
	t.Parallel()
	w := newHostWorld()
	bart, town, cult := uuid.New(), uuid.New(), uuid.New()
	noSecrets := asGuest(w.tenant, w.guestCampaign.ID, auth.RightWikiRead)
	withSecrets := asGuest(w.tenant, w.guestCampaign.ID, auth.RightWikiRead, auth.RightWikiSecrets)

	edges := newFakeKGEdgeStore()
	edges.fakeActive = w.active
	edge := func(from, to uuid.UUID, fromName, toName string) storage.KGEdgeWithNodes {
		return storage.KGEdgeWithNodes{
			KGEdge:   storage.KGEdge{ID: uuid.New(), FromNodeID: from, ToNodeID: to, Type: storage.KGEdgeKnows},
			FromName: fromName, ToName: toName,
		}
	}
	edges.edgesOut = []storage.KGEdgeWithNodes{edge(bart, town, "Bart", "Saltmarsh"), edge(bart, cult, "Bart", "The Drowned Cult")}
	edges.edgesIn = []storage.KGEdgeWithNodes{edge(cult, bart, "The Drowned Cult", "Bart")}

	organize := newFakeOrganizeStore()
	organize.fakeActive = w.active
	organize.tags = map[uuid.UUID][]string{bart: {"harbour"}, cult: {"villains"}}
	organize.boards = []storage.KGBoard{{ID: uuid.New(), CampaignID: w.guestCampaign.ID, Name: "Session 12", NodeIDs: []uuid.UUID{bart, cult, town}}}

	secrets := secretsOf(cult)
	srv := rpc.NewCampaignServerWith(rpc.CampaignStores{Active: w.active, KGEdges: edges, Organize: organize, Secrets: secrets})

	t.Run("edges", func(t *testing.T) {
		resp, err := srv.ListNodeEdges(noSecrets, connect.NewRequest(&managementv1.ListNodeEdgesRequest{NodeId: bart.String()}))
		if err != nil {
			t.Fatalf("ListNodeEdges: %v", err)
		}
		if len(resp.Msg.GetOutgoing()) != 1 || resp.Msg.GetOutgoing()[0].GetToNodeName() != "Saltmarsh" || len(resp.Msg.GetIncoming()) != 0 {
			t.Errorf("guest got outgoing %v incoming %v; want only the edge to the public node",
				resp.Msg.GetOutgoing(), resp.Msg.GetIncoming())
		}
		if secrets.campaign != w.guestCampaign.ID {
			t.Errorf("private set read for campaign %s, want the guest campaign", secrets.campaign)
		}
		// Asking for the withheld node's own edges: it does not exist.
		_, err = srv.ListNodeEdges(noSecrets, connect.NewRequest(&managementv1.ListNodeEdgesRequest{NodeId: cult.String()}))
		if connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("edges of a withheld node: err = %v, want CodeNotFound", err)
		}
		full, err := srv.ListNodeEdges(withSecrets, connect.NewRequest(&managementv1.ListNodeEdgesRequest{NodeId: bart.String()}))
		if err != nil || len(full.Msg.GetOutgoing()) != 2 || len(full.Msg.GetIncoming()) != 1 {
			t.Errorf("guest with secrets got %v, %v; want every edge", full, err)
		}
	})

	t.Run("tags", func(t *testing.T) {
		resp, err := srv.GetCampaignTags(noSecrets, connect.NewRequest(&managementv1.GetCampaignTagsRequest{}))
		if err != nil {
			t.Fatalf("GetCampaignTags: %v", err)
		}
		if len(resp.Msg.GetEntries()) != 1 || resp.Msg.GetEntries()[0].GetNodeId() != bart.String() {
			t.Errorf("guest got tag entries %v, want only the public entry's", resp.Msg.GetEntries())
		}
		full, _ := srv.GetCampaignTags(asGM(w.tenant), connect.NewRequest(&managementv1.GetCampaignTagsRequest{}))
		if len(full.Msg.GetEntries()) != 2 {
			t.Errorf("the GM got %d tag entries, want 2", len(full.Msg.GetEntries()))
		}
	})

	t.Run("boards", func(t *testing.T) {
		resp, err := srv.ListBoards(noSecrets, connect.NewRequest(&managementv1.ListBoardsRequest{}))
		if err != nil {
			t.Fatalf("ListBoards: %v", err)
		}
		want := []string{bart.String(), town.String()}
		if len(resp.Msg.GetBoards()) != 1 || !slices.Equal(resp.Msg.GetBoards()[0].GetNodeIds(), want) {
			t.Errorf("guest got boards %v, want the board listing only %v", resp.Msg.GetBoards(), want)
		}
		if len(organize.boards[0].NodeIDs) != 3 {
			t.Fatal("redaction mutated the store's board")
		}
		full, _ := srv.ListBoards(withSecrets, connect.NewRequest(&managementv1.ListBoardsRequest{}))
		if len(full.Msg.GetBoards()[0].GetNodeIds()) != 3 {
			t.Errorf("guest with secrets got %v, want all three entries", full.Msg.GetBoards()[0].GetNodeIds())
		}
	})

	t.Run("fails closed when the private set cannot be read", func(t *testing.T) {
		for name, s := range map[string]rpc.CampaignStores{
			"not wired": {Active: w.active, KGEdges: edges, Organize: organize},
			"erroring":  {Active: w.active, KGEdges: edges, Organize: organize, Secrets: &fakeSecrets{err: errors.New("db down")}},
		} {
			srv := rpc.NewCampaignServerWith(s)
			if _, err := srv.ListNodeEdges(noSecrets, connect.NewRequest(&managementv1.ListNodeEdgesRequest{NodeId: bart.String()})); connect.CodeOf(err) != connect.CodeInternal {
				t.Errorf("%s: ListNodeEdges err = %v, want CodeInternal (never an unfiltered answer)", name, err)
			}
			if _, err := srv.GetCampaignTags(noSecrets, connect.NewRequest(&managementv1.GetCampaignTagsRequest{})); connect.CodeOf(err) != connect.CodeInternal {
				t.Errorf("%s: GetCampaignTags err = %v, want CodeInternal", name, err)
			}
			if _, err := srv.ListBoards(noSecrets, connect.NewRequest(&managementv1.ListBoardsRequest{})); connect.CodeOf(err) != connect.CodeInternal {
				t.Errorf("%s: ListBoards err = %v, want CodeInternal", name, err)
			}
			// The GM needs no private set and is unaffected.
			if _, err := srv.ListBoards(asGM(w.tenant), connect.NewRequest(&managementv1.ListBoardsRequest{})); err != nil {
				t.Errorf("%s: the GM's ListBoards failed: %v", name, err)
			}
		}
	})
}

// fakeAppearances is the Appearances read.
type fakeAppearances struct {
	hits  []storage.AppearanceHit
	calls int
}

func (f *fakeAppearances) ListNodeAppearances(context.Context, uuid.UUID, uuid.UUID, int) ([]storage.AppearanceHit, error) {
	f.calls++
	return f.hits, nil
}

// TestGuest_AppearancesNeedTranscripts: an appearance quotes a transcript
// line, so wiki.read alone returns none — and so does a withheld entry.
func TestGuest_AppearancesNeedTranscripts(t *testing.T) {
	t.Parallel()
	w := newHostWorld()
	bart, cult := uuid.New(), uuid.New()
	mk := func() (*rpc.CampaignServer, *fakeAppearances) {
		store := &fakeAppearances{hits: []storage.AppearanceHit{{VoiceSessionID: uuid.New(), LineID: "l-1", Who: "Rin", Text: "We should ask Bart about the cult."}}}
		return rpc.NewCampaignServerWith(rpc.CampaignStores{Active: w.active, KGAppearances: store, Secrets: secretsOf(cult)}), store
	}
	call := func(srv *rpc.CampaignServer, ctx context.Context, node uuid.UUID) int {
		t.Helper()
		resp, err := srv.ListNodeAppearances(ctx, connect.NewRequest(&managementv1.ListNodeAppearancesRequest{NodeId: node.String()}))
		if err != nil {
			t.Fatalf("ListNodeAppearances: %v", err)
		}
		return len(resp.Msg.GetAppearances())
	}

	srv, store := mk()
	if n := call(srv, asGuest(w.tenant, w.guestCampaign.ID, auth.RightWikiRead, auth.RightWikiSecrets), bart); n != 0 || store.calls != 0 {
		t.Errorf("guest without transcripts.read got %d appearances (store read %d times); want none, unread", n, store.calls)
	}
	// planning.use implies the wiki rights but NOT transcripts.
	srv, store = mk()
	if n := call(srv, asGuest(w.tenant, w.guestCampaign.ID, auth.RightPlanningUse), bart); n != 0 || store.calls != 0 {
		t.Errorf("guest with planning.use only got %d appearances; want none", n)
	}
	srv, store = mk()
	if n := call(srv, asGuest(w.tenant, w.guestCampaign.ID, auth.RightWikiRead, auth.RightTranscriptsRead), cult); n != 0 || store.calls != 0 {
		t.Errorf("guest without secrets got %d appearances of a withheld entry (store read %d times); want none", n, store.calls)
	}
	srv, _ = mk()
	if n := call(srv, asGuest(w.tenant, w.guestCampaign.ID, auth.RightWikiRead, auth.RightTranscriptsRead), bart); n != 1 {
		t.Errorf("guest with wiki.read + transcripts.read got %d appearances of a public entry, want 1", n)
	}
	srv, _ = mk()
	if n := call(srv, asGM(w.tenant), cult); n != 1 {
		t.Errorf("the GM got %d appearances of their own private entry, want 1", n)
	}
}

// TestGuest_MapsWithheldWithoutSecrets: private Maps, private Pins, Pins on
// private entries, private ancestors and children, anchors on withheld entries
// and the unpinned tray.
func TestGuest_MapsWithheldWithoutSecrets(t *testing.T) {
	t.Parallel()
	w := newHostWorld()
	bart, cult := uuid.New(), uuid.New()
	world := storage.CampaignMap{ID: uuid.New(), Name: "A World", BlobKey: "k"}
	region := storage.CampaignMap{ID: uuid.New(), Name: "B Region", BlobKey: "k",
		ParentMapID: uuid.NullUUID{UUID: world.ID, Valid: true}, AnchorNodeID: uuid.NullUUID{UUID: cult, Valid: true}}
	lair := storage.CampaignMap{ID: uuid.New(), Name: "C Lair", BlobKey: "k", GMPrivate: true,
		ParentMapID: uuid.NullUUID{UUID: region.ID, Valid: true}}
	town := storage.CampaignMap{ID: uuid.New(), Name: "D Town", BlobKey: "k",
		ParentMapID: uuid.NullUUID{UUID: region.ID, Valid: true}, AnchorNodeID: uuid.NullUUID{UUID: bart, Valid: true}}

	store := newFakeMapStore()
	for _, m := range []storage.CampaignMap{world, region, lair, town} {
		store.maps[m.ID] = m
	}
	store.pins = []storage.MapPin{
		{ID: uuid.New(), MapID: region.ID, NodeID: bart, NodeName: "Bart", NodeType: storage.KGNodeNPC},
		{ID: uuid.New(), MapID: region.ID, NodeID: bart, NodeName: "Bart's stash", GMPrivate: true},
		{ID: uuid.New(), MapID: region.ID, NodeID: cult, NodeName: "The Drowned Cult", NodeGMPrivate: true},
	}
	store.ancestors = []storage.CampaignMap{world}
	store.unpinned = []storage.KGNode{
		{ID: uuid.New(), Name: "The Empty Net", Type: storage.KGNodeLocation, Body: "A harbour tavern."},
		{ID: uuid.New(), Name: "The altar", Type: storage.KGNodeLocation, Body: "Below the lighthouse.", GMPrivate: true},
	}
	srv := rpc.NewCampaignServerWith(rpc.CampaignStores{Active: w.active, Maps: store, Secrets: secretsOf(cult)})
	mapsOnly := asGuest(w.tenant, w.guestCampaign.ID, auth.RightMapsRead)

	listed, err := srv.ListMaps(mapsOnly, connect.NewRequest(&managementv1.ListMapsRequest{}))
	if err != nil {
		t.Fatalf("ListMaps: %v", err)
	}
	var names []string
	for _, m := range listed.Msg.GetMaps() {
		names = append(names, m.GetName())
		if m.GetId() == region.ID.String() && m.GetAnchorNodeId() != "" {
			t.Errorf("the region's anchor (%s) names a withheld entry and was not blanked", m.GetAnchorNodeId())
		}
		if m.GetId() == town.ID.String() && m.GetAnchorNodeId() != bart.String() {
			t.Errorf("the town's public anchor was blanked: %q", m.GetAnchorNodeId())
		}
	}
	if !slices.Equal(names, []string{"A World", "B Region", "D Town"}) {
		t.Errorf("guest without secrets listed %v, want every map but the private lair", names)
	}

	view, err := srv.GetMapView(mapsOnly, connect.NewRequest(&managementv1.GetMapViewRequest{Id: region.ID.String()}))
	if err != nil {
		t.Fatalf("GetMapView: %v", err)
	}
	v := view.Msg
	if v.GetMap().GetAnchorNodeId() != "" {
		t.Errorf("the viewed map's withheld anchor leaked: %q", v.GetMap().GetAnchorNodeId())
	}
	if len(v.GetPins()) != 1 || v.GetPins()[0].GetNodeName() != "Bart" {
		t.Errorf("guest got pins %v, want only the public pin on the public entry", v.GetPins())
	}
	if len(v.GetChildren()) != 1 || v.GetChildren()[0].GetId() != town.ID.String() {
		t.Errorf("guest got children %v, want only the public town", v.GetChildren())
	}
	if len(v.GetBreadcrumb()) != 1 {
		t.Errorf("breadcrumb = %v, want the public world", v.GetBreadcrumb())
	}
	// maps.read alone does not open wiki entries: the tray carries bodies.
	if len(v.GetUnpinned()) != 0 {
		t.Errorf("a maps-only guest got the unpinned tray: %v", v.GetUnpinned())
	}

	// With wiki.read the tray lists public entries only.
	withWiki, _ := srv.GetMapView(asGuest(w.tenant, w.guestCampaign.ID, auth.RightMapsRead, auth.RightWikiRead),
		connect.NewRequest(&managementv1.GetMapViewRequest{Id: region.ID.String()}))
	if len(withWiki.Msg.GetUnpinned()) != 1 || withWiki.Msg.GetUnpinned()[0].GetName() != "The Empty Net" {
		t.Errorf("guest with wiki.read got tray %v, want only the public entry", withWiki.Msg.GetUnpinned())
	}

	// A private map does not exist for them.
	if _, err := srv.GetMapView(mapsOnly, connect.NewRequest(&managementv1.GetMapViewRequest{Id: lair.ID.String()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("viewing a private map: err = %v, want CodeNotFound", err)
	}

	// With secrets — and for the GM — nothing is withheld.
	for name, ctx := range map[string]context.Context{
		"a guest with secrets": asGuest(w.tenant, w.guestCampaign.ID, auth.RightMapsRead, auth.RightWikiRead, auth.RightWikiSecrets),
		"the GM":               asGM(w.tenant),
	} {
		full, err := srv.GetMapView(ctx, connect.NewRequest(&managementv1.GetMapViewRequest{Id: region.ID.String()}))
		if err != nil || len(full.Msg.GetPins()) != 3 || len(full.Msg.GetChildren()) != 2 ||
			len(full.Msg.GetUnpinned()) != 2 || full.Msg.GetMap().GetAnchorNodeId() != cult.String() {
			t.Errorf("%s: view = %+v, err %v; want everything", name, full.Msg, err)
		}
		if _, err := srv.GetMapView(ctx, connect.NewRequest(&managementv1.GetMapViewRequest{Id: lair.ID.String()})); err != nil {
			t.Errorf("%s could not open the private map: %v", name, err)
		}
		all, _ := srv.ListMaps(ctx, connect.NewRequest(&managementv1.ListMapsRequest{}))
		if len(all.Msg.GetMaps()) != 4 {
			t.Errorf("%s listed %d maps, want 4", name, len(all.Msg.GetMaps()))
		}
	}
}

// TestGuest_PinWritesWithoutSecrets: a guest with maps.edit but not
// wiki.secrets cannot pin a private entry, pin onto a private map, touch a pin
// they cannot see, or mark a pin private.
func TestGuest_PinWritesWithoutSecrets(t *testing.T) {
	t.Parallel()
	w := newHostWorld()
	bart, cult := uuid.New(), uuid.New()
	region := storage.CampaignMap{ID: uuid.New(), Name: "Region", BlobKey: "k"}
	lair := storage.CampaignMap{ID: uuid.New(), Name: "Lair", BlobKey: "k", GMPrivate: true}
	visible := storage.MapPin{ID: uuid.New(), MapID: region.ID, NodeID: bart, NodeName: "Bart"}
	privatePin := storage.MapPin{ID: uuid.New(), MapID: region.ID, NodeID: bart, GMPrivate: true}
	onSecret := storage.MapPin{ID: uuid.New(), MapID: region.ID, NodeID: cult, NodeGMPrivate: true}
	inLair := storage.MapPin{ID: uuid.New(), MapID: lair.ID, NodeID: bart}

	mk := func() (*rpc.CampaignServer, *fakeMapStore) {
		store := newFakeMapStore()
		store.maps[region.ID] = region
		store.maps[lair.ID] = lair
		store.pins = []storage.MapPin{visible, privatePin, onSecret, inLair}
		return rpc.NewCampaignServerWith(rpc.CampaignStores{Active: w.active, Maps: store, Secrets: secretsOf(cult)}), store
	}
	editor := asGuest(w.tenant, w.guestCampaign.ID, auth.RightMapsEdit)

	t.Run("create", func(t *testing.T) {
		srv, store := mk()
		for name, req := range map[string]*managementv1.CreatePinRequest{
			"a private entry":    {MapId: region.ID.String(), NodeId: cult.String(), X: 0.5, Y: 0.5},
			"onto a private map": {MapId: lair.ID.String(), NodeId: bart.String(), X: 0.5, Y: 0.5},
			"an unknown map":     {MapId: uuid.NewString(), NodeId: bart.String(), X: 0.5, Y: 0.5},
		} {
			if _, err := srv.CreatePin(editor, connect.NewRequest(req)); connect.CodeOf(err) != connect.CodeNotFound {
				t.Errorf("pinning %s: err = %v, want CodeNotFound", name, err)
			}
		}
		if len(store.pinsCreated) != 0 {
			t.Fatalf("a refused pin reached the store: %+v", store.pinsCreated)
		}
		if _, err := srv.CreatePin(editor, connect.NewRequest(&managementv1.CreatePinRequest{
			MapId: region.ID.String(), NodeId: bart.String(), X: 0.2, Y: 0.3, GmPrivate: true,
		})); err != nil {
			t.Fatalf("pinning a public entry on a public map: %v", err)
		}
		if len(store.pinsCreated) != 1 || store.pinsCreated[0].GMPrivate || store.pinsCreated[0].CampaignID != w.guestCampaign.ID {
			t.Errorf("created pin = %+v; want one pin on the guest campaign, NOT private", store.pinsCreated)
		}
	})

	t.Run("update and delete", func(t *testing.T) {
		srv, store := mk()
		for name, pin := range map[string]storage.MapPin{
			"a private pin": privatePin, "a pin on a private entry": onSecret, "a pin on a private map": inLair,
			"an unknown pin": {ID: uuid.New()},
		} {
			_, err := srv.UpdatePin(editor, connect.NewRequest(&managementv1.UpdatePinRequest{Id: pin.ID.String(), X: 0.9, Y: 0.9}))
			if connect.CodeOf(err) != connect.CodeNotFound {
				t.Errorf("moving %s: err = %v, want CodeNotFound", name, err)
			}
			_, err = srv.DeletePin(editor, connect.NewRequest(&managementv1.DeletePinRequest{Id: pin.ID.String()}))
			if connect.CodeOf(err) != connect.CodeNotFound {
				t.Errorf("deleting %s: err = %v, want CodeNotFound", name, err)
			}
		}
		if len(store.pinsUpdated) != 0 || len(store.pinsDeleted) != 0 {
			t.Fatalf("a refused pin write reached the store: updated %v deleted %v", store.pinsUpdated, store.pinsDeleted)
		}
		if _, err := srv.UpdatePin(editor, connect.NewRequest(&managementv1.UpdatePinRequest{Id: visible.ID.String(), X: 0.4, Y: 0.4, GmPrivate: true})); err != nil {
			t.Fatalf("moving a visible pin: %v", err)
		}
		if len(store.pinsUpdated) != 1 || store.pinsUpdated[0].GMPrivate || store.pinsUpdated[0].CampaignID != w.guestCampaign.ID {
			t.Errorf("updated pin = %+v; want it moved on the guest campaign, not made private", store.pinsUpdated)
		}
		if _, err := srv.DeletePin(editor, connect.NewRequest(&managementv1.DeletePinRequest{Id: visible.ID.String()})); err != nil || len(store.pinsDeleted) != 1 {
			t.Errorf("deleting a visible pin: err %v, deletes %v", err, store.pinsDeleted)
		}
	})

	t.Run("with secrets, and for the GM, the writes pass through", func(t *testing.T) {
		for name, ctx := range map[string]context.Context{
			"a guest with secrets": asGuest(w.tenant, w.guestCampaign.ID, auth.RightMapsEdit, auth.RightWikiSecrets),
			"the GM":               asGM(w.tenant),
		} {
			srv, store := mk()
			if _, err := srv.CreatePin(ctx, connect.NewRequest(&managementv1.CreatePinRequest{
				MapId: lair.ID.String(), NodeId: cult.String(), X: 0.5, Y: 0.5, GmPrivate: true,
			})); err != nil || len(store.pinsCreated) != 1 || !store.pinsCreated[0].GMPrivate {
				t.Errorf("%s: private pin on a private map: err %v, created %+v", name, err, store.pinsCreated)
			}
			if _, err := srv.UpdatePin(ctx, connect.NewRequest(&managementv1.UpdatePinRequest{Id: onSecret.ID.String(), X: 0.1, Y: 0.1})); err != nil {
				t.Errorf("%s: moving a pin on a private entry: %v", name, err)
			}
			if _, err := srv.DeletePin(ctx, connect.NewRequest(&managementv1.DeletePinRequest{Id: privatePin.ID.String()})); err != nil {
				t.Errorf("%s: deleting a private pin: %v", name, err)
			}
		}
	})
}

// pinnedSessionStore is a SessionStore that tells Campaigns apart by id (the
// shared fake returns its one campaign for any id) and records which one the
// pinned resolution asked for.
type pinnedSessionStore struct {
	*fakeSessionStore
	byID  map[uuid.UUID]storage.Campaign
	asked []uuid.UUID
}

func (p *pinnedSessionStore) GetCampaignInTenant(_ context.Context, tenantID, id uuid.UUID) (storage.Campaign, error) {
	p.asked = append(p.asked, id)
	c, ok := p.byID[id]
	if !ok || c.TenantID != tenantID {
		return storage.Campaign{}, storage.ErrNotFound
	}
	return c, nil
}

// sessionWorld is a SessionServer whose host tenant is LIVE on the other
// campaign and whose durable/fallback resolution also points there.
func sessionWorld(t *testing.T) (hostWorld, *rpc.SessionServer, *pinnedSessionStore, *fakeHighlightStore) {
	t.Helper()
	w := newHostWorld()
	base := &fakeSessionStore{
		campaign: w.otherCampaign, forUser: w.otherCampaign, latestErr: storage.ErrNotFound,
		voiceSessions: map[uuid.UUID]storage.VoiceSession{},
	}
	store := &pinnedSessionStore{fakeSessionStore: base, byID: map[uuid.UUID]storage.Campaign{
		w.guestCampaign.ID: w.guestCampaign, w.otherCampaign.ID: w.otherCampaign,
	}}
	srv := rpc.NewSessionServer(liveMgr(w.otherCampaign.ID), store, nil, nil)
	highlights := newFakeHighlightStore(w.tenant)
	srv.SetHighlights(highlights, nil, nil)
	return w, srv, store, highlights
}

// TestGuest_SessionSurfacesArePinned: ListSessions, the transcript search and
// the plain-mount campaign resolver all scope to the guest Campaign even while
// another Campaign of the host Tenant is live.
func TestGuest_SessionSurfacesArePinned(t *testing.T) {
	t.Parallel()
	w, srv, store, _ := sessionWorld(t)
	ctx := asGuest(w.tenant, w.guestCampaign.ID, auth.RightTranscriptsRead)

	if _, err := srv.ListSessions(ctx, connect.NewRequest(&managementv1.ListSessionsRequest{})); err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if store.listCampaign != w.guestCampaign.ID {
		t.Fatalf("ListSessions listed campaign %s, want the guest campaign %s (the live one is %s)",
			store.listCampaign, w.guestCampaign.ID, w.otherCampaign.ID)
	}
	if _, err := srv.SearchTranscriptLines(ctx, connect.NewRequest(&managementv1.SearchTranscriptLinesRequest{Query: "dragon"})); err != nil {
		t.Fatalf("SearchTranscriptLines: %v", err)
	}
	if store.searchCampaign != w.guestCampaign.ID {
		t.Fatalf("transcript search ran on campaign %s, want the guest campaign", store.searchCampaign)
	}
	id, ok, err := srv.ResolveActiveCampaign(ctx)
	if err != nil || !ok || id != w.guestCampaign.ID {
		t.Fatalf("ResolveActiveCampaign (the byte mounts' resolver) = %s, %v, %v; want the guest campaign", id, ok, err)
	}
	for _, asked := range store.asked {
		if asked != w.guestCampaign.ID {
			t.Errorf("the resolution loaded campaign %s for a guest", asked)
		}
	}

	// The GM, on the same server, follows the live session.
	if id, _, _ := srv.ResolveActiveCampaign(asGM(w.tenant)); id != w.otherCampaign.ID {
		t.Fatalf("the GM resolved %s, want the live campaign", id)
	}

	// Without transcripts.read the line search returns nothing and reads nothing.
	before := store.searchCalls
	resp, err := srv.SearchTranscriptLines(asGuest(w.tenant, w.guestCampaign.ID, auth.RightHighlightsRead),
		connect.NewRequest(&managementv1.SearchTranscriptLinesRequest{Query: "dragon"}))
	if err != nil || len(resp.Msg.GetLines()) != 0 || store.searchCalls != before {
		t.Errorf("guest without transcripts.read: %d lines, %d extra store reads, err %v; want none", len(resp.Msg.GetLines()), store.searchCalls-before, err)
	}
}

// TestGuest_HighlightsPromotedOnlyAndCampaignScoped: a guest never sees a
// candidate, and a Highlight of another Campaign of the host Tenant is "no
// such highlight" through both by-id paths.
func TestGuest_HighlightsPromotedOnlyAndCampaignScoped(t *testing.T) {
	t.Parallel()
	w, srv, store, highlights := sessionWorld(t)
	mine, theirs := uuid.New(), uuid.New()
	store.voiceSessions[mine] = storage.VoiceSession{ID: mine, CampaignID: w.guestCampaign.ID}
	store.voiceSessions[theirs] = storage.VoiceSession{ID: theirs, CampaignID: w.otherCampaign.ID}

	promoted := seedRPCHighlight(highlights, w.tenant, mine, w.guestCampaign.ID, storage.HighlightPromoted)
	promoted.SpeakerIDs = []string{"111", "222"}
	highlights.put(promoted)
	candidate := seedRPCHighlight(highlights, w.tenant, mine, w.guestCampaign.ID, storage.HighlightCandidate)
	foreign := seedRPCHighlight(highlights, w.tenant, theirs, w.otherCampaign.ID, storage.HighlightPromoted)

	ctx := asGuest(w.tenant, w.guestCampaign.ID, auth.RightHighlightsRead)
	listed, err := srv.ListHighlights(ctx, connect.NewRequest(&managementv1.ListHighlightsRequest{VoiceSessionId: mine.String()}))
	if err != nil {
		t.Fatalf("ListHighlights: %v", err)
	}
	if len(listed.Msg.GetHighlights()) != 1 || listed.Msg.GetHighlights()[0].GetId() != promoted.ID.String() {
		t.Fatalf("guest listed %v, want only the promoted highlight", listed.Msg.GetHighlights())
	}
	h := listed.Msg.GetHighlights()[0]
	if len(h.GetSpeakerIds()) != 0 || h.GetScore() != 0 {
		t.Errorf("guest got the GM's working data: speaker ids %v, score %v", h.GetSpeakerIds(), h.GetScore())
	}
	if h.GetExcerpt() == "" {
		t.Error("the promoted highlight's caption was withheld")
	}

	// By session id: another campaign's session.
	if _, err := srv.ListHighlights(ctx, connect.NewRequest(&managementv1.ListHighlightsRequest{VoiceSessionId: theirs.String()})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("listing another campaign's session: err = %v, want CodeNotFound", err)
	}
	// By highlight id: a candidate of their own campaign, and a promoted one
	// of the other campaign.
	for name, id := range map[string]uuid.UUID{"a candidate": candidate.ID, "another campaign's highlight": foreign.ID} {
		if _, err := srv.GetHighlight(ctx, connect.NewRequest(&managementv1.GetHighlightRequest{Id: id.String()})); connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("GetHighlight(%s): err = %v, want CodeNotFound", name, err)
		}
	}
	if got, err := srv.GetHighlight(ctx, connect.NewRequest(&managementv1.GetHighlightRequest{Id: promoted.ID.String()})); err != nil || len(got.Msg.GetHighlight().GetSpeakerIds()) != 0 {
		t.Errorf("GetHighlight(promoted) = %v, %v", got, err)
	}

	// The GM of this tenant, with the guest's campaign resolved, sees the
	// candidate and the working data.
	gmSrv := rpc.NewSessionServer(&fakeSessionManager{}, campaignSessionStore(w.guestCampaign.ID, mine), nil, nil)
	gmSrv.SetHighlights(highlights, nil, nil)
	all, err := gmSrv.ListHighlights(asGM(w.tenant), connect.NewRequest(&managementv1.ListHighlightsRequest{VoiceSessionId: mine.String()}))
	if err != nil || len(all.Msg.GetHighlights()) != 2 {
		t.Fatalf("the GM listed %v, %v; want the candidate too", all, err)
	}
	for _, h := range all.Msg.GetHighlights() {
		if h.GetId() == promoted.ID.String() && (len(h.GetSpeakerIds()) != 2 || h.GetScore() == 0) {
			t.Errorf("the GM lost speaker ids/score: %+v", h)
		}
	}
}

// recordingSearchStore records which engine reads a search performed.
type recordingSearchStore struct {
	fakeSearchEngineStore
	lineSearches, highlightSearches int
	highlightCampaign               uuid.UUID
}

func (r *recordingSearchStore) SearchTranscriptLines(context.Context, uuid.UUID, string, int) ([]storage.TranscriptLine, error) {
	r.lineSearches++
	return []storage.TranscriptLine{{VoiceSessionID: uuid.New(), LineID: "l-1", Text: "the dragon wakes"}}, nil
}

func (r *recordingSearchStore) SearchPromotedHighlights(_ context.Context, _, campaignID uuid.UUID, _ string, _ int) ([]storage.Highlight, error) {
	r.highlightSearches++
	r.highlightCampaign = campaignID
	return []storage.Highlight{{ID: uuid.New(), Excerpt: "nat 20"}}, nil
}

// TestGuest_PaletteSearchHonoursEachRight: transcripts.read does not open
// Highlights and highlights.read does not open transcripts, even if a guest
// reached a handler the gate would have refused.
func TestGuest_PaletteSearchHonoursEachRight(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                         string
		rights                       []auth.GuestRight
		wantTranscripts, wantHilight bool
	}{
		{"transcripts.read only", []auth.GuestRight{auth.RightTranscriptsRead}, true, false},
		{"highlights.read only", []auth.GuestRight{auth.RightHighlightsRead}, false, true},
		{"both", []auth.GuestRight{auth.RightTranscriptsRead, auth.RightHighlightsRead}, true, true},
		{"neither", []auth.GuestRight{auth.RightWikiEdit, auth.RightPlanningUse, auth.RightMapsEdit}, false, false},
	}
	for _, tc := range cases {
		w, srv, _, _ := sessionWorld(t)
		estore := &recordingSearchStore{}
		srv.SetSearch(search.New(estore, nil))
		ctx := asGuest(w.tenant, w.guestCampaign.ID, tc.rights...)

		tr, err := srv.SearchTranscripts(ctx, connect.NewRequest(&managementv1.SearchTranscriptsRequest{Query: "dragon"}))
		if err != nil {
			t.Fatalf("%s: SearchTranscripts: %v", tc.name, err)
		}
		if got := len(tr.Msg.GetHits()) > 0; got != tc.wantTranscripts || (estore.lineSearches > 0) != tc.wantTranscripts {
			t.Errorf("%s: transcript hits = %v (store reads %d), want hits = %v", tc.name, got, estore.lineSearches, tc.wantTranscripts)
		}
		hl, err := srv.SearchHighlights(ctx, connect.NewRequest(&managementv1.SearchHighlightsRequest{Query: "nat"}))
		if err != nil {
			t.Fatalf("%s: SearchHighlights: %v", tc.name, err)
		}
		if got := len(hl.Msg.GetHits()) > 0; got != tc.wantHilight || (estore.highlightSearches > 0) != tc.wantHilight {
			t.Errorf("%s: highlight hits = %v (store reads %d), want hits = %v", tc.name, got, estore.highlightSearches, tc.wantHilight)
		}
		if tc.wantHilight && estore.highlightCampaign != w.guestCampaign.ID {
			t.Errorf("%s: highlight search ran on campaign %s, want the guest campaign", tc.name, estore.highlightCampaign)
		}
	}
}

// ctxInjector installs a prepared context on both Connect call shapes, so the
// streaming exchange can be driven as a guest.
type ctxInjector struct {
	with func(context.Context) context.Context
}

func (i ctxInjector) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		return next(i.with(ctx), req)
	}
}

func (i ctxInjector) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i ctxInjector) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(i.with(ctx), conn)
	}
}

// markEngine is a ChatEngine recording whether an exchange reached it and
// whether it was told to run without the transcript tools.
type markEngine struct {
	calls              int
	withoutTranscripts bool
	gotThread          uuid.UUID
}

func (m *markEngine) Exchange(ctx context.Context, _ uuid.UUID, c storage.Campaign, threadID uuid.UUID, _ string, _ planchat.Events) (planchat.Result, error) {
	m.calls++
	m.withoutTranscripts = planchat.TranscriptsWithheld(ctx)
	m.gotThread = threadID
	return planchat.Result{
		AssistantMessage: storage.PlanningMessage{ID: uuid.New(), ThreadID: threadID, Role: storage.PlanningRoleAssistant},
		Thread:           storage.PlanningThread{ID: threadID, CampaignID: c.ID},
	}, nil
}

// TestGuest_PlanningIsOwnThreadsInOwnCampaign: a guest's chat is confined to
// their Campaign (the campaign id is client input) and to threads they
// started; the GM still sees every thread, the guests' included.
func TestGuest_PlanningIsOwnThreadsInOwnCampaign(t *testing.T) {
	t.Parallel()
	tenant := uuid.New()
	store := newFakeChatStore(tenant)
	guestCampaign := store.addCampaign()
	otherCampaign := store.addCampaign()
	srv := rpc.NewChatServer(store, nil)
	gm := asGM(tenant)
	guest := asGuest(tenant, guestCampaign.ID, auth.RightPlanningUse)
	otherGuestUser := storage.User{ID: uuid.New(), DiscordUserID: "777", Name: "Noor"}
	otherGuest := auth.WithGuest(auth.WithTenant(auth.WithUser(context.Background(), otherGuestUser), tenant),
		auth.GuestScope{CampaignID: guestCampaign.ID, Rights: auth.NewRightSet(auth.RightPlanningUse)})

	create := func(ctx context.Context, campaign uuid.UUID, title string) string {
		t.Helper()
		resp, err := srv.CreatePlanningThread(ctx, connect.NewRequest(&managementv1.CreatePlanningThreadRequest{CampaignId: campaign.String(), Title: title}))
		if err != nil {
			t.Fatalf("CreatePlanningThread(%q): %v", title, err)
		}
		return resp.Msg.GetThread().GetId()
	}
	gmThread := create(gm, guestCampaign.ID, "GM prep")
	mine := create(guest, guestCampaign.ID, "Mika's ideas")
	noors := create(otherGuest, guestCampaign.ID, "Noor's ideas")
	legacy, _ := store.CreatePlanningThread(context.Background(), guestCampaign.ID, "from before created_by")
	inOther := create(gm, otherCampaign.ID, "the other table")

	// Creation records the creator — for the GM's threads too.
	for id, wantUser := range map[string]uuid.UUID{mine: guestUser.ID, noors: otherGuestUser.ID} {
		th := store.threads[uuid.MustParse(id)]
		if !th.CreatedBy.Valid || th.CreatedBy.UUID != wantUser {
			t.Errorf("thread %s created_by = %v, want %s", th.Title, th.CreatedBy, wantUser)
		}
	}
	if th := store.threads[uuid.MustParse(gmThread)]; !th.CreatedBy.Valid {
		t.Error("the GM's thread recorded no creator")
	}

	// The campaign id is checked against the guest scope on EVERY procedure.
	other := otherCampaign.ID.String()
	for name, call := range map[string]func() error{
		"List": func() error {
			_, err := srv.ListPlanningThreads(guest, connect.NewRequest(&managementv1.ListPlanningThreadsRequest{CampaignId: other}))
			return err
		},
		"Get": func() error {
			_, err := srv.GetPlanningThread(guest, connect.NewRequest(&managementv1.GetPlanningThreadRequest{CampaignId: other, ThreadId: inOther}))
			return err
		},
		"Create": func() error {
			_, err := srv.CreatePlanningThread(guest, connect.NewRequest(&managementv1.CreatePlanningThreadRequest{CampaignId: other, Title: "x"}))
			return err
		},
		"Rename": func() error {
			_, err := srv.RenamePlanningThread(guest, connect.NewRequest(&managementv1.RenamePlanningThreadRequest{CampaignId: other, ThreadId: inOther, Title: "x"}))
			return err
		},
		"Delete": func() error {
			_, err := srv.DeletePlanningThread(guest, connect.NewRequest(&managementv1.DeletePlanningThreadRequest{CampaignId: other, ThreadId: inOther}))
			return err
		},
	} {
		if err := call(); connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("%s on another campaign of the host tenant: err = %v, want CodeNotFound", name, err)
		}
	}
	if th := store.threads[uuid.MustParse(inOther)]; th.Title != "the other table" {
		t.Fatalf("a guest changed a thread of another campaign: %+v", th)
	}
	// The GM of the tenant can name either campaign.
	if _, err := srv.ListPlanningThreads(gm, connect.NewRequest(&managementv1.ListPlanningThreadsRequest{CampaignId: other})); err != nil {
		t.Errorf("the GM listing their other campaign: %v", err)
	}

	// Within their campaign: only their own threads.
	listed, err := srv.ListPlanningThreads(guest, connect.NewRequest(&managementv1.ListPlanningThreadsRequest{CampaignId: guestCampaign.ID.String()}))
	if err != nil || len(listed.Msg.GetThreads()) != 1 || listed.Msg.GetThreads()[0].GetId() != mine {
		t.Fatalf("guest listed %v, %v; want only their own thread", listed.Msg.GetThreads(), err)
	}
	all, err := srv.ListPlanningThreads(gm, connect.NewRequest(&managementv1.ListPlanningThreadsRequest{CampaignId: guestCampaign.ID.String()}))
	if err != nil || len(all.Msg.GetThreads()) != 4 {
		t.Fatalf("the GM listed %d threads, %v; want all 4, guests' included", len(all.Msg.GetThreads()), err)
	}
	campaign := guestCampaign.ID.String()
	for name, id := range map[string]string{"the GM's": gmThread, "another guest's": noors, "a creator-less": legacy.ID.String()} {
		if _, err := srv.GetPlanningThread(guest, connect.NewRequest(&managementv1.GetPlanningThreadRequest{CampaignId: campaign, ThreadId: id})); connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("guest reading %s thread: err = %v, want CodeNotFound", name, err)
		}
		if _, err := srv.RenamePlanningThread(guest, connect.NewRequest(&managementv1.RenamePlanningThreadRequest{CampaignId: campaign, ThreadId: id, Title: "hijacked"})); connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("guest renaming %s thread: err = %v, want CodeNotFound", name, err)
		}
		if _, err := srv.DeletePlanningThread(guest, connect.NewRequest(&managementv1.DeletePlanningThreadRequest{CampaignId: campaign, ThreadId: id})); connect.CodeOf(err) != connect.CodeNotFound {
			t.Errorf("guest deleting %s thread: err = %v, want CodeNotFound", name, err)
		}
		if th, ok := store.threads[uuid.MustParse(id)]; !ok || th.Title == "hijacked" {
			t.Errorf("%s thread was changed or deleted by a guest: %+v (present %v)", name, th, ok)
		}
	}
	if _, err := srv.GetPlanningThread(guest, connect.NewRequest(&managementv1.GetPlanningThreadRequest{CampaignId: campaign, ThreadId: mine})); err != nil {
		t.Errorf("guest reading their own thread: %v", err)
	}
	if _, err := srv.RenamePlanningThread(guest, connect.NewRequest(&managementv1.RenamePlanningThreadRequest{CampaignId: campaign, ThreadId: mine, Title: "Mika's plan"})); err != nil {
		t.Errorf("guest renaming their own thread: %v", err)
	}
	// The GM reads and deletes a guest's thread.
	if _, err := srv.GetPlanningThread(gm, connect.NewRequest(&managementv1.GetPlanningThreadRequest{CampaignId: campaign, ThreadId: noors})); err != nil {
		t.Errorf("the GM reading a guest's thread: %v", err)
	}
	if _, err := srv.DeletePlanningThread(gm, connect.NewRequest(&managementv1.DeletePlanningThreadRequest{CampaignId: campaign, ThreadId: noors})); err != nil {
		t.Errorf("the GM deleting a guest's thread: %v", err)
	}

	// The exchange: a guest cannot talk in someone else's thread or another
	// campaign, and the engine is never reached when they try.
	exchange := func(with func(context.Context) context.Context, campaignID, threadID string) (*markEngine, error) {
		t.Helper()
		engine := &markEngine{}
		server := rpc.NewChatServer(store, nil)
		server.SetEngine(engine)
		mux := http.NewServeMux()
		mux.Handle(server.Handler(connect.WithInterceptors(ctxInjector{with: with})))
		httpSrv := httptest.NewServer(mux)
		defer httpSrv.Close()
		client := managementv1connect.NewChatServiceClient(http.DefaultClient, httpSrv.URL, connect.WithProtoJSON())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stream, err := client.PlanningExchange(ctx, connect.NewRequest(&managementv1.PlanningExchangeRequest{
			CampaignId: campaignID, ThreadId: threadID, Message: "Who guards the docks?",
		}))
		if err != nil {
			return engine, err
		}
		for stream.Receive() {
		}
		return engine, stream.Err()
	}
	rescope := func(base context.Context) func(context.Context) context.Context {
		return func(ctx context.Context) context.Context {
			u, _ := auth.CurrentUser(base)
			tid, _ := auth.TenantID(base)
			ctx = auth.WithTenant(auth.WithUser(ctx, u), tid)
			if g, ok := auth.Guest(base); ok {
				ctx = auth.WithGuest(ctx, g)
			}
			return ctx
		}
	}
	for name, target := range map[string][2]string{
		"the GM's thread":              {campaign, gmThread},
		"a creator-less thread":        {campaign, legacy.ID.String()},
		"a thread of another campaign": {other, inOther},
	} {
		engine, err := exchange(rescope(guest), target[0], target[1])
		if connect.CodeOf(err) != connect.CodeNotFound || engine.calls != 0 {
			t.Errorf("guest exchanging in %s: err = %v, engine calls = %d; want CodeNotFound and no spend", name, err, engine.calls)
		}
	}
	// In their own thread it runs — without the transcript tools, since
	// planning.use does not include transcripts.read…
	engine, err := exchange(rescope(guest), campaign, mine)
	if err != nil || engine.calls != 1 || !engine.withoutTranscripts || engine.gotThread.String() != mine {
		t.Errorf("guest's own exchange: err %v, calls %d, withoutTranscripts %v", err, engine.calls, engine.withoutTranscripts)
	}
	// …with them when the guest holds transcripts.read as well…
	both := asGuest(tenant, guestCampaign.ID, auth.RightPlanningUse, auth.RightTranscriptsRead)
	engine, err = exchange(rescope(both), campaign, mine)
	if err != nil || engine.calls != 1 || engine.withoutTranscripts {
		t.Errorf("guest with transcripts.read: err %v, calls %d, withoutTranscripts %v; want the full belt", err, engine.calls, engine.withoutTranscripts)
	}
	// …and always for the GM, in any thread.
	engine, err = exchange(rescope(gm), campaign, mine)
	if err != nil || engine.calls != 1 || engine.withoutTranscripts {
		t.Errorf("the GM's exchange in a guest's thread: err %v, calls %d, withoutTranscripts %v", err, engine.calls, engine.withoutTranscripts)
	}
}

// TestGuest_ListSessionsWithholdsEndReason: a guest sees that a session
// failed, never why — end_reason is the host's operational detail (raw loop
// and provider errors, the spend-cap cause), which no Guest Right covers. The
// GM, on the same rows, still gets it.
func TestGuest_ListSessionsWithholdsEndReason(t *testing.T) {
	t.Parallel()
	w, srv, store, _ := sessionWorld(t)
	reason := "loop_error: dial tcp 10.0.0.7:443: connection refused"
	store.listSessions = []storage.VoiceSession{
		{ID: uuid.New(), CampaignID: w.guestCampaign.ID, Status: "failed", EndReason: &reason},
	}

	for _, right := range []auth.GuestRight{auth.RightTranscriptsRead, auth.RightHighlightsRead} {
		resp, err := srv.ListSessions(asGuest(w.tenant, w.guestCampaign.ID, right),
			connect.NewRequest(&managementv1.ListSessionsRequest{}))
		if err != nil {
			t.Fatalf("ListSessions as a %s guest: %v", right, err)
		}
		if len(resp.Msg.GetSessions()) != 1 {
			t.Fatalf("a %s guest got %d sessions, want 1", right, len(resp.Msg.GetSessions()))
		}
		got := resp.Msg.GetSessions()[0]
		if got.GetEndReason() != "" {
			t.Errorf("a %s guest got end_reason %q, want it withheld", right, got.GetEndReason())
		}
		if got.GetStatus() != "failed" {
			t.Errorf("a %s guest lost the session status with the reason", right)
		}
	}

	resp, err := srv.ListSessions(asGM(w.tenant), connect.NewRequest(&managementv1.ListSessionsRequest{}))
	if err != nil {
		t.Fatalf("ListSessions as the GM: %v", err)
	}
	if got := resp.Msg.GetSessions()[0].GetEndReason(); got != reason {
		t.Errorf("the GM got end_reason %q, want %q", got, reason)
	}
}
