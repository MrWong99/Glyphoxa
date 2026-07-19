package wirenpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.uber.org/goleak"

	"github.com/MrWong99/Glyphoxa/internal/tape"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// fakeConsentStore is the authoritative consent surface the tape reseeds from, its
// per-campaign list settable so a test can simulate a grant/revoke landing in the
// DB out of band.
type fakeConsentStore struct {
	mu      sync.Mutex
	byCID   map[uuid.UUID][]string
	upserts int
	deletes int
}

func newFakeConsentStore() *fakeConsentStore {
	return &fakeConsentStore{byCID: map[uuid.UUID][]string{}}
}

func (f *fakeConsentStore) set(cid uuid.UUID, ids ...string) {
	f.mu.Lock()
	f.byCID[cid] = ids
	f.mu.Unlock()
}

func (f *fakeConsentStore) ListTapeConsent(_ context.Context, cid uuid.UUID) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.byCID[cid]...), nil
}

func (f *fakeConsentStore) UpsertTapeConsent(_ context.Context, cid uuid.UUID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts++
	f.byCID[cid] = append(f.byCID[cid], id)
	return nil
}

func (f *fakeConsentStore) DeleteTapeConsent(_ context.Context, cid uuid.UUID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletes++
	kept := f.byCID[cid][:0:0]
	for _, x := range f.byCID[cid] {
		if x != id {
			kept = append(kept, x)
		}
	}
	f.byCID[cid] = kept
	return nil
}

func laneCaptured(tp *tape.Tape, lane string, now time.Time) bool {
	tp.AppendInbound(lane, []byte{0x01}, now)
	snap := tp.Snapshot(now.Add(-time.Second), now.Add(time.Second))
	for _, l := range snap.Lanes {
		if l.LaneID == lane {
			return true
		}
	}
	return false
}

// TestTapeWiring_NilTapeWiresNothing pins the default-OFF contract (#306, ADR-0051):
// with no tape (campaign not armed) the loop wires no options and no consent sub,
// so it is byte-identical to the pre-tape path.
func TestTapeWiring_NilTapeWiresNothing(t *testing.T) {
	if got := tapeInboundOptions(nil); got != nil {
		t.Errorf("tapeInboundOptions(nil) = %v, want nil", got)
	}
	if got := tapePumpOptions(nil); got != nil {
		t.Errorf("tapePumpOptions(nil) = %v, want nil", got)
	}
	// wireTapeConsent(nil) returns an inert unsubscribe and subscribes nothing:
	// publishing an event must not panic.
	bus := voiceevent.NewBus()
	unsub := wireTapeConsent(context.Background(), bus, nil, uuid.New(), newFakeConsentStore(), 0, discardLog())
	bus.Publish(voiceevent.TapeConsentChanged{SpeakerID: "111", Granted: true})
	unsub()
}

// TestTapeWiring_ArmedWiresOptions pins that an armed tape produces exactly one
// inbound and one outbound tap option (the taps' end-to-end capture is covered by
// the pkg/voice/wire tap tests and the tape tests).
func TestTapeWiring_ArmedWiresOptions(t *testing.T) {
	tp := tape.New(tape.Window, []string{"111"}, nil)
	defer tp.Close()

	if got := len(tapeInboundOptions(tp)); got != 1 {
		t.Errorf("tapeInboundOptions armed = %d options, want 1", got)
	}
	if got := len(tapePumpOptions(tp)); got != 1 {
		t.Errorf("tapePumpOptions armed = %d options, want 1", got)
	}
}

// TestTapeWiring_ArmedTapeNoStoreDoesNotPanic pins finding 3: a wiring bug where
// the tape is armed but no consent reader was threaded through must not panic at
// cycle start — it returns an inert unsubscribe (and logs).
func TestTapeWiring_ArmedTapeNoStoreDoesNotPanic(t *testing.T) {
	tp := tape.New(tape.Window, []string{"111"}, nil)
	defer tp.Close()
	bus := voiceevent.NewBus()

	unsub := wireTapeConsent(context.Background(), bus, tp, uuid.New(), nil, 0, discardLog())
	// Publishing must not reach a nil-store reconcile.
	bus.Publish(voiceevent.TapeConsentChanged{CampaignID: uuid.New().String(), SpeakerID: "111", Granted: true})
	unsub()
}

