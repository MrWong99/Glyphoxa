//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

func mkMap(t *testing.T, st *storage.Store, campaignID uuid.UUID, name string) storage.CampaignMap {
	t.Helper()
	m, err := st.CreateMap(context.Background(), storage.NewCampaignMap{
		CampaignID: campaignID, Name: name,
		BlobKey:  "t/" + uuid.New().String() + "/map/" + uuid.New().String() + "/image",
		WidthPx:  2000,
		HeightPx: 1500,
	})
	if err != nil {
		t.Fatalf("CreateMap %q: %v", name, err)
	}
	return m
}

// TestMapPinLifecycle covers the load-bearing behaviours: normalized coordinates
// survive a re-upload at a different resolution, one pin per node per map, the
// same node on several maps, and deleting a node taking its pins with it.
func TestMapPinLifecycle(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	city := mkMap(t, st, campaignID, "Saltmarsh")
	tavern := mkMap(t, st, campaignID, "The Rusty Anchor")
	inn := mkNode(t, st, campaignID, storage.KGNodeLocation, "Rusty Anchor")

	pin, err := st.CreatePin(ctx, storage.NewMapPin{
		MapID: city.ID, CampaignID: campaignID, NodeID: inn.ID, X: 0.25, Y: 0.75,
	})
	if err != nil {
		t.Fatalf("CreatePin: %v", err)
	}
	if pin.NodeName != "Rusty Anchor" || pin.NodeType != storage.KGNodeLocation {
		t.Errorf("pin did not join its node: %+v", pin)
	}

	// One pin per node per map.
	if _, err := st.CreatePin(ctx, storage.NewMapPin{
		MapID: city.ID, CampaignID: campaignID, NodeID: inn.ID, X: 0.5, Y: 0.5,
	}); !errors.Is(err, storage.ErrConflict) {
		t.Errorf("duplicate pin: got %v, want ErrConflict", err)
	}

	// The SAME node on a DIFFERENT map is legitimate — a place appears on the city
	// map and on its own floor plan.
	if _, err := st.CreatePin(ctx, storage.NewMapPin{
		MapID: tavern.ID, CampaignID: campaignID, NodeID: inn.ID, X: 0.1, Y: 0.1,
	}); err != nil {
		t.Fatalf("pinning the same node on another map: %v", err)
	}

	// THE reason coordinates are normalized: a re-upload at a different resolution
	// keeps every pin exactly where the GM put it.
	if _, err := st.SetMapImage(ctx, campaignID, city.ID, "t/"+uuid.New().String()+"/map/"+uuid.New().String()+"/image", 400, 300); err != nil {
		t.Fatalf("SetMapImage: %v", err)
	}
	after, err := st.ListPins(ctx, campaignID, city.ID)
	if err != nil {
		t.Fatalf("ListPins: %v", err)
	}
	if len(after) != 1 || after[0].X != 0.25 || after[0].Y != 0.75 {
		t.Errorf("pins after re-upload = %+v, want the original normalized position", after)
	}

	// Out-of-range coordinates are refused, not clamped.
	if _, err := st.CreatePin(ctx, storage.NewMapPin{
		MapID: tavern.ID, CampaignID: campaignID, NodeID: mkNode(t, st, campaignID, storage.KGNodeItem, "Key").ID,
		X: 1.5, Y: 0.5,
	}); !errors.Is(err, storage.ErrInvalidPin) {
		t.Errorf("out-of-range pin: got %v, want ErrInvalidPin", err)
	}

	// Deleting the Node removes its Pins — a position with nothing at it is not
	// information.
	if err := st.DeleteNode(ctx, campaignID, inn.ID); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}
	for _, m := range []storage.CampaignMap{city, tavern} {
		pins, err := st.ListPins(ctx, campaignID, m.ID)
		if err != nil {
			t.Fatalf("ListPins after node delete: %v", err)
		}
		if len(pins) != 0 {
			t.Errorf("map %q kept %d pins after its node was deleted", m.Name, len(pins))
		}
	}
}

// TestMapPinCrossCampaign pins the composite-FK guarantee: a Pin cannot join a Map
// and a Node from different Campaigns, without a trigger.
func TestMapPinCrossCampaign(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignA := seedCampaign(t, dsn)
	_, _, campaignB := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	mapA := mkMap(t, st, campaignA, "A's world")
	nodeB := mkNode(t, st, campaignB, storage.KGNodeLocation, "B's town")

	if _, err := st.CreatePin(ctx, storage.NewMapPin{
		MapID: mapA.ID, CampaignID: campaignA, NodeID: nodeB.ID, X: 0.5, Y: 0.5,
	}); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("cross-campaign pin: got %v, want ErrNotFound", err)
	}
}

