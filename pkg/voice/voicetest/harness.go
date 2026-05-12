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