// TestTapeWiring_ReseedsAndReconcilesAuthoritatively pins findings 2+4: the consent
// sub reseeds the tape from the DURABLE store at cycle start (a revoke that landed
// during a reconnect gap still takes effect), and on each event re-reads the store
// rather than trusting the event payload (so a stale/reordered Granted can't leave
// the tape armed while the DB says revoked).
func TestTapeWiring_ReseedsAndReconcilesAuthoritatively(t *testing.T) {
	cid := uuid.New()
	store := newFakeConsentStore()
	store.set(cid, "111") // durable truth at cycle start: 111 consents

	tp := tape.New(tape.Window, nil, nil) // fresh tape, nothing seeded in-memory
	defer tp.Close()
	bus := voiceevent.NewBus()
	// A long interval so the poller never fires here — this test exercises the seed +
	// bus fast path; a cancellable ctx stops the poller goroutine at test end.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	unsub := wireTapeConsent(ctx, bus, tp, cid, store, time.Hour, discardLog())
	defer unsub()

	// Reseed at cycle start armed 111 from the store (finding 2).
	if !laneCaptured(tp, "111", time.Now()) {
		t.Fatalf("cycle-start reseed did not arm 111 from the store")
	}

	// Durable truth changes to "nobody" (a revoke). The event carries a STALE
	// Granted:true, but the handler must re-read the store (finding 4) and clear.
	store.set(cid) // now empty
	bus.Publish(voiceevent.TapeConsentChanged{CampaignID: cid.String(), SpeakerID: "111", Granted: true})
	if laneCaptured(tp, "111", time.Now().Add(time.Second)) {
		t.Fatalf("handler trusted stale Granted payload instead of re-reading the store")
	}
}

// TestTapeWiring_IgnoresOtherCampaign pins finding 1: a consent press against a
// stale disclosure for a DIFFERENT campaign (a reused channel) must not touch this
// session's tape. The event is filtered by CampaignID before any store re-read.
func TestTapeWiring_IgnoresOtherCampaign(t *testing.T) {
	sessionCID := uuid.New()
	otherCID := uuid.New()
	store := newFakeConsentStore()
	store.set(sessionCID, "111") // this session's 111 consents

	tp := tape.New(tape.Window, nil, nil)
	defer tp.Close()
	bus := voiceevent.NewBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	unsub := wireTapeConsent(ctx, bus, tp, sessionCID, store, time.Hour, discardLog())
	defer unsub()

	if !laneCaptured(tp, "111", time.Now()) {
		t.Fatalf("reseed did not arm 111")
	}

	// A revoke lands for THIS session's 111 in the store, but the event is published
	// for ANOTHER campaign — it must be ignored, leaving 111 armed here.
	store.set(sessionCID) // durable revoke for this session
	bus.Publish(voiceevent.TapeConsentChanged{CampaignID: otherCID.String(), SpeakerID: "111", Granted: false})
	if !laneCaptured(tp, "111", time.Now().Add(time.Second)) {
		t.Fatalf("an event for a different campaign reconciled this session's tape")
	}
}

// errConsentStore wraps a fakeConsentStore to fail ListTapeConsent a bounded number
// of times, so a test can assert the poller logs the error and keeps ticking.
type errConsentStore struct {
	*fakeConsentStore
	mu    sync.Mutex
	fails int
	reads int
}

func (e *errConsentStore) ListTapeConsent(ctx context.Context, cid uuid.UUID) ([]string, error) {
	e.mu.Lock()
	e.reads++
	fail := e.fails > 0
	if fail {
		e.fails--
	}
	e.mu.Unlock()
	if fail {
		return nil, context.DeadlineExceeded
	}
	return e.fakeConsentStore.ListTapeConsent(ctx, cid)
}

func (e *errConsentStore) readCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.reads
}

