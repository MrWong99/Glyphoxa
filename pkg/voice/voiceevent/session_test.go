package voiceevent

import (
	"bufio"
	"os"
	"reflect"
	"regexp"
	"testing"
)

// taxonomy is one zero value of every Event type in the package. The
// completeness guard below cross-checks it against the source so a new event
// type cannot be added without teaching WithSessionID/SessionIDOf about it.
func taxonomy() []Event {
	return []Event{
		VADSpeechStart{},
		VADSpeechEnd{},
		VADVoicingStopped{},
		VADVoicingResumed{},
		STTPartial{},
		STTFinal{},
		AddressRouted{},
		EnsembleRouted{},
		EnsembleLead{},
		EnsembleReaction{},
		SpeakRequested{},
		TTSInvoked{},
		TTSStreamFailed{},
		FirstAudio{},
		FirstOpus{},
		TurnEnded{},
		BargeDetected{},
		MuteChanged{},
		TapeConsentChanged{},
		ReplayRequested{},
		SpendCapReached{},
		ConnectionStateChanged{},
	}
}

// TestWithSessionID_RoundTripsEveryEventType pins that WithSessionID stamps the
// session identity onto a copy of EVERY event type and SessionIDOf reads it back
// — the S1 shared contract other issues build on. The copy must not mutate the
// original (additive, value-semantics), and an empty id leaves the event unstamped.
func TestWithSessionID_RoundTripsEveryEventType(t *testing.T) {
	t.Parallel()

	const sid = "550e8400-e29b-41d4-a716-446655440000"
	for _, e := range taxonomy() {
		name := reflect.TypeOf(e).Name()
		stamped := WithSessionID(e, sid)
		if got := SessionIDOf(stamped); got != sid {
			t.Errorf("%s: SessionIDOf(WithSessionID(e, %q)) = %q, want %q", name, sid, got, sid)
		}
		// The original zero value must still read "" — WithSessionID returns a copy.
		if got := SessionIDOf(e); got != "" {
			t.Errorf("%s: WithSessionID mutated the original (SessionIDOf = %q, want \"\")", name, got)
		}
		// The wire name must be preserved across the stamped copy (additive).
		if stamped.EventName() != e.EventName() {
			t.Errorf("%s: WithSessionID changed EventName %q -> %q", name, e.EventName(), stamped.EventName())
		}
	}
}

// TestSessionIDOf_UnstampedIsEmpty pins that a never-stamped event (the
// session-local / pre-stamp default) reads "".
func TestSessionIDOf_UnstampedIsEmpty(t *testing.T) {
	t.Parallel()

	for _, e := range taxonomy() {
		if got := SessionIDOf(e); got != "" {
			t.Errorf("%s: unstamped SessionIDOf = %q, want \"\"", reflect.TypeOf(e).Name(), got)
		}
	}
}

// TestWithSessionID_TaxonomyComplete is the completeness guard: it scans event.go
// for every type carrying an EventName method (the wire taxonomy, ADR-0020) and
// fails if any is missing from taxonomy() above — which would mean WithSessionID's
// exhaustive type switch is also missing it, silently dropping the session
// identity on that event.
func TestWithSessionID_TaxonomyComplete(t *testing.T) {
	t.Parallel()

	source := sourceEventTypes(t)
	have := map[string]bool{}
	for _, e := range taxonomy() {
		have[reflect.TypeOf(e).Name()] = true
	}
	for name := range source {
		if !have[name] {
			t.Errorf("event type %s carries EventName() but is missing from taxonomy() / WithSessionID — session identity would be dropped on it", name)
		}
	}
	// And no phantom types in the list that the source doesn't declare.
	for name := range have {
		if !source[name] {
			t.Errorf("taxonomy() lists %s but no EventName() method declares it in event.go", name)
		}
	}
}

// sourceEventTypes parses event.go for the receiver type of every EventName
// method — the authoritative taxonomy the wire is built on.
func sourceEventTypes(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open("event.go")
	if err != nil {
		t.Fatalf("open event.go: %v", err)
	}
	defer f.Close()

	re := regexp.MustCompile(`^func \(([A-Za-z0-9_]+)\) EventName\(\) string`)
	out := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := re.FindStringSubmatch(sc.Text()); m != nil {
			out[m[1]] = true
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan event.go: %v", err)
	}
	return out
}

// TestForward_StampsAndRepublishes pins the bridge: every event published on the
// session bus is republished on the process bus stamped with the origin session
// id, while the session-local subscriber on src sees the original unstamped event
// (#487 — session-local reactors never see the stamp).
func TestForward_StampsAndRepublishes(t *testing.T) {
	t.Parallel()

	const sid = "11111111-1111-1111-1111-111111111111"
	src, dst := NewBus(), NewBus()

	var local, process []Event
	t.Cleanup(src.Subscribe(func(e Event) { local = append(local, e) }))
	t.Cleanup(dst.Subscribe(func(e Event) { process = append(process, e) }))

	stop := Forward(src, dst, sid)
	t.Cleanup(stop)

	src.Publish(STTFinal{Text: "hi"})
	src.Publish(MuteChanged{AgentID: "a"})

	if len(process) != 2 {
		t.Fatalf("process bus got %d events, want 2", len(process))
	}
	for _, e := range process {
		if SessionIDOf(e) != sid {
			t.Errorf("process event %s not stamped: SessionIDOf = %q, want %q", e.EventName(), SessionIDOf(e), sid)
		}
	}
	// The session-local subscriber saw the ORIGINAL, unstamped events.
	if len(local) != 2 {
		t.Fatalf("session-local subscriber got %d events, want 2", len(local))
	}
	for _, e := range local {
		if SessionIDOf(e) != "" {
			t.Errorf("session-local event %s was stamped: SessionIDOf = %q, want \"\"", e.EventName(), SessionIDOf(e))
		}
	}
}

// TestForward_UnsubscribeStops pins that the returned unsubscribe detaches the
// bridge: events after it are no longer republished onto the process bus.
func TestForward_UnsubscribeStops(t *testing.T) {
	t.Parallel()

	src, dst := NewBus(), NewBus()
	var process []Event
	t.Cleanup(dst.Subscribe(func(e Event) { process = append(process, e) }))

	stop := Forward(src, dst, "s")
	src.Publish(STTFinal{})
	stop()
	src.Publish(STTFinal{})

	if len(process) != 1 {
		t.Errorf("after unsubscribe process bus got %d events, want 1", len(process))
	}
}

// TestForward_NilDstNoOp pins the bench / voice-standalone posture: a nil process
// bus bridges nothing and its unsubscribe is a safe no-op.
func TestForward_NilDstNoOp(t *testing.T) {
	t.Parallel()

	src := NewBus()
	stop := Forward(src, nil, "s")
	if stop == nil {
		t.Fatal("Forward(nil dst) returned a nil unsubscribe")
	}
	src.Publish(STTFinal{}) // must not panic
	stop()
	stop() // idempotent / safe twice
}
