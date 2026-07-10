package presence

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

type fakeConsentStore struct {
	upserts []consentCall
	deletes []consentCall
	err     error
}

type consentCall struct {
	campaignID uuid.UUID
	userID     string
}

func (f *fakeConsentStore) UpsertTapeConsent(_ context.Context, campaignID uuid.UUID, userID string) error {
	if f.err != nil {
		return f.err
	}
	f.upserts = append(f.upserts, consentCall{campaignID, userID})
	return nil
}

func (f *fakeConsentStore) DeleteTapeConsent(_ context.Context, campaignID uuid.UUID, userID string) error {
	if f.err != nil {
		return f.err
	}
	f.deletes = append(f.deletes, consentCall{campaignID, userID})
	return nil
}

func captureConsent(bus *voiceevent.Bus) *[]voiceevent.TapeConsentChanged {
	var got []voiceevent.TapeConsentChanged
	voiceevent.On(bus, func(e voiceevent.TapeConsentChanged) { got = append(got, e) })
	return &got
}

// TestConsentButtons_Grant pins the grant path (#306): a Consent button press
// writes the row, publishes TapeConsentChanged{Granted:true} for the pressing
// Speaker on that Campaign, and returns an ephemeral confirmation.
func TestConsentButtons_Grant(t *testing.T) {
	store := &fakeConsentStore{}
	bus := voiceevent.NewBus()
	events := captureConsent(bus)
	cb := NewConsentButtons(store, bus, nil)

	cid := uuid.New()
	customID := tapeGrantCustomIDForTest(cid)
	reply, ok := cb.apply(context.Background(), customID, "player-42")
	if !ok {
		t.Fatalf("apply ok = false, want true for a tape button")
	}
	if reply == "" {
		t.Fatalf("no ephemeral reply on grant")
	}
	if len(store.upserts) != 1 || store.upserts[0].campaignID != cid || store.upserts[0].userID != "player-42" {
		t.Fatalf("upserts = %+v, want one for (cid, player-42)", store.upserts)
	}
	if len(store.deletes) != 0 {
		t.Fatalf("unexpected deletes on grant: %+v", store.deletes)
	}
	if len(*events) != 1 {
		t.Fatalf("published %d events, want 1", len(*events))
	}
	e := (*events)[0]
	if !e.Granted || e.SpeakerID != "player-42" || e.CampaignID != cid.String() {
		t.Fatalf("event = %+v, want grant for player-42 on %s", e, cid)
	}
}

// TestConsentButtons_Revoke pins the revoke path: a Revoke press deletes the row
// and publishes TapeConsentChanged{Granted:false}.
func TestConsentButtons_Revoke(t *testing.T) {
	store := &fakeConsentStore{}
	bus := voiceevent.NewBus()
	events := captureConsent(bus)
	cb := NewConsentButtons(store, bus, nil)

	cid := uuid.New()
	reply, ok := cb.apply(context.Background(), tapeRevokeCustomIDForTest(cid), "player-7")
	if !ok || reply == "" {
		t.Fatalf("apply = (%q, %v), want a reply and ok", reply, ok)
	}
	if len(store.deletes) != 1 || store.deletes[0].userID != "player-7" {
		t.Fatalf("deletes = %+v, want one for player-7", store.deletes)
	}
	if len(store.upserts) != 0 {
		t.Fatalf("unexpected upserts on revoke: %+v", store.upserts)
	}
	if len(*events) != 1 || (*events)[0].Granted {
		t.Fatalf("events = %+v, want one revoke", *events)
	}
}

// TestConsentButtons_ForeignButtonIgnored pins that a non-tape custom id is not
// ours: apply reports ok=false and touches neither storage nor the bus.
func TestConsentButtons_ForeignButtonIgnored(t *testing.T) {
	store := &fakeConsentStore{}
	bus := voiceevent.NewBus()
	events := captureConsent(bus)
	cb := NewConsentButtons(store, bus, nil)

	if _, ok := cb.apply(context.Background(), "gx:mute:agent:123", "player-1"); ok {
		t.Fatalf("apply ok = true for a foreign button, want false")
	}
	if len(store.upserts)+len(store.deletes) != 0 || len(*events) != 0 {
		t.Fatalf("foreign button touched state: upserts=%v deletes=%v events=%v", store.upserts, store.deletes, *events)
	}
}

// TestConsentButtons_StorageFailurePublishesNothing pins that a failed durable
// write does NOT publish a consent change (the live tape must not diverge from the
// unchanged durable state), but still owns the interaction (ok=true, apologetic
// reply).
func TestConsentButtons_StorageFailurePublishesNothing(t *testing.T) {
	store := &fakeConsentStore{err: errors.New("db down")}
	bus := voiceevent.NewBus()
	events := captureConsent(bus)
	cb := NewConsentButtons(store, bus, nil)

	reply, ok := cb.apply(context.Background(), tapeGrantCustomIDForTest(uuid.New()), "player-9")
	if !ok || reply == "" {
		t.Fatalf("apply = (%q, %v), want an apologetic reply and ok", reply, ok)
	}
	if len(*events) != 0 {
		t.Fatalf("published on storage failure: %+v", *events)
	}
}

// tapeGrantCustomIDForTest / tapeRevokeCustomIDForTest build custom ids matching
// the wirenpc scheme, so this test does not depend on wirenpc's unexported
// builders. They must stay in sync with wirenpc.ParseTapeConsentCustomID (pinned
// by the round-trip below).
func tapeGrantCustomIDForTest(cid uuid.UUID) string  { return "gx:tape:grant:" + cid.String() }
func tapeRevokeCustomIDForTest(cid uuid.UUID) string { return "gx:tape:revoke:" + cid.String() }

// TestConsentCustomIDSchemeInSync guards against the two packages drifting: the
// ids this test builds must parse as wirenpc intends.
func TestConsentCustomIDSchemeInSync(t *testing.T) {
	cid := uuid.New()
	if id, granted, ok := wirenpc.ParseTapeConsentCustomID(tapeGrantCustomIDForTest(cid)); !ok || !granted || id != cid {
		t.Fatalf("grant id out of sync: (%v,%v,%v)", id, granted, ok)
	}
	if id, granted, ok := wirenpc.ParseTapeConsentCustomID(tapeRevokeCustomIDForTest(cid)); !ok || granted || id != cid {
		t.Fatalf("revoke id out of sync: (%v,%v,%v)", id, granted, ok)
	}
}