// TestMapPlayerVisibility pins ADR-0056 at the READ: a gm_private Map, a
// gm_private Pin, and a Pin whose NODE is gm_private are all absent from the
// player-tier response — not merely hidden by the UI. A position pointing at a GM
// secret is itself a leak.
func TestMapPlayerVisibility(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	open := mkMap(t, st, campaignID, "Saltmarsh")
	secretMap, err := st.CreateMap(ctx, storage.NewCampaignMap{
		CampaignID: campaignID, Name: "The smugglers' routes",
		BlobKey: "t/" + uuid.New().String() + "/map/" + uuid.New().String() + "/image",
		WidthPx: 100, HeightPx: 100, GMPrivate: true,
	})
	if err != nil {
		t.Fatalf("CreateMap secret: %v", err)
	}

	publicNode := mkNode(t, st, campaignID, storage.KGNodeLocation, "The docks")
	secretNode, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeLocation, Name: "The cache", GMPrivate: true,
	})
	if err != nil {
		t.Fatalf("CreateNode secret: %v", err)
	}
	hush := mkNode(t, st, campaignID, storage.KGNodeItem, "A loose floorboard")

	mustPin := func(nodeID uuid.UUID, x float64, private bool) {
		t.Helper()
		if _, err := st.CreatePin(ctx, storage.NewMapPin{
			MapID: open.ID, CampaignID: campaignID, NodeID: nodeID, X: x, Y: 0.5, GMPrivate: private,
		}); err != nil {
			t.Fatalf("CreatePin: %v", err)
		}
	}
	mustPin(publicNode.ID, 0.1, false)
	mustPin(secretNode.ID, 0.2, false) // public pin, PRIVATE node
	mustPin(hush.ID, 0.3, true)        // private pin, public node

	gmMaps, err := st.ListMaps(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListMaps: %v", err)
	}
	if len(gmMaps) != 2 {
		t.Errorf("GM sees %d maps, want both", len(gmMaps))
	}
	playerMaps, err := st.ListPlayerMaps(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListPlayerMaps: %v", err)
	}
	if len(playerMaps) != 1 || playerMaps[0].ID != open.ID {
		t.Errorf("player sees %+v, want only the public map", playerMaps)
	}
	for _, m := range playerMaps {
		if m.ID == secretMap.ID {
			t.Error("a gm_private map reached the player tier")
		}
	}

	gmPins, err := st.ListPins(ctx, campaignID, open.ID)
	if err != nil {
		t.Fatalf("ListPins: %v", err)
	}
	if len(gmPins) != 3 {
		t.Errorf("GM sees %d pins, want all 3", len(gmPins))
	}
	playerPins, err := st.ListPlayerPins(ctx, campaignID, open.ID)
	if err != nil {
		t.Fatalf("ListPlayerPins: %v", err)
	}
	if len(playerPins) != 1 || playerPins[0].NodeID != publicNode.ID {
		t.Fatalf("player pins = %+v, want only the fully-public one", playerPins)
	}
	for _, p := range playerPins {
		if p.Hidden() {
			t.Errorf("a hidden pin reached the player tier: %+v", p)
		}
	}
}

// TestMapNestingAndDelete covers the breadcrumb chain, the self-parent refusal,
// the bounded walk, and the SET NULL rather than cascade on a middle delete.
func TestMapNestingAndDelete(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	world := mkMap(t, st, campaignID, "The world")
	region := mkMap(t, st, campaignID, "The coast")
	city := mkMap(t, st, campaignID, "Saltmarsh")

	nest := func(child, parent storage.CampaignMap) storage.CampaignMap {
		t.Helper()
		u, err := st.UpdateMap(ctx, storage.CampaignMapUpdate{
			ID: child.ID, CampaignID: campaignID, Name: child.Name,
			ParentMapID: uuid.NullUUID{UUID: parent.ID, Valid: true},
		})
		if err != nil {
			t.Fatalf("UpdateMap nest %q: %v", child.Name, err)
		}
		return u
	}
	region = nest(region, world)
	city = nest(city, region)

	crumbs, err := st.MapAncestors(ctx, campaignID, city.ID)
	if err != nil {
		t.Fatalf("MapAncestors: %v", err)
	}
	if len(crumbs) != 2 || crumbs[0].ID != region.ID || crumbs[1].ID != world.ID {
		t.Errorf("breadcrumb = %+v, want nearest parent first", crumbs)
	}

	// A Map cannot contain itself.
	if _, err := st.UpdateMap(ctx, storage.CampaignMapUpdate{
		ID: city.ID, CampaignID: campaignID, Name: city.Name,
		ParentMapID: uuid.NullUUID{UUID: city.ID, Valid: true},
	}); !errors.Is(err, storage.ErrInvalidMapParent) {
		t.Errorf("self-parent: got %v, want ErrInvalidMapParent", err)
	}

	// A deeper cycle is not schema-blocked, so the walk must be BOUNDED rather than
	// hang the screen: re-parent the world under the city and confirm it terminates.
	if _, err := st.UpdateMap(ctx, storage.CampaignMapUpdate{
		ID: world.ID, CampaignID: campaignID, Name: world.Name,
		ParentMapID: uuid.NullUUID{UUID: city.ID, Valid: true},
	}); err != nil {
		t.Fatalf("UpdateMap cycle: %v", err)
	}
	looped, err := st.MapAncestors(ctx, campaignID, city.ID)
	if err != nil {
		t.Fatalf("MapAncestors on a cycle: %v", err)
	}
	if len(looped) > 10 {
		t.Errorf("cyclic breadcrumb returned %d entries; the walk must be bounded", len(looped))
	}

	// Deleting a middle Map lifts its children instead of taking a subtree with it,
	// and returns the blob key the caller must drop through the seam.
	key, err := st.DeleteMap(ctx, campaignID, region.ID)
	if err != nil {
		t.Fatalf("DeleteMap: %v", err)
	}
	if key == "" {
		t.Error("DeleteMap returned no blob key; the caller cannot free the bytes")
	}
	orphan, err := st.GetMap(ctx, campaignID, city.ID)
	if err != nil {
		t.Fatalf("GetMap after middle delete: %v", err)
	}
	if orphan.ParentMapID.Valid {
		t.Errorf("child kept parent %v after its parent was deleted", orphan.ParentMapID)
	}
}