// TestTapeWiring_PollerConvergesCrossPod covers #492 (a): a consent row flip with NO
// bus event — the cross-pod case, where the OWNER pod published TapeConsentChanged on
// its own bus and this worker's bus never saw it — converges the tape within one
// poll interval.
func TestTapeWiring_PollerConvergesCrossPod(t *testing.T) {
	cid := uuid.New()
	store := newFakeConsentStore() // empty at cycle start: nobody consents

	tp := tape.New(tape.Window, nil, nil)
	defer tp.Close()
	bus := voiceevent.NewBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	unsub := wireTapeConsent(ctx, bus, tp, cid, store, 10*time.Millisecond, discardLog())
	defer unsub()

	// A grant lands in the DB out of band (the button was dispatched on ANOTHER pod),
	// so NO TapeConsentChanged reaches this bus. The poller must pick it up.
	store.set(cid, "111")

	deadline := time.After(2 * time.Second)
	for {
		if laneCaptured(tp, "111", time.Now()) {
			return // converged
		}
		select {
		case <-deadline:
			t.Fatal("poller did not converge the tape to the durable grant within the window")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestTapeWiring_PollerStopsOnCtxCancel covers #492 (c): the poller goroutine dies
// with the cycle ctx (goleak catches a survivor).
func TestTapeWiring_PollerStopsOnCtxCancel(t *testing.T) {
	defer goleak.VerifyNone(t)

	cid := uuid.New()
	store := newFakeConsentStore()
	tp := tape.New(tape.Window, nil, nil)
	defer tp.Close()
	bus := voiceevent.NewBus()

	ctx, cancel := context.WithCancel(context.Background())
	unsub := wireTapeConsent(ctx, bus, tp, cid, store, 5*time.Millisecond, discardLog())

	// Let the poller run a few ticks, then cancel: the goroutine must exit.
	time.Sleep(20 * time.Millisecond)
	cancel()
	unsub()
	// Give the goroutine a moment to observe ctx.Done before goleak inspects.
	time.Sleep(20 * time.Millisecond)
}

// TestTapeWiring_PollerContinuesAfterError covers #492 (d): a reconcile error is
// logged and the ticker keeps going — a later tick still converges the tape once the
// store recovers.
func TestTapeWiring_PollerContinuesAfterError(t *testing.T) {
	cid := uuid.New()
	base := newFakeConsentStore()
	store := &errConsentStore{fakeConsentStore: base, fails: 2} // first two reads (incl the seed) error

	tp := tape.New(tape.Window, nil, nil)
	defer tp.Close()
	bus := voiceevent.NewBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	unsub := wireTapeConsent(ctx, bus, tp, cid, store, 10*time.Millisecond, discardLog())
	defer unsub()

	base.set(cid, "111") // durable grant, but the next read(s) still error

	deadline := time.After(2 * time.Second)
	for {
		if laneCaptured(tp, "111", time.Now()) {
			if store.readCount() < 3 {
				t.Fatalf("converged after %d reads, want the ticker to have survived the errors", store.readCount())
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("poller did not recover and converge after the store errors")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestTapeConsentReconcileInterval covers the env knob (#492): a valid duration is
// honored; blank/unparsable/non-positive falls back to the 5s default.
func TestTapeConsentReconcileInterval(t *testing.T) {
	cases := map[string]time.Duration{
		"":       defaultTapeConsentReconcileInterval,
		"bogus":  defaultTapeConsentReconcileInterval,
		"0s":     defaultTapeConsentReconcileInterval,
		"-3s":    defaultTapeConsentReconcileInterval,
		"250ms":  250 * time.Millisecond,
		"  10s ": 10 * time.Second,
	}
	for in, want := range cases {
		got := tapeConsentReconcileInterval(func(string) string { return in })
		if got != want {
			t.Errorf("tapeConsentReconcileInterval(%q) = %s, want %s", in, got, want)
		}
	}
}

// TestParseTapeConsentCustomID round-trips the button custom-id scheme and rejects
// foreign ids (which the presence handler must ignore).
func TestParseTapeConsentCustomID(t *testing.T) {
	cid := uuid.New()

	id, granted, ok := ParseTapeConsentCustomID(tapeGrantCustomID(cid))
	if !ok || !granted || id != cid {
		t.Fatalf("grant parse = (%v, %v, %v), want (%v, true, true)", id, granted, ok, cid)
	}
	id, granted, ok = ParseTapeConsentCustomID(tapeRevokeCustomID(cid))
	if !ok || granted || id != cid {
		t.Fatalf("revoke parse = (%v, %v, %v), want (%v, false, true)", id, granted, ok, cid)
	}

	for _, foreign := range []string{"", "gx:mute:agent:1", "gx:tape:grant:not-a-uuid", "gx:tape:bogus:" + cid.String(), "gx:tape:grant"} {
		if _, _, ok := ParseTapeConsentCustomID(foreign); ok {
			t.Errorf("ParseTapeConsentCustomID(%q) ok = true, want false", foreign)
		}
	}
}
