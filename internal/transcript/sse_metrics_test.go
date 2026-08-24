package transcript

// SSE observability tests (#612): the lagged-drop counter and the live
// subscriber gauge. A lag event is a user-visible glitch (the GM's live
// transcript stalls and reloads), and today it is invisible; these pin that
// every attach/detach republishes the subscriber count and that one
// subscriber's overflow counts exactly once.

import (
	"fmt"
	"sync"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// fakeSSEMetrics records what the relay publishes. Its methods run under r.mu
// (the SSEMetrics contract), so it takes only its own lock and never blocks.
type fakeSSEMetrics struct {
	mu     sync.Mutex
	sets   []int
	lagged int
}

func (f *fakeSSEMetrics) TranscriptSSELagged() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lagged++
}

func (f *fakeSSEMetrics) SetTranscriptSSESubscribers(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sets = append(f.sets, n)
}

// lastSet is the most recently published subscriber count.
func (f *fakeSSEMetrics) lastSet(t *testing.T) int {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sets) == 0 {
		t.Fatal("no subscriber count was published")
	}
	return f.sets[len(f.sets)-1]
}

func (f *fakeSSEMetrics) laggedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lagged
}

// TestSSESubscriberGauge: every attach and detach republishes the live
// subscriber count from len(r.subs) — Set-from-COUNT, never Inc/Dec (ADR-0032),
// so the gauge is right no matter which handler goroutine moved last.
func TestSSESubscriberGauge(t *testing.T) {
	_, r, _, id := liveRelay(t)
	m := &fakeSSEMetrics{}
	r.SetMetrics(m)

	s1, _ := r.attach(id, 0)
	if got := m.lastSet(t); got != 1 {
		t.Fatalf("after first attach gauge = %d, want 1", got)
	}
	s2, _ := r.attach(id, 0)
	if got := m.lastSet(t); got != 2 {
		t.Fatalf("after second attach gauge = %d, want 2", got)
	}

	r.detach(s1)
	if got := m.lastSet(t); got != 1 {
		t.Fatalf("after first detach gauge = %d, want 1", got)
	}
	r.detach(s2)
	if got := m.lastSet(t); got != 0 {
		t.Fatalf("after last detach gauge = %d, want 0", got)
	}
}

// flood publishes n line frames onto the session bus, filling any attached
// subscriber's channel.
func flood(bus *voiceevent.Bus, n int) {
	for i := 0; i < n; i++ {
		bus.Publish(voiceevent.STTFinal{At: at(i), Text: "flood", TurnID: fmt.Sprintf("f%d", i)})
	}
}

// TestSSELaggedCountedOnce: a stalled reader whose channel overflows is counted
// exactly once, no matter how many further frames are pushed at it. The
// already-lagged branch in push delivers nothing and so must count nothing —
// otherwise a busy session would inflate one glitch into thousands.
func TestSSELaggedCountedOnce(t *testing.T) {
	bus, r, _, id := liveRelay(t)
	m := &fakeSSEMetrics{}
	r.SetMetrics(m)

	// Warm publish sets the active session before the subscriber attaches.
	bus.Publish(voiceevent.STTFinal{At: at(0), Text: "warm", TurnID: "w"})
	s, _ := r.attach(id, 0)
	defer r.detach(s)

	// subBuffer frames fill s.ch; the next one overflows and drops the reader.
	flood(bus, subBuffer+1)
	select {
	case <-s.lagged:
	default:
		t.Fatal("expected the overflowing subscriber to be signalled lagged")
	}
	if got := m.laggedCount(); got != 1 {
		t.Fatalf("lagged count after overflow = %d, want 1", got)
	}

	// Every later frame takes the already-lagged path: still exactly one event.
	flood(bus, 50)
	if got := m.laggedCount(); got != 1 {
		t.Fatalf("lagged count after further pushes = %d, want 1", got)
	}
}

// TestSSELaggedIsPerSubscriber: the counter tracks subscribers, not pushes. Two
// browsers tail the same session; only the stalled one overflows, and the
// healthy one draining alongside it neither adds an event nor suppresses one.
func TestSSELaggedIsPerSubscriber(t *testing.T) {
	bus, r, _, id := liveRelay(t)
	m := &fakeSSEMetrics{}
	r.SetMetrics(m)

	bus.Publish(voiceevent.STTFinal{At: at(0), Text: "warm", TurnID: "w"})
	stalled, _ := r.attach(id, 0)
	defer r.detach(stalled)
	healthy, _ := r.attach(id, 0)
	defer r.detach(healthy)

	// Publish one frame at a time, draining only the healthy subscriber, so it
	// never fills while the stalled one runs past subBuffer.
	for i := 0; i <= subBuffer+10; i++ {
		bus.Publish(voiceevent.STTFinal{At: at(i), Text: "flood", TurnID: fmt.Sprintf("f%d", i)})
		for drained := true; drained; {
			select {
			case <-healthy.ch:
			default:
				drained = false
			}
		}
	}

	select {
	case <-healthy.lagged:
		t.Fatal("the draining subscriber must not be dropped")
	default:
	}
	if got := m.laggedCount(); got != 1 {
		t.Fatalf("lagged count = %d, want 1 (only the stalled subscriber)", got)
	}
}

// TestSSEMetricsUnwired: a relay that never had SetMetrics called still
// attaches, pushes, overflows and detaches — the discard default means no call
// site needs a nil check.
func TestSSEMetricsUnwired(t *testing.T) {
	bus, r, _, id := liveRelay(t)

	bus.Publish(voiceevent.STTFinal{At: at(0), Text: "warm", TurnID: "w"})
	s, _ := r.attach(id, 0)
	flood(bus, subBuffer+2)
	r.detach(s)

	// Explicitly clearing the sink is equally safe.
	r.SetMetrics(nil)
	s2, _ := r.attach(id, 0)
	flood(bus, subBuffer+2)
	r.detach(s2)
}
