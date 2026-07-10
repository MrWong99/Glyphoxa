package wirenpc

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/tape"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

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
	unsub := wireTapeConsent(bus, nil)
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

// TestTapeWiring_ConsentSubAppliesToTape pins that TapeConsentChanged drives
// tape.SetConsent (#306): a grant arms a lane so a subsequent frame is captured,
// a revoke clears it.
func TestTapeWiring_ConsentSubAppliesToTape(t *testing.T) {
	tp := tape.New(tape.Window, nil, nil) // nobody consented yet
	defer tp.Close()
	bus := voiceevent.NewBus()
	unsub := wireTapeConsent(bus, tp)
	defer unsub()

	now := time.Now()
	// Before consent, a frame is dropped.
	tp.AppendInbound("111", []byte{0x01}, now)
	snap := tp.Snapshot(now.Add(-time.Second), now.Add(time.Second))
	if len(snap.Lanes) != 0 {
		t.Fatalf("captured before consent: %+v", snap.Lanes)
	}

	// Grant arrives on the bus -> lane armed.
	bus.Publish(voiceevent.TapeConsentChanged{SpeakerID: "111", Granted: true})
	tp.AppendInbound("111", []byte{0x02}, now.Add(20*time.Millisecond))
	snap = tp.Snapshot(now.Add(-time.Second), now.Add(time.Second))
	if len(snap.Lanes) != 1 {
		t.Fatalf("grant did not arm the lane: %+v", snap.Lanes)
	}

	// Revoke clears the lane.
	bus.Publish(voiceevent.TapeConsentChanged{SpeakerID: "111", Granted: false})
	snap = tp.Snapshot(now.Add(-time.Second), now.Add(time.Second))
	if len(snap.Lanes) != 0 {
		t.Fatalf("revoke did not clear the lane: %+v", snap.Lanes)
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