// TestUnpinnedNodes pins the tray's contents: placeable types only, and only what
// is not already on THIS map.
func TestUnpinnedNodes(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	m := mkMap(t, st, campaignID, "Saltmarsh")
	town := mkNode(t, st, campaignID, storage.KGNodeLocation, "The docks")
	mkNode(t, st, campaignID, storage.KGNodeNPC, "Bart")
	mkNode(t, st, campaignID, storage.KGNodeItem, "A key")
	// Not placeable: a faction and a plot thread do not occupy space.
	mkNode(t, st, campaignID, storage.KGNodeFaction, "The smugglers")
	mkNode(t, st, campaignID, storage.KGNodePlotThread, "The heist")

	before, err := st.UnpinnedNodes(ctx, campaignID, m.ID)
	if err != nil {
		t.Fatalf("UnpinnedNodes: %v", err)
	}
	names := map[string]bool{}
	for _, n := range before {
		names[n.Name] = true
	}
	if len(before) != 3 || !names["The docks"] || !names["Bart"] || !names["A key"] {
		t.Fatalf("tray = %v, want the three placeable entries only", names)
	}

	if _, err := st.CreatePin(ctx, storage.NewMapPin{
		MapID: m.ID, CampaignID: campaignID, NodeID: town.ID, X: 0.5, Y: 0.5,
	}); err != nil {
		t.Fatalf("CreatePin: %v", err)
	}
	after, err := st.UnpinnedNodes(ctx, campaignID, m.ID)
	if err != nil {
		t.Fatalf("UnpinnedNodes after pin: %v", err)
	}
	for _, n := range after {
		if n.ID == town.ID {
			t.Error("a pinned entry is still offered in the tray")
		}
	}
	if len(after) != 2 {
		t.Errorf("tray = %d entries, want 2 after pinning one", len(after))
	}
}

// TestMapAnchorNodeDelete pins the composite-FK column list. A bare
// ON DELETE SET NULL on a composite FK nulls EVERY referencing column — including
// campaign_id, which is NOT NULL — so deleting an anchored Node would fail the
// whole delete. Naming the column confines the null to the anchor.
func TestMapAnchorNodeDelete(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	town := mkNode(t, st, campaignID, storage.KGNodeLocation, "Saltmarsh")
	m, err := st.CreateMap(ctx, storage.NewCampaignMap{
		CampaignID: campaignID, Name: "Saltmarsh",
		BlobKey: "t/" + uuid.New().String() + "/map/" + uuid.New().String() + "/image",
		WidthPx: 100, HeightPx: 100,
		AnchorNodeID: uuid.NullUUID{UUID: town.ID, Valid: true},
	})
	if err != nil {
		t.Fatalf("CreateMap: %v", err)
	}

	// Deleting the anchor Node must succeed, not fail on a NOT NULL campaign_id.
	if err := st.DeleteNode(ctx, campaignID, town.ID); err != nil {
		t.Fatalf("DeleteNode on an anchored map: %v", err)
	}

	after, err := st.GetMap(ctx, campaignID, m.ID)
	if err != nil {
		t.Fatalf("GetMap after anchor delete: %v", err)
	}
	if after.AnchorNodeID.Valid {
		t.Errorf("anchor = %v, want cleared", after.AnchorNodeID)
	}
	if after.CampaignID != campaignID {
		t.Errorf("campaign_id = %v, want it untouched (%v)", after.CampaignID, campaignID)
	}
}
