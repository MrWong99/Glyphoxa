// Package voicetest is the imperative-Go test harness for the voice pipeline,
// per ADR-0020.
//
// A test creates a [Harness] which owns a [voiceevent.Bus] and observes every
// event published on it. Assertions run against the observed log via the
// package-level helpers ([AssertEventOccurred], [AssertEvent], …) which take
// the event type as a generic type parameter so tests can both restrict by
// concrete event type and apply a value-level predicate.
//
// Voicetest deliberately uses no DSL: assertions are plain Go calls so they
// participate in `go test` features (race, coverage, parallelism, IDE
// navigation). Per-clip `meta.yaml` files under tests/voice-clips/ are pure
// documentation; they carry no executable assertions.
package voicetest

import (
	"sync"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// Harness observes events on its Bus and exposes assertion primitives.
//
// Tests should publish into Harness.Bus (or wire it as the orchestrator's bus)
// before calling assertions. Subscriptions registered before New returns are
// torn down automatically when the test ends.
type Harness struct {
	t *testing.T

	// Bus is the event channel observed by this harness. Pass it to the
	// system under test as the orchestrator's publishing bus.
	Bus *voiceevent.Bus

	mu   sync.Mutex
	seen []voiceevent.Event
}

// New creates a Harness with a fresh Bus, subscribed for the lifetime of t.
func New(t *testing.T) *Harness {
	t.Helper()
	h := &Harness{
		t:   t,
		Bus: voiceevent.NewBus(),
	}
	unsub := h.Bus.Subscribe(func(e voiceevent.Event) {
		h.mu.Lock()
		h.seen = append(h.seen, e)
		h.mu.Unlock()
	})
	t.Cleanup(unsub)
	return h
}

// Events returns a snapshot of every event observed so far.
func (h *Harness) Events() []voiceevent.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]voiceevent.Event, len(h.seen))
	copy(out, h.seen)
	return out
}

// AssertEventOccurred fails the test if no observed event has concrete type T.
// Use [AssertEvent] when the assertion needs to inspect the event's field
// values, not just its type.
func AssertEventOccurred[T voiceevent.Event](t *testing.T, h *Harness) {
	t.Helper()
	AssertEvent(t, h, func(T) bool { return true }, "any "+eventTypeName[T]())
}

// AssertEvent fails the test if no observed event of concrete type T satisfies
// match. desc names what was expected and is included in the failure message.
func AssertEvent[T voiceevent.Event](t *testing.T, h *Harness, match func(T) bool, desc string) {
	t.Helper()

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.seen {
		typed, ok := e.(T)
		if !ok {
			continue
		}
		if match(typed) {
			return
		}
	}
	t.Fatalf("AssertEvent[%s]: no event matched %q; seen %d events: %v",
		eventTypeName[T](), desc, len(h.seen), eventNames(h.seen))
}

// OrderMatcher is one step in an [AssertOrder] sequence: a predicate plus a
// human-readable name used in failure messages. Construct one with [MatchType]
// (and, in later tracer bullets, value-level helpers layered on top).
type OrderMatcher struct {
	Name  string
	Match func(voiceevent.Event) bool
}

// MatchType returns an [OrderMatcher] that accepts any event of concrete type T.
// Its Name is the wire name of T (e.g. "vad.speech_end") so failure messages
// read in the same vocabulary the SSE relay (ADR-0014) uses on the wire.
func MatchType[T voiceevent.Event]() OrderMatcher {
	name := eventTypeName[T]()
	return OrderMatcher{
		Name: name,
		Match: func(e voiceevent.Event) bool {
			_, ok := e.(T)
			return ok
		},
	}
}

// AssertOrder fails the test unless the observed event log contains a
// subsequence of events that satisfies steps in order.
//
// Subsequence — not contiguous — matching is the right primitive for the voice
// pipeline: between speech_start and speech_end the VAD stage emits any number
// of frame-level events (and other stages will interleave their own), and the
// ordering claim is "X happened, then later Y happened", not "X was immediately
// followed by Y".
func AssertOrder(t *testing.T, h *Harness, steps ...OrderMatcher) {
	t.Helper()
	if len(steps) == 0 {
		t.Fatalf("AssertOrder: called with no matchers")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	idx := 0
	for _, e := range h.seen {
		if steps[idx].Match(e) {
			idx++
			if idx == len(steps) {
				return
			}
		}
	}

	want := make([]string, len(steps))
	for i, s := range steps {
		want[i] = s.Name
	}
	t.Fatalf("AssertOrder: matched %d/%d steps in order; want sequence %v; seen %d events: %v",
		idx, len(steps), want, len(h.seen), eventNames(h.seen))
}

// AssertNoEvent fails the test if any observed event has concrete type T.
// It is the negation of [AssertEventOccurred] and pins the "no speech"
// half of stages whose behaviour is otherwise validated only by positive
// fixtures.
func AssertNoEvent[T voiceevent.Event](t *testing.T, h *Harness) {
	t.Helper()

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.seen {
		if _, ok := e.(T); ok {
			t.Fatalf("AssertNoEvent[%s]: observed forbidden event %#v; seen %d events: %v",
				eventTypeName[T](), e, len(h.seen), eventNames(h.seen))
		}
	}
}

// eventTypeName returns the wire name of the zero value of T, used for
// diagnostics. Every voiceevent.Event implementation must satisfy EventName
// on a zero value, which is true for the current value-typed events.
func eventTypeName[T voiceevent.Event]() string {
	var zero T
	return zero.EventName()
}

// eventNames returns the wire names of every event in evs, for diagnostics.
func eventNames(evs []voiceevent.Event) []string {
	names := make([]string, len(evs))
	for i, e := range evs {
		names[i] = e.EventName()
	}
	return names
}
