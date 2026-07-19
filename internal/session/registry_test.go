package session_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// startedManagerOnBus builds a voice-enabled Manager sharing the process bus,
// registers it in reg, starts a session for campaignID, and returns the started
// row. The blocking runner keeps the session live until the test's Stop/cleanup.
func startedManagerOnBus(t *testing.T, reg *session.Registry, bus *voiceevent.Bus, campaignID uuid.UUID) (*session.Manager, storage.VoiceSession) {
	t.Helper()
	store := newFakeStore()
	runner := newBlockingRunner()
	mgr := session.NewManager(store, runner.run, wirenpc.Config{Token: "test-token", Bus: bus}, nil,
		slog.New(slog.DiscardHandler), true, session.Deps{Registry: reg})
	vs, err := mgr.Start(context.Background(), uuid.New(), campaignID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-runner.started
	t.Cleanup(func() { _, _ = mgr.Stop(context.Background()) })
	return mgr, vs
}

// TestRegistry_TwoManagersNoPanic pins the double-bind fix (AC2): two Managers
// registering with one Registry no longer panic (the old View.bind CAS did), and
// each session Resolves by its own id.
func TestRegistry_TwoManagersNoPanic(t *testing.T) {
	reg := session.NewRegistry()
	bus := voiceevent.NewBus()

	_, vsA := startedManagerOnBus(t, reg, bus, uuid.New())
	_, vsB := startedManagerOnBus(t, reg, bus, uuid.New())

	if got, ok := reg.Resolve(vsA.ID); !ok || got.ID != vsA.ID || got.CampaignID != vsA.CampaignID {
		t.Errorf("Resolve(A) = (%+v, %v), want session A", got, ok)
	}
	if got, ok := reg.Resolve(vsB.ID); !ok || got.ID != vsB.ID || got.CampaignID != vsB.CampaignID {
		t.Errorf("Resolve(B) = (%+v, %v), want session B", got, ok)
	}
}

// TestRegistry_ResolveUnknownFalse pins that an id no live session carries reports
// false (a straggler / pre-registry event's drop signal).
func TestRegistry_ResolveUnknownFalse(t *testing.T) {
	reg := session.NewRegistry()
	if got, ok := reg.Resolve(uuid.New()); ok {
		t.Errorf("Resolve(unknown) = (%+v, true), want (_, false)", got)
	}
}

// TestRegistry_PublishToCampaignRoutesToLiveSessionBus pins AC1/AC3 routing: a
// PublishToCampaign lands on ONLY the matching session's bus, so — bridged onto
// the process bus by Forward — the event arrives stamped with THAT session's id
// and never the other's (no cross-session leakage).
func TestRegistry_PublishToCampaignRoutesToLiveSessionBus(t *testing.T) {
	reg := session.NewRegistry()
	bus := voiceevent.NewBus()

	campA, campB := uuid.New(), uuid.New()
	_, vsA := startedManagerOnBus(t, reg, bus, campA)
	_, vsB := startedManagerOnBus(t, reg, bus, campB)

	var got []voiceevent.Event
	t.Cleanup(bus.Subscribe(func(e voiceevent.Event) { got = append(got, e) }))

	if !reg.PublishToCampaign(campA, voiceevent.TapeConsentChanged{CampaignID: campA.String(), SpeakerID: "u1", Granted: true}) {
		t.Fatal("PublishToCampaign(A) reported no live session, want true")
	}

	if len(got) != 1 {
		t.Fatalf("process bus got %d events, want 1 (routed to session A only)", len(got))
	}
	if sid := voiceevent.SessionIDOf(got[0]); sid != vsA.ID.String() {
		t.Errorf("routed event stamped %q, want session A %q (not B %q)", sid, vsA.ID.String(), vsB.ID.String())
	}
}

// TestRegistry_PublishToCampaignFalseWhenNone pins the no-live-session path: a
// campaign with no running session reports false and publishes nothing.
func TestRegistry_PublishToCampaignFalseWhenNone(t *testing.T) {
	reg := session.NewRegistry()
	bus := voiceevent.NewBus()
	_, _ = startedManagerOnBus(t, reg, bus, uuid.New())

	var got int
	t.Cleanup(bus.Subscribe(func(voiceevent.Event) { got++ }))

	if reg.PublishToCampaign(uuid.New(), voiceevent.TapeConsentChanged{}) {
		t.Error("PublishToCampaign(no live session) = true, want false")
	}
	// give any errant Forward a beat (synchronous bus, so this is belt-and-suspenders)
	time.Sleep(5 * time.Millisecond)
	if got != 0 {
		t.Errorf("process bus got %d events for an unrouted publish, want 0", got)
	}
}

// TestIdentityContext_RoundTrip pins the run-context Identity seam (#487): the
// non-bus per-turn consumers (recall Recall, KG-facts) recover the session,
// campaign and tenant from the context Manager.Start installs; a bare context
// reports absent (the bench / voice-standalone "no session to scope" signal).
func TestIdentityContext_RoundTrip(t *testing.T) {
	id := session.Identity{SessionID: uuid.New(), CampaignID: uuid.New(), TenantID: uuid.New()}
	ctx := session.NewContext(context.Background(), id)
	got, ok := session.FromContext(ctx)
	if !ok || got != id {
		t.Fatalf("FromContext = (%+v, %v), want (%+v, true)", got, ok, id)
	}
	if _, ok := session.FromContext(context.Background()); ok {
		t.Error("FromContext(bare) reported present, want absent")
	}
}

// TestStart_SessionEventsStampedOnProcessBus_AndRunCtxCarriesIdentity pins the
// Start wiring (#487): the loop runs with cfg.Bus pointing at the session's OWN
// bus, an event it publishes there arrives on the PROCESS bus stamped with the
// session id (via Forward), and the run context carries the session Identity for
// the non-bus per-turn consumers.
func TestStart_SessionEventsStampedOnProcessBus_AndRunCtxCarriesIdentity(t *testing.T) {
	reg := session.NewRegistry()
	procBus := voiceevent.NewBus()
	// Capture forwarded events through a channel, not a shared slice: the Forward
	// bridge republishes on the Manager's runLoop goroutine, so a plain slice append
	// would race the test's read (the channel receive is the happens-after edge).
	published := make(chan voiceevent.Event, 4)
	t.Cleanup(procBus.Subscribe(func(e voiceevent.Event) { published <- e }))

	tenantID, campaignID := uuid.New(), uuid.New()
	gotID := make(chan session.Identity, 1)
	store := newFakeStore()
	runner := func(ctx context.Context, cfg wirenpc.Config) error {
		id, _ := session.FromContext(ctx)
		gotID <- id
		cfg.Bus.Publish(voiceevent.STTFinal{Text: "hi from the session loop"})
		<-ctx.Done()
		return ctx.Err()
	}
	mgr := session.NewManager(store, runner, wirenpc.Config{Token: "test-token", Bus: procBus}, nil,
		slog.New(slog.DiscardHandler), true, session.Deps{Registry: reg})
	vs, err := mgr.Start(context.Background(), tenantID, campaignID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _, _ = mgr.Stop(context.Background()) })

	id := <-gotID
	want := session.Identity{SessionID: vs.ID, CampaignID: campaignID, TenantID: tenantID}
	if id != want {
		t.Errorf("run ctx Identity = %+v, want %+v", id, want)
	}

	select {
	case ev := <-published:
		if _, ok := ev.(voiceevent.STTFinal); !ok {
			t.Fatalf("process bus event = %T, want STTFinal", ev)
		}
		if sid := voiceevent.SessionIDOf(ev); sid != vs.ID.String() {
			t.Errorf("session loop event stamped %q, want %q", sid, vs.ID.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event arrived on the process bus (the loop's STTFinal, stamped)")
	}
}
