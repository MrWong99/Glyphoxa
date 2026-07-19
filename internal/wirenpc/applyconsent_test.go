package wirenpc

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// erroringConsentStore fails every write, to pin the storage-failure path.
type erroringConsentStore struct{}

func (erroringConsentStore) ListTapeConsent(context.Context, uuid.UUID) ([]string, error) {
	return nil, nil
}
func (erroringConsentStore) UpsertTapeConsent(context.Context, uuid.UUID, string) error {
	return errors.New("db down")
}
func (erroringConsentStore) DeleteTapeConsent(context.Context, uuid.UUID, string) error {
	return errors.New("db down")
}

func captureConsent(bus *voiceevent.Bus) *[]voiceevent.TapeConsentChanged {
	var got []voiceevent.TapeConsentChanged
	voiceevent.On(bus, func(e voiceevent.TapeConsentChanged) { got = append(got, e) })
	return &got
}

// TestApplyTapeConsent_Grant pins the grant path (#306): the row is written and
// TapeConsentChanged{Granted:true} is published AFTER the write, with a reply.
func TestApplyTapeConsent_Grant(t *testing.T) {
	store := newFakeConsentStore()
	bus := voiceevent.NewBus()
	events := captureConsent(bus)
	cid := uuid.New()

	reply, ok := ApplyTapeConsent(context.Background(), store, BusPublisher{Bus: bus}, time.Now, discardLog(), tapeGrantCustomID(cid), "player-42")
	if !ok || reply == "" {
		t.Fatalf("apply = (%q, %v), want a reply and ok", reply, ok)
	}
	if store.upserts != 1 {
		t.Fatalf("upserts = %d, want 1", store.upserts)
	}
	if got, _ := store.ListTapeConsent(context.Background(), cid); len(got) != 1 || got[0] != "player-42" {
		t.Fatalf("consent row not written: %v", got)
	}
	if len(*events) != 1 || !(*events)[0].Granted || (*events)[0].SpeakerID != "player-42" || (*events)[0].CampaignID != cid.String() {
		t.Fatalf("event = %+v, want one grant for player-42 on %s", *events, cid)
	}
}

// TestApplyTapeConsent_Revoke pins the revoke path: the row is deleted and
// TapeConsentChanged{Granted:false} is published.
func TestApplyTapeConsent_Revoke(t *testing.T) {
	store := newFakeConsentStore()
	cid := uuid.New()
	store.set(cid, "player-7")
	bus := voiceevent.NewBus()
	events := captureConsent(bus)

	reply, ok := ApplyTapeConsent(context.Background(), store, BusPublisher{Bus: bus}, time.Now, discardLog(), tapeRevokeCustomID(cid), "player-7")
	if !ok || reply == "" {
		t.Fatalf("apply = (%q, %v), want a reply and ok", reply, ok)
	}
	if store.deletes != 1 {
		t.Fatalf("deletes = %d, want 1", store.deletes)
	}
	if got, _ := store.ListTapeConsent(context.Background(), cid); len(got) != 0 {
		t.Fatalf("consent row not deleted: %v", got)
	}
	if len(*events) != 1 || (*events)[0].Granted {
		t.Fatalf("events = %+v, want one revoke", *events)
	}
}

// TestApplyTapeConsent_ForeignButtonIgnored pins that a non-tape custom id is not
// ours: ok=false, no storage or bus touched.
func TestApplyTapeConsent_ForeignButtonIgnored(t *testing.T) {
	store := newFakeConsentStore()
	bus := voiceevent.NewBus()
	events := captureConsent(bus)

	if _, ok := ApplyTapeConsent(context.Background(), store, BusPublisher{Bus: bus}, time.Now, discardLog(), "gx:mute:agent:123", "p1"); ok {
		t.Fatalf("ok = true for a foreign button, want false")
	}
	if store.upserts+store.deletes != 0 || len(*events) != 0 {
		t.Fatalf("foreign button touched state")
	}
}

// TestApplyTapeConsent_StorageFailurePublishesNothing pins that a failed durable
// write publishes nothing (the live tape must not diverge from the unchanged DB)
// but still owns the interaction (ok=true, apologetic reply).
func TestApplyTapeConsent_StorageFailurePublishesNothing(t *testing.T) {
	store := erroringConsentStore{}
	bus := voiceevent.NewBus()
	events := captureConsent(bus)

	var logbuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelError}))

	reply, ok := ApplyTapeConsent(context.Background(), store, BusPublisher{Bus: bus}, time.Now, log, tapeGrantCustomID(uuid.New()), "p9")
	if !ok || reply != tapeConsentFailedReply {
		t.Fatalf("apply = (%q, %v), want the failure reply and ok", reply, ok)
	}
	if len(*events) != 0 {
		t.Fatalf("published on storage failure: %+v", *events)
	}
	// The operator must be able to diagnose the failure (finding 4): it is logged.
	if !strings.Contains(logbuf.String(), "consent") {
		t.Fatalf("storage failure not logged; log = %q", logbuf.String())
	}
}
