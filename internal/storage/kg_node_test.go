//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestKGNodeCreateListRoundTrip is #126 AC1 + the ADR-0008 v1.0 grain: a Node is
// inserted with its type/name/body/gm_private and read back in list order
// (node_type enum, lower(name), id). It exercises Note AND Location so the schema
// sized for all 7 types is proven on more than one enum value.
func TestKGNodeCreateListRoundTrip(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// Insert out of both alphabetical and type order so ORDER BY is actually tested.
	zebra, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeNote, Name: "Zebra rumor", Body: "A striped horse was seen.",
	})
	if err != nil {
		t.Fatalf("CreateNode zebra: %v", err)
	}
	if zebra.ID.String() == "" || zebra.CampaignID != campaignID {
		t.Fatalf("created node missing ids: %+v", zebra)
	}
	if zebra.Type != storage.KGNodeNote || zebra.Name != "Zebra rumor" || zebra.Body != "A striped horse was seen." {
		t.Fatalf("created node fields not persisted: %+v", zebra)
	}
	if zebra.CreatedAt.IsZero() || zebra.UpdatedAt.IsZero() {
		t.Errorf("timestamps not defaulted: %+v", zebra)
	}

	if _, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeNote, Name: "apple orchard", Body: "Sweet fruit grows east.", GMPrivate: true,
	}); err != nil {
		t.Fatalf("CreateNode apple: %v", err)
	}
	loc, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeLocation, Name: "Barrow", Body: "A haunted mound.",
	})
	if err != nil {
		t.Fatalf("CreateNode location: %v", err)
	}
	if loc.Type != storage.KGNodeLocation {
		t.Errorf("location type not persisted: %+v", loc)
	}

	nodes, err := st.ListNodes(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("ListNodes returned %d nodes, want 3", len(nodes))
	}
	// Order: node_type enum first (location < note in the CREATE TYPE order), then
	// lower(name): so Barrow (location), then apple orchard, then Zebra rumor.
	if nodes[0].Name != "Barrow" {
		t.Errorf("nodes[0] = %q, want Barrow (location sorts before note)", nodes[0].Name)
	}
	if nodes[1].Name != "apple orchard" || !nodes[1].GMPrivate {
		t.Errorf("nodes[1] = %q (gm_private=%v), want apple orchard gm_private=true", nodes[1].Name, nodes[1].GMPrivate)
	}
	if nodes[2].Name != "Zebra rumor" {
		t.Errorf("nodes[2] = %q, want Zebra rumor (case-insensitive name order)", nodes[2].Name)
	}
}

// TestKGNodeUpdate is #129 AC2/AC3: UpdateNode persists name/body/gm_private,
// bumps updated_at (never touching node_type — immutable, ADR-0008), and yields
// ErrNotFound for an id that does not exist.
func TestKGNodeUpdate(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	created, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeLocation, Name: "Barrow", Body: "A haunted mound.",
	})
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}

	updated, err := st.UpdateNode(ctx, storage.KGNodeUpdate{
		ID: created.ID, Name: "Old Barrow", Body: "A very haunted mound.", GMPrivate: true,
	})
	if err != nil {
		t.Fatalf("UpdateNode: %v", err)
	}
	if updated.ID != created.ID || updated.CampaignID != campaignID {
		t.Errorf("update changed identity: %+v", updated)
	}
	if updated.Name != "Old Barrow" || updated.Body != "A very haunted mound." || !updated.GMPrivate {
		t.Errorf("fields not persisted: %+v", updated)
	}
	if updated.Type != storage.KGNodeLocation {
		t.Errorf("node_type must be immutable, got %q", updated.Type)
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Errorf("updated_at not bumped: created %v updated %v", created.UpdatedAt, updated.UpdatedAt)
	}

	// The change is durable — a fresh read reflects the new fields.
	nodes, err := st.ListNodes(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Name != "Old Barrow" || !nodes[0].GMPrivate {
		t.Errorf("update did not persist across reload: %+v", nodes)
	}

	if _, err := st.UpdateNode(ctx, storage.KGNodeUpdate{ID: uuid.New(), Name: "ghost"}); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("UpdateNode missing id err = %v, want ErrNotFound", err)
	}
}

// TestKGNodeDelete is #129 AC2: DeleteNode removes the row and yields ErrNotFound
// for a missing id.
func TestKGNodeDelete(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	created, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeFaction, Name: "The Cult",
	})
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}

	if err := st.DeleteNode(ctx, created.ID); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}
	nodes, err := st.ListNodes(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("node not deleted: %+v", nodes)
	}

	if err := st.DeleteNode(ctx, created.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("second DeleteNode err = %v, want ErrNotFound", err)
	}
	if err := st.DeleteNode(ctx, uuid.New()); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("DeleteNode unknown id err = %v, want ErrNotFound", err)
	}
}

// TestKGNodeCreateEditDeleteAcrossTypes is #129 AC5's storage grain: the full
// create → edit → delete lifecycle across two distinct Node types (Location and
// Faction), with the gm_private toggle round-tripping through ListNodes.
func TestKGNodeCreateEditDeleteAcrossTypes(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	loc, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeLocation, Name: "Harbor", Body: "Ships dock here.",
	})
	if err != nil {
		t.Fatalf("CreateNode location: %v", err)
	}
	fac, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeFaction, Name: "Dockers Guild", Body: "They run the docks.",
	})
	if err != nil {
		t.Fatalf("CreateNode faction: %v", err)
	}

	// Edit both: flip the Faction to gm_private, leave the Location public.
	if _, err := st.UpdateNode(ctx, storage.KGNodeUpdate{
		ID: loc.ID, Name: "Old Harbor", Body: "Ships used to dock here.",
	}); err != nil {
		t.Fatalf("UpdateNode location: %v", err)
	}
	if _, err := st.UpdateNode(ctx, storage.KGNodeUpdate{
		ID: fac.ID, Name: "Dockers Guild", Body: "A secret cabal.", GMPrivate: true,
	}); err != nil {
		t.Fatalf("UpdateNode faction: %v", err)
	}

	// The edits round-trip across both types: the Location stays public with its
	// new name/body, the Faction flips to gm_private with its new body. ListNodes
	// orders by node_type enum (location < faction), so the Location is first.
	afterEdit, err := st.ListNodes(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListNodes after edit: %v", err)
	}
	if len(afterEdit) != 2 {
		t.Fatalf("after edit want 2 nodes, got %+v", afterEdit)
	}
	if afterEdit[0].ID != loc.ID || afterEdit[0].Type != storage.KGNodeLocation ||
		afterEdit[0].Name != "Old Harbor" || afterEdit[0].GMPrivate {
		t.Errorf("location edit not persisted: %+v", afterEdit[0])
	}
	if afterEdit[1].ID != fac.ID || afterEdit[1].Type != storage.KGNodeFaction ||
		afterEdit[1].Body != "A secret cabal." || !afterEdit[1].GMPrivate {
		t.Errorf("faction edit not persisted: %+v", afterEdit[1])
	}

	// Delete the Location; only the (private) Faction remains.
	if err := st.DeleteNode(ctx, loc.ID); err != nil {
		t.Fatalf("DeleteNode location: %v", err)
	}
	all, err := st.ListNodes(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(all) != 1 || all[0].ID != fac.ID || !all[0].GMPrivate {
		t.Errorf("after delete want only the private Faction, got %+v", all)
	}
}
