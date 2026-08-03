package presence

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// /where (#540, ADR-0060). The command is GM-only and ephemeral, and the state it
// writes is read out loud by NPCs on the next turn — so what is tested is the
// resolution (right map, right pin, right refusal) and the GM-only posture.

type markerCall struct {
	mapID uuid.NullUUID
	pinID uuid.NullUUID
}

type fakeWhere struct {
	active     bool
	tenantID   uuid.UUID
	campaignID uuid.UUID
	sessionID  uuid.UUID

	maps     []storage.CampaignMap
	pins     []storage.MapPin
	setErr   error
	setCalls []markerCall
}

func (f *fakeWhere) Active(_ context.Context, tenantID uuid.UUID) (storage.VoiceSession, bool, error) {
	if !f.active || tenantID != f.tenantID {
		return storage.VoiceSession{}, false, nil
	}
	return storage.VoiceSession{ID: f.sessionID, CampaignID: f.campaignID}, true, nil
}

func (f *fakeWhere) ListMaps(context.Context, uuid.UUID) ([]storage.CampaignMap, error) {
	return f.maps, nil
}

func (f *fakeWhere) ListPins(context.Context, uuid.UUID, uuid.UUID) ([]storage.MapPin, error) {
	return f.pins, nil
}

func (f *fakeWhere) ResolveMapByName(_ context.Context, _ uuid.UUID, name string) (storage.CampaignMap, error) {
	var found []storage.CampaignMap
	for _, m := range f.maps {
		if strings.EqualFold(strings.TrimSpace(m.Name), strings.TrimSpace(name)) {
			found = append(found, m)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return storage.CampaignMap{}, storage.ErrNotFound
	default:
		return storage.CampaignMap{}, storage.ErrConflict
	}
}

func (f *fakeWhere) ResolvePinByLabel(_ context.Context, _, mapID uuid.UUID, label string) (storage.MapPin, error) {
	for _, p := range f.pins {
		if p.MapID == mapID && strings.EqualFold(p.Label(), strings.TrimSpace(label)) {
			return p, nil
		}
	}
	return storage.MapPin{}, storage.ErrNotFound
}

func (f *fakeWhere) SetPartyMarker(_ context.Context, _, _ uuid.UUID, mapID, pinID uuid.NullUUID, _, _ *float64) error {
	f.setCalls = append(f.setCalls, markerCall{mapID, pinID})
	return f.setErr
}

func whereWorld() *fakeWhere {
	town := uuid.New()
	return &fakeWhere{
		active: true, tenantID: tenantA, campaignID: uuid.New(), sessionID: uuid.New(),
		maps: []storage.CampaignMap{
			{ID: town, Name: "Saltmarsh"},
			{ID: uuid.New(), Name: "The Vault", GMPrivate: true},
		},
		pins: []storage.MapPin{
			{ID: uuid.New(), MapID: town, NodeName: "The Rusty Anchor"},
		},
	}
}

func whereIC(f *fakeWhere, resp *fakeResponder, mapName, at string) *Interaction {
	opts := fakeOpts{s: map[string]string{"map": mapName}, i: map[string]int{}}
	if at != "" {
		opts.s["at"] = at
	}
	return &Interaction{guildID: testGuild, userID: operatorID, tenantID: f.tenantID, opts: opts, resp: resp}
}

// TestWhereIsGMOnly: where the party is going next is the GM's information.
func TestWhereIsGMOnly(t *testing.T) {
	if !WhereCommand(whereWorld(), whereWorld()).GMOnly {
		t.Fatal("/where must be GM-only")
	}
}

// TestWhere_PlacesThePartyOnAMap: the happy path writes the resolved Map with no
// pin — "somewhere on this map" is a legitimate placement.
func TestWhere_PlacesThePartyOnAMap(t *testing.T) {
	f := whereWorld()
	resp := &fakeResponder{}
	if err := WhereCommand(f, f).Handle(context.Background(), whereIC(f, resp, "saltmarsh", "")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(f.setCalls) != 1 {
		t.Fatalf("marker not written: %+v", f.setCalls)
	}
	if !f.setCalls[0].mapID.Valid || f.setCalls[0].mapID.UUID != f.maps[0].ID {
		t.Errorf("wrong map: %+v", f.setCalls[0])
	}
	if f.setCalls[0].pinID.Valid {
		t.Errorf("no pin was named, but one was written: %+v", f.setCalls[0])
	}
	if len(resp.replies) != 1 || !resp.replies[0].ephemeral {
		t.Errorf("the reply must be a single ephemeral one: %+v", resp.replies)
	}
}

// TestWhere_PlacesThePartyAtAPin: naming a pin writes the pin too, so the location
// clause can say "at the Rusty Anchor" rather than merely "in Saltmarsh".
func TestWhere_PlacesThePartyAtAPin(t *testing.T) {
	f := whereWorld()
	resp := &fakeResponder{}
	if err := WhereCommand(f, f).Handle(context.Background(), whereIC(f, resp, "Saltmarsh", "the rusty anchor")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(f.setCalls) != 1 || !f.setCalls[0].pinID.Valid {
		t.Fatalf("pin not written: %+v", f.setCalls)
	}
	if !strings.Contains(lastReply(resp), "Rusty Anchor") {
		t.Errorf("confirmation does not name the pin: %q", lastReply(resp))
	}
}

// TestWhere_ClearsTheMarker: "none" takes the party off the map entirely, which
// must write NULLs rather than resolving a map called "none".
func TestWhere_ClearsTheMarker(t *testing.T) {
	f := whereWorld()
	resp := &fakeResponder{}
	if err := WhereCommand(f, f).Handle(context.Background(), whereIC(f, resp, "none", "")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(f.setCalls) != 1 || f.setCalls[0].mapID.Valid || f.setCalls[0].pinID.Valid {
		t.Fatalf("clear did not write NULLs: %+v", f.setCalls)
	}
}

// TestWhere_UnknownMapIsRefused: a typo must not silently leave the party where it
// was while the GM believes it moved.
func TestWhere_UnknownMapIsRefused(t *testing.T) {
	f := whereWorld()
	resp := &fakeResponder{}
	if err := WhereCommand(f, f).Handle(context.Background(), whereIC(f, resp, "Saltmrsh", "")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(f.setCalls) != 0 {
		t.Fatalf("an unresolved map still wrote the marker: %+v", f.setCalls)
	}
	if !strings.Contains(lastReply(resp), "No map called") {
		t.Errorf("unhelpful refusal: %q", lastReply(resp))
	}
}

// TestWhere_UnknownPinIsRefusedWithoutMovingTheParty: naming a pin that is not on
// the map must refuse the WHOLE placement, not fall back to the bare map — the GM
// asked for a specific spot.
func TestWhere_UnknownPinIsRefusedWithoutMovingTheParty(t *testing.T) {
	f := whereWorld()
	resp := &fakeResponder{}
	if err := WhereCommand(f, f).Handle(context.Background(), whereIC(f, resp, "Saltmarsh", "The Black Gate")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(f.setCalls) != 0 {
		t.Fatalf("the party moved despite an unresolved pin: %+v", f.setCalls)
	}
}

// TestWhere_PrivateMapWarnsTheGM: the placement is honoured (the GM may well want
// the map view to follow), but the GM is told the NPCs will stay silent about it
// rather than being left to wonder why.
func TestWhere_PrivateMapWarnsTheGM(t *testing.T) {
	f := whereWorld()
	resp := &fakeResponder{}
	if err := WhereCommand(f, f).Handle(context.Background(), whereIC(f, resp, "The Vault", "")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(f.setCalls) != 1 {
		t.Fatalf("marker not written: %+v", f.setCalls)
	}
	if !strings.Contains(lastReply(resp), "GM-private") {
		t.Errorf("the GM was not warned the clause will be silent: %q", lastReply(resp))
	}
}

// TestWhere_NoSessionIsRefused: the marker is per Voice Session, so there is
// nothing to place without one.
func TestWhere_NoSessionIsRefused(t *testing.T) {
	f := whereWorld()
	f.active = false
	resp := &fakeResponder{}
	if err := WhereCommand(f, f).Handle(context.Background(), whereIC(f, resp, "Saltmarsh", "")); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(f.setCalls) != 0 {
		t.Fatalf("wrote a marker with no session: %+v", f.setCalls)
	}
	if !strings.Contains(lastReply(resp), "No Voice Session") {
		t.Errorf("unexpected reply: %q", lastReply(resp))
	}
}

// TestWhere_OtherTenantHasNoSession: the Active read is Tenant-keyed, so a GM in
// another Tenant can never move this party (#488).
func TestWhere_OtherTenantHasNoSession(t *testing.T) {
	f := whereWorld()
	resp := &fakeResponder{}
	ic := whereIC(f, resp, "Saltmarsh", "")
	ic.tenantID = uuid.New() // a different Tenant
	if err := WhereCommand(f, f).Handle(context.Background(), ic); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(f.setCalls) != 0 {
		t.Fatalf("a foreign tenant moved the party: %+v", f.setCalls)
	}
}

// lastReply is the content of the responder's most recent reply, or "" when it
// never answered — an empty string fails the Contains assertions loudly rather
// than panicking on an index.
func lastReply(r *fakeResponder) string {
	if len(r.replies) == 0 {
		return ""
	}
	return r.replies[len(r.replies)-1].content
}
