//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// The Party Marker (#540, ADR-0060). voice_sessions carries only plain
// single-column FKs to campaign_map and map_pin, so the same-campaign integrity
// every other table gets declaratively has to be asserted in the write's own
// statement — and therefore tested against a real Postgres.

// TestSetPartyMarker_RefusesAForeignCampaignsMap: the marker's Map must belong to
// the session's Campaign.
//
// The FK alone only proves the uuid is a map SOMEWHERE. Without the EXISTS guard a
// marker could point into another Tenant's world, and GetPartyMarker's LEFT JOIN
// would then read that map's NAME straight into the location clause an NPC speaks
// at the table.
func TestSetPartyMarker_RefusesAForeignCampaignsMap(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignA := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// A second Campaign on the same Postgres — the marker must not reach into it.
	poolB, _, campaignB := seedCampaign(t, dsn)
	foreign := mkMap(t, storage.New(poolB), campaignB, "The Enemy Capital")
	sess, err := st.CreateVoiceSession(ctx, campaignA)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}

	err = st.SetPartyMarker(ctx, campaignA, sess.ID,
		uuid.NullUUID{UUID: foreign.ID, Valid: true}, uuid.NullUUID{}, nil, nil)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("a foreign campaign's map was accepted: err = %v", err)
	}

	marker, err := st.GetPartyMarker(ctx, campaignA, sess.ID)
	if err != nil {
		t.Fatalf("GetPartyMarker: %v", err)
	}
	if marker.Set() {
		t.Fatalf("the refused write still landed: %+v", marker)
	}
}

// TestSetPartyMarker_RefusesAPinFromAnotherMap: a Pin must be ON the Map the
// marker names. Otherwise the clause reads out a pin's label next to an unrelated
// map's name — a location that does not exist.
func TestSetPartyMarker_RefusesAPinFromAnotherMap(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	city := mkMap(t, st, campaignID, "Saltmarsh")
	tavern := mkMap(t, st, campaignID, "The Rusty Anchor")
	node := mkNode(t, st, campaignID, storage.KGNodeLocation, "The cellar")
	pinOnTavern, err := st.CreatePin(ctx, storage.NewMapPin{
		MapID: tavern.ID, CampaignID: campaignID, NodeID: node.ID, X: 0.5, Y: 0.5,
	})
	if err != nil {
		t.Fatalf("CreatePin: %v", err)
	}
	sess, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}

	err = st.SetPartyMarker(ctx, campaignID, sess.ID,
		uuid.NullUUID{UUID: city.ID, Valid: true},
		uuid.NullUUID{UUID: pinOnTavern.ID, Valid: true}, nil, nil)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("a pin from another map was accepted: err = %v", err)
	}
}

// TestSetPartyMarker_RoundTrips: the happy path, and the clear.
func TestSetPartyMarker_RoundTrips(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	city := mkMap(t, st, campaignID, "Saltmarsh")
	node := mkNode(t, st, campaignID, storage.KGNodeLocation, "The docks")
	pin, err := st.CreatePin(ctx, storage.NewMapPin{
		MapID: city.ID, CampaignID: campaignID, NodeID: node.ID, X: 0.4, Y: 0.6,
	})
	if err != nil {
		t.Fatalf("CreatePin: %v", err)
	}
	sess, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}

	if err := st.SetPartyMarker(ctx, campaignID, sess.ID,
		uuid.NullUUID{UUID: city.ID, Valid: true},
		uuid.NullUUID{UUID: pin.ID, Valid: true}, nil, nil); err != nil {
		t.Fatalf("SetPartyMarker: %v", err)
	}
	marker, err := st.GetPartyMarker(ctx, campaignID, sess.ID)
	if err != nil {
		t.Fatalf("GetPartyMarker: %v", err)
	}
	if !marker.Set() || marker.MapName != "Saltmarsh" || marker.PinLabel != "The docks" {
		t.Fatalf("marker did not round-trip: %+v", marker)
	}

	if err := st.SetPartyMarker(ctx, campaignID, sess.ID,
		uuid.NullUUID{}, uuid.NullUUID{}, nil, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	marker, err = st.GetPartyMarker(ctx, campaignID, sess.ID)
	if err != nil {
		t.Fatalf("GetPartyMarker after clear: %v", err)
	}
	if marker.Set() {
		t.Fatalf("clear left a marker behind: %+v", marker)
	}
}

