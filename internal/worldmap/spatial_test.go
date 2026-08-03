package worldmap_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/worldmap"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
)

// The spatial adapter (#539, ADR-0060). Everything asserted here is spoken aloud
// at the table when it goes wrong, so the tests are about what must NOT come back:
// a private map's name, a hidden pin's label, a map the calling Agent has no
// business knowing, and a confidently wrong list of neighbours.

var (
	campaignID = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	sessionID  = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	townID     = uuid.MustParse("33333333-3333-3333-3333-333333333333")
	capitalID  = uuid.MustParse("44444444-4444-4444-4444-444444444444")
	secretID   = uuid.MustParse("55555555-5555-5555-5555-555555555555")
	agentID    = uuid.MustParse("66666666-6666-6666-6666-666666666666")
)

// fakeSpatialStore is a hand-built world: two public maps and one private one.
type fakeSpatialStore struct {
	maps    []storage.CampaignMap
	pins    map[uuid.UUID][]storage.MapPin // by map
	byNode  map[uuid.UUID][]storage.MapPin // by node, ALREADY privacy-filtered
	marker  storage.PartyMarker
	linked  storage.KGNode
	isLink  bool
	search  []storage.KGNode
	nearby  []storage.MapPin
	nearMap uuid.UUID
}

func (f *fakeSpatialStore) ListPlayerMaps(context.Context, uuid.UUID) ([]storage.CampaignMap, error) {
	var out []storage.CampaignMap
	for _, m := range f.maps {
		if !m.GMPrivate {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeSpatialStore) ListPlayerPins(_ context.Context, _, mapID uuid.UUID) ([]storage.MapPin, error) {
	var out []storage.MapPin
	for _, p := range f.pins[mapID] {
		if p.Hidden() {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func (f *fakeSpatialStore) PlayerNodePins(_ context.Context, _, nodeID uuid.UUID) ([]storage.MapPin, error) {
	return f.byNode[nodeID], nil
}

// PinsNear honours mapID the way the real query does — a fake that returns the
// same pins for every map would let a test "prove" isolation it never had.
func (f *fakeSpatialStore) PinsNear(_ context.Context, _, mapID uuid.UUID, _, _, _ float64, _ bool, _ int) ([]storage.MapPin, error) {
	f.nearMap = mapID
	var out []storage.MapPin
	for _, p := range f.nearby {
		if p.MapID == mapID {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeSpatialStore) GetPartyMarker(context.Context, uuid.UUID, uuid.UUID) (storage.PartyMarker, error) {
	return f.marker, nil
}

func (f *fakeSpatialStore) SearchPublicNodes(context.Context, uuid.UUID, string, int) ([]storage.KGNode, error) {
	return f.search, nil
}

func (f *fakeSpatialStore) AgentLinkedNode(context.Context, uuid.UUID) (storage.KGNode, bool, error) {
	return f.linked, f.isLink, nil
}

func liveCtx() context.Context {
	return session.NewContext(context.Background(), session.Identity{
		SessionID: sessionID, CampaignID: campaignID, TenantID: uuid.New(),
	})
}

// world builds the standard fixture: the innkeeper stands in Saltmarsh; the same
// Node is also pinned in the enemy capital and on a GM-private map.
func world() *fakeSpatialStore {
	inn := storage.KGNode{ID: uuid.New(), Name: "The Rusty Anchor"}
	return &fakeSpatialStore{
		maps: []storage.CampaignMap{
			{ID: townID, Name: "Saltmarsh"},
			{ID: capitalID, Name: "The Enemy Capital"},
			{ID: secretID, Name: "The Vault", GMPrivate: true},
		},
		search: []storage.KGNode{inn},
		byNode: map[uuid.UUID][]storage.MapPin{
			inn.ID: {
				{ID: uuid.New(), MapID: townID, NodeName: "The Rusty Anchor", X: 0.2, Y: 0.2},
				{ID: uuid.New(), MapID: capitalID, NodeName: "The Rusty Anchor", X: 0.8, Y: 0.8},
				{ID: uuid.New(), MapID: secretID, NodeName: "The Rusty Anchor", X: 0.5, Y: 0.5},
			},
		},
		linked: inn,
		isLink: true,
	}
}

// TestLocate_NeverNamesAPrivateMap: a GM-private Map's name is a secret, exactly
// as a private Pin's label is. Naming the map is as much a leak as naming the pin.
func TestLocate_NeverNamesAPrivateMap(t *testing.T) {
	a := worldmap.NewSpatialAdapter(world())
	places, err := a.Locate(liveCtx(), agentID.String(), "anchor", tool.SpatialScopeCampaign)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	for _, p := range places {
		if p.MapName == "The Vault" {
			t.Fatalf("a GM-private map reached the prompt: %+v", places)
		}
	}
	if len(places) != 2 {
		t.Fatalf("want both public maps, got %+v", places)
	}
}

// TestLocate_OwnMapsScopeCannotReachAnotherMap is the #539 acceptance criterion
// that had no code behind it: "a narrowed grant cannot reach another Map".
//
// The innkeeper's own Node is pinned in Saltmarsh AND in the enemy capital. A
// campaign-scoped grant answers with both; an own_node grant must answer only
// with the map the innkeeper actually stands on.
func TestLocate_OwnMapsScopeCannotReachAnotherMap(t *testing.T) {
	store := world()
	// The innkeeper stands only in Saltmarsh.
	store.byNode[store.linked.ID] = []storage.MapPin{
		{ID: uuid.New(), MapID: townID, NodeName: "The Rusty Anchor", X: 0.2, Y: 0.2},
	}
	// The thing being LOOKED UP is pinned in both towns.
	target := storage.KGNode{ID: uuid.New(), Name: "The Black Gate"}
	store.search = []storage.KGNode{target}
	store.byNode[target.ID] = []storage.MapPin{
		{ID: uuid.New(), MapID: townID, NodeName: "The Black Gate", X: 0.3, Y: 0.3},
		{ID: uuid.New(), MapID: capitalID, NodeName: "The Black Gate", X: 0.7, Y: 0.7},
	}
	a := worldmap.NewSpatialAdapter(store)

	wide, err := a.Locate(liveCtx(), agentID.String(), "gate", tool.SpatialScopeCampaign)
	if err != nil {
		t.Fatalf("Locate campaign: %v", err)
	}
	if len(wide) != 2 {
		t.Fatalf("a campaign grant should see both maps, got %+v", wide)
	}

	narrow, err := a.Locate(liveCtx(), agentID.String(), "gate", tool.SpatialScopeOwnMaps)
	if err != nil {
		t.Fatalf("Locate own_maps: %v", err)
	}
	if len(narrow) != 1 || narrow[0].MapName != "Saltmarsh" {
		t.Fatalf("a narrowed grant reached beyond its own map: %+v", narrow)
	}
}

// TestLocate_UnlinkedAgentUnderOwnMapsSeesNothing: an Agent with no linked Node
// stands nowhere, so it has no vantage point. Failing OPEN here would make the
// narrowing meaningless for exactly the Agents most likely to be scoped.
func TestLocate_UnlinkedAgentUnderOwnMapsSeesNothing(t *testing.T) {
	store := world()
	store.isLink = false
	a := worldmap.NewSpatialAdapter(store)

	places, err := a.Locate(liveCtx(), agentID.String(), "anchor", tool.SpatialScopeOwnMaps)
	if err != nil {
		t.Fatalf("Locate: %v", err)
	}
	if len(places) != 0 {
		t.Fatalf("an Agent that stands nowhere answered anyway: %+v", places)
	}
}

// TestNearby_MarkerOnAHiddenPinAnswersNothing: the party is at the secret cache.
// Falling back to the map's centre would produce a confidently WRONG list of
// neighbours — worse than silence, because the NPC states it as fact.
func TestNearby_MarkerOnAHiddenPinAnswersNothing(t *testing.T) {
	store := world()
	hidden := uuid.New()
	store.marker = storage.PartyMarker{
		MapID: uuid.NullUUID{UUID: townID, Valid: true},
		PinID: uuid.NullUUID{UUID: hidden, Valid: true},
	}
	// ListPlayerPins excludes the hidden pin, so the origin cannot be resolved.
	store.pins = map[uuid.UUID][]storage.MapPin{
		townID: {{ID: hidden, MapID: townID, GMPrivate: true, X: 0.9, Y: 0.9}},
	}
	store.nearby = []storage.MapPin{{ID: uuid.New(), MapID: townID, NodeName: "Fish Market", X: 0.5, Y: 0.5}}
	a := worldmap.NewSpatialAdapter(store)

	places, err := a.Nearby(liveCtx(), agentID.String(), 0.2, 5, tool.SpatialScopeCampaign)
	if err != nil {
		t.Fatalf("Nearby: %v", err)
	}
	if len(places) != 0 {
		t.Fatalf("answered from the map centre instead of staying silent: %+v", places)
	}
}

// TestNearby_CoLocatedPinIsStillANeighbour: an innkeeper pinned on the exact spot
// of their tavern is a real neighbour at distance zero. Dropping every pin at
// distance 0 silently lost them; only the origin's OWN pin is noise.
func TestNearby_CoLocatedPinIsStillANeighbour(t *testing.T) {
	store := world()
	originPin := uuid.New()
	store.marker = storage.PartyMarker{
		MapID: uuid.NullUUID{UUID: townID, Valid: true},
		PinID: uuid.NullUUID{UUID: originPin, Valid: true},
	}
	store.pins = map[uuid.UUID][]storage.MapPin{
		townID: {{ID: originPin, MapID: townID, NodeName: "The Rusty Anchor", X: 0.4, Y: 0.4}},
	}
	store.nearby = []storage.MapPin{
		{ID: originPin, MapID: townID, NodeName: "The Rusty Anchor", X: 0.4, Y: 0.4},
		{ID: uuid.New(), MapID: townID, NodeName: "Bart the Innkeeper", X: 0.4, Y: 0.4},
	}
	a := worldmap.NewSpatialAdapter(store)

	places, err := a.Nearby(liveCtx(), agentID.String(), 0.2, 5, tool.SpatialScopeCampaign)
	if err != nil {
		t.Fatalf("Nearby: %v", err)
	}
	if len(places) != 1 || places[0].Name != "Bart the Innkeeper" {
		t.Fatalf("the co-located innkeeper was lost: %+v", places)
	}
}

// TestNearby_NarrowedAgentCannotLookAroundAnotherMap: the GM places the marker in
// the enemy capital. A scoped innkeeper who stands in Saltmarsh must not be able
// to describe it.
func TestNearby_NarrowedAgentCannotLookAroundAnotherMap(t *testing.T) {
	store := world()
	store.byNode[store.linked.ID] = []storage.MapPin{
		{ID: uuid.New(), MapID: townID, NodeName: "The Rusty Anchor", X: 0.2, Y: 0.2},
	}
	store.marker = storage.PartyMarker{MapID: uuid.NullUUID{UUID: capitalID, Valid: true}}
	store.nearby = []storage.MapPin{{ID: uuid.New(), MapID: capitalID, NodeName: "The Black Gate", X: 0.5, Y: 0.5}}
	a := worldmap.NewSpatialAdapter(store)

	wide, err := a.Nearby(liveCtx(), agentID.String(), 0.2, 5, tool.SpatialScopeCampaign)
	if err != nil {
		t.Fatalf("Nearby campaign: %v", err)
	}
	if len(wide) != 1 {
		t.Fatalf("a campaign grant should answer from the marker: %+v", wide)
	}

	narrow, err := a.Nearby(liveCtx(), agentID.String(), 0.2, 5, tool.SpatialScopeOwnMaps)
	if err != nil {
		t.Fatalf("Nearby own_maps: %v", err)
	}
	if len(narrow) != 0 {
		t.Fatalf("a narrowed Agent described a map it has never stood on: %+v", narrow)
	}
}

// TestNearby_MarkerOnAPrivateMapNeverDescribesIt: the party is somewhere the table
// does not know exists. The marker is unusable, so the query falls back to where
// the ASKING NPC stands — which it legitimately knows — and the private map's
// contents never appear.
func TestNearby_MarkerOnAPrivateMapNeverDescribesIt(t *testing.T) {
	store := world()
	store.byNode[store.linked.ID] = []storage.MapPin{
		{ID: uuid.New(), MapID: townID, NodeName: "The Rusty Anchor", X: 0.2, Y: 0.2},
	}
	store.marker = storage.PartyMarker{
		MapID: uuid.NullUUID{UUID: secretID, Valid: true}, MapGMPrivate: true,
	}
	store.nearby = []storage.MapPin{
		{ID: uuid.New(), MapID: secretID, NodeName: "The Hoard", X: 0.5, Y: 0.5},
		{ID: uuid.New(), MapID: townID, NodeName: "Fish Market", X: 0.25, Y: 0.25},
	}
	a := worldmap.NewSpatialAdapter(store)

	places, err := a.Nearby(liveCtx(), agentID.String(), 0.2, 5, tool.SpatialScopeCampaign)
	if err != nil {
		t.Fatalf("Nearby: %v", err)
	}
	for _, p := range places {
		if p.Name == "The Hoard" || p.MapName == "The Vault" {
			t.Fatalf("a GM-private map's contents reached the prompt: %+v", places)
		}
	}
	if len(places) != 1 || places[0].Name != "Fish Market" {
		t.Fatalf("want the asking NPC's own surroundings, got %+v", places)
	}
}

// TestNearby_PrivateMarkerAndNowhereToStandAnswersNothing: with the marker
// unusable AND the Agent pinned nowhere, there is no origin at all, and the honest
// answer is nothing.
func TestNearby_PrivateMarkerAndNowhereToStandAnswersNothing(t *testing.T) {
	store := world()
	store.byNode = map[uuid.UUID][]storage.MapPin{}
	store.marker = storage.PartyMarker{
		MapID: uuid.NullUUID{UUID: secretID, Valid: true}, MapGMPrivate: true,
	}
	store.nearby = []storage.MapPin{{ID: uuid.New(), MapID: secretID, NodeName: "The Hoard", X: 0.5, Y: 0.5}}
	a := worldmap.NewSpatialAdapter(store)

	places, err := a.Nearby(liveCtx(), agentID.String(), 0.2, 5, tool.SpatialScopeCampaign)
	if err != nil {
		t.Fatalf("Nearby: %v", err)
	}
	if len(places) != 0 {
		t.Fatalf("answered with no origin at all: %+v", places)
	}
}

// TestNearby_NoSessionIsAnError: a spatial Tool outside a live turn has no
// Campaign to scope to, and answering from nothing would be answering from
// whatever the caller last had.
func TestNearby_NoSessionIsAnError(t *testing.T) {
	a := worldmap.NewSpatialAdapter(world())
	if _, err := a.Nearby(context.Background(), agentID.String(), 0.2, 5, tool.SpatialScopeCampaign); err == nil {
		t.Fatal("a spatial read outside a session must not succeed")
	}
}

// TestNearby_CapsAtTheLimit: the adapter enforces the cap, not just the Tool. A
// list longer than this is not an answer an NPC can say out loud.
func TestNearby_CapsAtTheLimit(t *testing.T) {
	store := world()
	store.marker = storage.PartyMarker{MapID: uuid.NullUUID{UUID: townID, Valid: true}}
	for i := 0; i < 20; i++ {
		store.nearby = append(store.nearby, storage.MapPin{
			ID: uuid.New(), MapID: townID, NodeName: "Stall", X: float64(i) / 100, Y: 0.1,
		})
	}
	a := worldmap.NewSpatialAdapter(store)

	places, err := a.Nearby(liveCtx(), agentID.String(), 0.5, 3, tool.SpatialScopeCampaign)
	if err != nil {
		t.Fatalf("Nearby: %v", err)
	}
	if len(places) != 3 {
		t.Fatalf("limit not enforced by the adapter: got %d", len(places))
	}
}
