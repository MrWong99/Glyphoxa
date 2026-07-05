//go:build integration

package storage_test

import (
	"context"
	"testing"

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

// TestKGNodeListPublicNodes is #126 AC3: the prompt-injection read excludes
// gm_private Nodes and orders by updated_at DESC — the newest public Node first.
func TestKGNodeListPublicNodes(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// Two public and one private. Force distinct updated_at ordering by inserting
	// in a known sequence and stamping updated_at explicitly for determinism.
	pubOld, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeNote, Name: "Old public", Body: "old",
	})
	if err != nil {
		t.Fatalf("CreateNode pubOld: %v", err)
	}
	if _, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeNote, Name: "Secret", Body: "hidden", GMPrivate: true,
	}); err != nil {
		t.Fatalf("CreateNode secret: %v", err)
	}
	pubNew, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeNote, Name: "New public", Body: "new",
	})
	if err != nil {
		t.Fatalf("CreateNode pubNew: %v", err)
	}
	// Make pubNew strictly newer than pubOld regardless of insert-time granularity.
	if _, err := pool.Exec(ctx,
		`UPDATE kg_node SET updated_at = now() + interval '1 hour' WHERE id = $1`, pubNew.ID); err != nil {
		t.Fatalf("bump pubNew updated_at: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE kg_node SET updated_at = now() - interval '1 hour' WHERE id = $1`, pubOld.ID); err != nil {
		t.Fatalf("bump pubOld updated_at: %v", err)
	}

	pub, err := st.ListPublicNodes(ctx, campaignID)
	if err != nil {
		t.Fatalf("ListPublicNodes: %v", err)
	}
	if len(pub) != 2 {
		t.Fatalf("ListPublicNodes returned %d, want 2 (gm_private excluded)", len(pub))
	}
	if pub[0].Name != "New public" || pub[1].Name != "Old public" {
		t.Errorf("order = [%q %q], want [New public, Old public] (updated_at DESC)", pub[0].Name, pub[1].Name)
	}
	for _, n := range pub {
		if n.GMPrivate {
			t.Errorf("ListPublicNodes leaked a gm_private node: %+v", n)
		}
	}
}