// TestSetPartyMarker_IsPerSession: two concurrent Voice Sessions in the same
// Campaign never see each other's marker (#540 AC1). The marker lives on the
// session row precisely so two tables running the same module do not teleport each
// other's party.
func TestSetPartyMarker_IsPerSession(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	city := mkMap(t, st, campaignID, "Saltmarsh")
	other := mkMap(t, st, campaignID, "The Moathouse")
	one, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}
	two, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}

	if err := st.SetPartyMarker(ctx, campaignID, one.ID,
		uuid.NullUUID{UUID: city.ID, Valid: true}, uuid.NullUUID{}, nil, nil); err != nil {
		t.Fatalf("SetPartyMarker one: %v", err)
	}
	if err := st.SetPartyMarker(ctx, campaignID, two.ID,
		uuid.NullUUID{UUID: other.ID, Valid: true}, uuid.NullUUID{}, nil, nil); err != nil {
		t.Fatalf("SetPartyMarker two: %v", err)
	}

	m1, err := st.GetPartyMarker(ctx, campaignID, one.ID)
	if err != nil {
		t.Fatalf("GetPartyMarker one: %v", err)
	}
	m2, err := st.GetPartyMarker(ctx, campaignID, two.ID)
	if err != nil {
		t.Fatalf("GetPartyMarker two: %v", err)
	}
	if m1.MapName != "Saltmarsh" || m2.MapName != "The Moathouse" {
		t.Fatalf("sessions share a marker: %q vs %q", m1.MapName, m2.MapName)
	}
}

// TestPinsNear_OrdersByDistanceAndHidesSecrets: the prompt-facing proximity read.
// publicOnly is what keeps a GM secret out of an NPC's answer, and the ordering is
// what makes the cap ("the 8 nearest") mean something.
func TestPinsNear_OrdersByDistanceAndHidesSecrets(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	city := mkMap(t, st, campaignID, "Saltmarsh")
	near := mkNode(t, st, campaignID, storage.KGNodeLocation, "The docks")
	far := mkNode(t, st, campaignID, storage.KGNodeLocation, "The north gate")
	secretNode := mkNode(t, st, campaignID, storage.KGNodeItem, "A loose floorboard")

	mustPin := func(nodeID uuid.UUID, x, y float64, hidden bool) {
		t.Helper()
		if _, err := st.CreatePin(ctx, storage.NewMapPin{
			MapID: city.ID, CampaignID: campaignID, NodeID: nodeID, X: x, Y: y, GMPrivate: hidden,
		}); err != nil {
			t.Fatalf("CreatePin: %v", err)
		}
	}
	mustPin(near.ID, 0.52, 0.5, false)
	mustPin(far.ID, 0.68, 0.5, false)
	mustPin(secretNode.ID, 0.51, 0.5, true) // nearest of all, and must not appear

	pins, err := st.PinsNear(ctx, campaignID, city.ID, 0.5, 0.5, 0.3, true, 10)
	if err != nil {
		t.Fatalf("PinsNear: %v", err)
	}
	if len(pins) != 2 {
		t.Fatalf("want the two public pins, got %d: %+v", len(pins), pins)
	}
	if pins[0].NodeID != near.ID || pins[1].NodeID != far.ID {
		t.Errorf("not ordered by distance: %+v", pins)
	}
	for _, p := range pins {
		if p.NodeID == secretNode.ID {
			t.Fatal("a gm_private pin reached the prompt-facing read")
		}
	}

	// The radius is a real bound, not decoration.
	tight, err := st.PinsNear(ctx, campaignID, city.ID, 0.5, 0.5, 0.05, true, 10)
	if err != nil {
		t.Fatalf("PinsNear tight: %v", err)
	}
	if len(tight) != 1 || tight[0].NodeID != near.ID {
		t.Fatalf("radius not applied: %+v", tight)
	}
}
