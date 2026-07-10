package highlight

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/tape"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// ---- test doubles ----------------------------------------------------------

// fakeProvider is a scriptable [llm.Provider]: it returns respJSON(callIndex) as
// the assistant text, optionally reports usage, and can signal/block so a test can
// observe the worker mid-classify.
type fakeProvider struct {
	mu       sync.Mutex
	calls    int
	respJSON func(call int) string
	usage    *llm.Usage

	entered chan struct{} // non-blocking notify per Complete call (optional)
	gate    chan struct{} // if non-nil, Complete blocks until it receives (optional)
}

func (p *fakeProvider) Complete(ctx context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	call := p.calls
	p.calls++
	p.mu.Unlock()

	if p.entered != nil {
		select {
		case p.entered <- struct{}{}:
		default:
		}
	}
	if p.gate != nil {
		select {
		case <-p.gate:
		case <-ctx.Done():
			ch := make(chan llm.StreamEvent)
			close(ch)
			return ch, nil
		}
	}

	text := ""
	if p.respJSON != nil {
		text = p.respJSON(call)
	}
	usage := p.usage
	ch := make(chan llm.StreamEvent, 4)
	if text != "" {
		ch <- llm.StreamEvent{Type: llm.EventText, Text: text}
	}
	if usage != nil {
		ch <- llm.StreamEvent{Type: llm.EventUsage, Usage: *usage}
	}
	ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
	close(ch)
	return ch, nil
}

func (p *fakeProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// fakeSink records triggers.
type fakeSink struct {
	mu       sync.Mutex
	triggers []Trigger
}

func (s *fakeSink) HandleTrigger(t Trigger) {
	s.mu.Lock()
	s.triggers = append(s.triggers, t)
	s.mu.Unlock()
}

func (s *fakeSink) all() []Trigger {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Trigger(nil), s.triggers...)
}

// denyGate always refuses; allowGate always allows.
type denyGate struct{}

func (denyGate) AllowTurn() bool { return false }

type allowGate struct{}

func (allowGate) AllowTurn() bool { return true }

// testClock is a mutable injectable clock.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// scoreJSON is the canned classifier verdict for a given score.
func scoreJSON(score float64) string {
	return fmt.Sprintf(`{"score": %.1f, "excerpt": "the moment", "reason": "epic"}`, score)
}

// ---- helpers ---------------------------------------------------------------

// buildDetector constructs a detector with an injected clock and a classified
// notify channel, then starts it. Returns the detector, the clock, and the notify
// channel a test drains to await the async worker.
func buildDetector(t *testing.T, bus *voiceevent.Bus, prov llm.Provider, snap SnapshotFunc, sink Sink, gate orchestrator.TurnGate, cfg Config) (*Detector, *testClock, <-chan classification) {
	t.Helper()
	clk := &testClock{t: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)}
	d := newDetector(prov, "", snap, sink, gate, nil, nil, cfg)
	d.now = clk.now
	classified := make(chan classification, 256)
	d.classified = classified
	d.handled = make(chan struct{}, 1)
	d.start(bus)
	t.Cleanup(d.Close)
	return d, clk, classified
}

// awaitClassifications drains n classification notifications or fails on timeout.
func awaitClassifications(t *testing.T, ch <-chan classification, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for classification %d/%d", i+1, n)
		}
	}
}

// rawFinal publishes one STTFinal without waiting (used to force coalescing).
func rawFinal(bus *voiceevent.Bus, clk *testClock, speaker string, i int) {
	bus.Publish(voiceevent.STTFinal{
		At:        clk.now(),
		Text:      fmt.Sprintf("line %d from %s", i, speaker),
		SpeakerID: speaker,
	})
}

// publishFinals publishes n STTFinal events SERIALIZED: each waits for the worker
// to fold it in (d.handled) before the next, defeating the latest-wins coalescing
// so a cadence assertion is deterministic. Do not use it for a final that stalls
// the worker (a gated classify) — it would block on the missing handled signal.
func publishFinals(t *testing.T, d *Detector, bus *voiceevent.Bus, clk *testClock, speaker string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		rawFinal(bus, clk, speaker, i)
		select {
		case <-d.handled:
		case <-time.After(3 * time.Second):
			t.Fatalf("timed out waiting for final %d/%d to be handled", i+1, n)
		}
	}
}

// ---- tests -----------------------------------------------------------------

// TestPublishNeverBlocksAndCoalesces (TEST 1): a worker stalled mid-classify must
// not block Publish, and finals published during the stall coalesce latest-wins so
// a burst does not fan out into one classify per ClassifyEvery finals.
func TestPublishNeverBlocksAndCoalesces(t *testing.T) {
	bus := voiceevent.NewBus()
	gate := make(chan struct{})
	prov := &fakeProvider{
		respJSON: func(int) string { return scoreJSON(1.0) },
		entered:  make(chan struct{}, 1),
		gate:     gate,
	}
	sink := &fakeSink{}
	d, clk, _ := buildDetector(t, bus, prov, nil, sink, allowGate{}, Config{ClassifyEvery: 6})

	// Fold in 5 finals (serialized), then fire the 6th WITHOUT waiting: it drives the
	// first classify, which stalls the worker on the gated provider.
	publishFinals(t, d, bus, clk, "A", 5)
	rawFinal(bus, clk, "A", 5)
	select {
	case <-prov.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("worker never entered the classifier")
	}

	// While the worker is stalled, flood finals. Publish must return promptly.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			rawFinal(bus, clk, "A", i)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Publish blocked while the worker was stalled")
	}

	// Release the classifier and let the worker drain.
	close(gate)
	time.Sleep(100 * time.Millisecond)

	// Without coalescing, 6+500 finals would be ~84 classifies. Latest-wins collapses
	// the flood to a single pending final, so the worker re-arms slowly: far fewer.
	if got := prov.callCount(); got > 3 {
		t.Errorf("classify calls = %d, want <=3 (finals should coalesce under load)", got)
	}
}

// TestClassifyCadence (TEST 2): the worker classifies once per ClassifyEvery
// processed finals, no sooner.
func TestClassifyCadence(t *testing.T) {
	bus := voiceevent.NewBus()
	prov := &fakeProvider{respJSON: func(int) string { return scoreJSON(1.0) }}
	sink := &fakeSink{}
	d, clk, classified := buildDetector(t, bus, prov, nil, sink, allowGate{}, Config{ClassifyEvery: 6})

	// Five finals: below the cadence, no classify yet.
	for i := 0; i < 5; i++ {
		publishFinals(t, d, bus, clk, "A", 1)
	}
	select {
	case <-classified:
		t.Fatal("classified before ClassifyEvery finals")
	case <-time.After(150 * time.Millisecond):
	}

	// The sixth final triggers exactly one classify.
	publishFinals(t, d, bus, clk, "A", 1)
	awaitClassifications(t, classified, 1)
	if got := prov.callCount(); got != 1 {
		t.Errorf("classify calls = %d, want 1 after 6 finals", got)
	}
}

// TestConfirmWindowsProducesOneTrigger (TEST 3): a window scoring >=Bar for
// ConfirmWindows consecutive classifications emits exactly one trigger.
func TestConfirmWindowsProducesOneTrigger(t *testing.T) {
	bus := voiceevent.NewBus()
	prov := &fakeProvider{respJSON: func(int) string { return scoreJSON(9.0) }}
	sink := &fakeSink{}
	snap := func(from, to time.Time) tape.Snapshot { return tape.Snapshot{From: from, To: to} }
	d, clk, classified := buildDetector(t, bus, prov, snap, sink, allowGate{}, Config{
		ClassifyEvery: 6, Bar: 8.0, ConfirmWindows: 2, Cooldown: time.Hour,
	})

	// One classify (>=Bar) — not yet confirmed.
	publishFinals(t, d, bus, clk, "A", 6)
	awaitClassifications(t, classified, 1)
	if n := len(sink.all()); n != 0 {
		t.Fatalf("trigger after 1 confirming window, want 0 (need 2), got %d", n)
	}

	// Second consecutive classify (>=Bar) confirms → exactly one trigger.
	publishFinals(t, d, bus, clk, "A", 6)
	awaitClassifications(t, classified, 1)
	time.Sleep(50 * time.Millisecond)
	if n := len(sink.all()); n != 1 {
		t.Fatalf("trigger count = %d, want exactly 1", n)
	}
}

// TestCooldownThenRearm (TEST 4): after a trigger the detector suppresses new
// triggers for Cooldown, then rearms.
func TestCooldownThenRearm(t *testing.T) {
	bus := voiceevent.NewBus()
	prov := &fakeProvider{respJSON: func(int) string { return scoreJSON(9.0) }}
	sink := &fakeSink{}
	snap := func(from, to time.Time) tape.Snapshot { return tape.Snapshot{From: from, To: to} }
	d, clk, classified := buildDetector(t, bus, prov, snap, sink, allowGate{}, Config{
		ClassifyEvery: 2, Bar: 8.0, ConfirmWindows: 2, Cooldown: 120 * time.Second,
	})

	// Two classifies confirm the first trigger.
	publishFinals(t, d, bus, clk, "A", 4)
	awaitClassifications(t, classified, 2)
	time.Sleep(50 * time.Millisecond)
	if n := len(sink.all()); n != 1 {
		t.Fatalf("first trigger count = %d, want 1", n)
	}

	// Within the cooldown, further high windows are suppressed (classify is skipped,
	// so no notification arrives).
	publishFinals(t, d, bus, clk, "A", 4)
	select {
	case <-classified:
		t.Fatal("classified within cooldown, want suppression")
	case <-time.After(150 * time.Millisecond):
	}
	if n := len(sink.all()); n != 1 {
		t.Fatalf("trigger count during cooldown = %d, want still 1", n)
	}

	// Advance past the cooldown and rearm: two more confirming windows → a 2nd trigger.
	clk.advance(121 * time.Second)
	publishFinals(t, d, bus, clk, "A", 4)
	awaitClassifications(t, classified, 2)
	time.Sleep(50 * time.Millisecond)
	if n := len(sink.all()); n != 2 {
		t.Fatalf("trigger count after rearm = %d, want 2", n)
	}
}

// TestMaxCandidatesCap (TEST 5): once MaxCandidates triggers have fired, the
// detector stops classifying entirely.
func TestMaxCandidatesCap(t *testing.T) {
	bus := voiceevent.NewBus()
	prov := &fakeProvider{respJSON: func(int) string { return scoreJSON(9.0) }}
	sink := &fakeSink{}
	snap := func(from, to time.Time) tape.Snapshot { return tape.Snapshot{From: from, To: to} }
	d, clk, classified := buildDetector(t, bus, prov, snap, sink, allowGate{}, Config{
		ClassifyEvery: 2, Bar: 8.0, ConfirmWindows: 1, Cooldown: time.Nanosecond, MaxCandidates: 1,
	})

	// First confirming window → first (and only allowed) trigger.
	publishFinals(t, d, bus, clk, "A", 2)
	awaitClassifications(t, classified, 1)
	time.Sleep(50 * time.Millisecond)
	if n := len(sink.all()); n != 1 {
		t.Fatalf("trigger count = %d, want 1", n)
	}
	callsAtCap := prov.callCount()

	// Cap reached: further finals must NOT classify (no spend past the cap).
	publishFinals(t, d, bus, clk, "A", 20)
	select {
	case <-classified:
		t.Fatal("classified after MaxCandidates reached")
	case <-time.After(150 * time.Millisecond):
	}
	if got := prov.callCount(); got != callsAtCap {
		t.Errorf("classify calls after cap = %d, want unchanged %d", got, callsAtCap)
	}
}

// TestTriggerShape (TEST 6): a trigger carries the window±Lead/Tail range, the
// populated caption fields, and the verbatim snapshot.
func TestTriggerShape(t *testing.T) {
	bus := voiceevent.NewBus()
	prov := &fakeProvider{respJSON: func(int) string {
		return `{"score": 9.5, "excerpt": "natural twenty!", "reason": "critical hit on the dragon"}`
	}}
	sink := &fakeSink{}
	var gotFrom, gotTo time.Time
	snap := func(from, to time.Time) tape.Snapshot {
		gotFrom, gotTo = from, to
		return tape.Snapshot{From: from, To: to, Lanes: []tape.LaneSnapshot{{LaneID: "A"}}}
	}
	d, clk, classified := buildDetector(t, bus, prov, snap, sink, allowGate{}, Config{
		ClassifyEvery: 2, Bar: 8.0, ConfirmWindows: 1, Cooldown: time.Hour,
		Lead: 15 * time.Second, Tail: 5 * time.Second,
	})

	at := clk.now()
	publishFinals(t, d, bus, clk, "A", 2)
	awaitClassifications(t, classified, 1)
	time.Sleep(50 * time.Millisecond)

	trigs := sink.all()
	if len(trigs) != 1 {
		t.Fatalf("trigger count = %d, want 1", len(trigs))
	}
	tr := trigs[0]
	if !tr.From.Equal(at.Add(-15 * time.Second)) {
		t.Errorf("From = %v, want %v", tr.From, at.Add(-15*time.Second))
	}
	if !tr.To.Equal(at.Add(5 * time.Second)) {
		t.Errorf("To = %v, want %v", tr.To, at.Add(5*time.Second))
	}
	if !gotFrom.Equal(tr.From) || !gotTo.Equal(tr.To) {
		t.Errorf("snapshot cut over (%v,%v), want trigger range (%v,%v)", gotFrom, gotTo, tr.From, tr.To)
	}
	if tr.Score != 9.5 {
		t.Errorf("Score = %v, want 9.5", tr.Score)
	}
	if tr.Excerpt != "natural twenty!" {
		t.Errorf("Excerpt = %q", tr.Excerpt)
	}
	if tr.Reason != "critical hit on the dragon" {
		t.Errorf("Reason = %q", tr.Reason)
	}
	if len(tr.SpeakerIDs) != 1 || tr.SpeakerIDs[0] != "A" {
		t.Errorf("SpeakerIDs = %v, want [A]", tr.SpeakerIDs)
	}
	if len(tr.Snapshot.Lanes) != 1 || tr.Snapshot.Lanes[0].LaneID != "A" {
		t.Errorf("Snapshot not passed through verbatim: %+v", tr.Snapshot)
	}
}

// TestGateDeniedDisarms (TEST 7): a closed spend gate stops the classifier being
// called at all and disarms the detector — a Highlight never ends a session.
func TestGateDeniedDisarms(t *testing.T) {
	bus := voiceevent.NewBus()
	prov := &fakeProvider{respJSON: func(int) string { return scoreJSON(9.0) }}
	sink := &fakeSink{}
	d, clk, _ := buildDetector(t, bus, prov, nil, sink, denyGate{}, Config{ClassifyEvery: 2})

	publishFinals(t, d, bus, clk, "A", 10)
	time.Sleep(150 * time.Millisecond)
	if got := prov.callCount(); got != 0 {
		t.Errorf("classify calls with closed gate = %d, want 0", got)
	}
	if n := len(sink.all()); n != 0 {
		t.Errorf("triggers with closed gate = %d, want 0", n)
	}
}

// TestUsageMetered (TEST 7 cont.): a classify records LLM token usage on the stage
// recorder (ADR-0045/0046).
func TestUsageMetered(t *testing.T) {
	bus := voiceevent.NewBus()
	prov := &fakeProvider{
		respJSON: func(int) string { return scoreJSON(1.0) },
		usage:    &llm.Usage{InputTokens: 123, OutputTokens: 45},
	}
	sink := &fakeSink{}
	rec := &spyRecorder{}
	clk := &testClock{t: time.Now()}
	d := newDetector(prov, "test-model", nil, sink, allowGate{}, rec, nil, Config{ClassifyEvery: 2})
	d.now = clk.now
	classified := make(chan classification, 8)
	d.classified = classified
	d.handled = make(chan struct{}, 1)
	d.start(bus)
	t.Cleanup(d.Close)

	publishFinals(t, d, bus, clk, "A", 2)
	awaitClassifications(t, classified, 1)

	in, out, ok := rec.llmTokens()
	if !ok {
		t.Fatal("no LLM usage recorded")
	}
	if in != 123 || out != 45 {
		t.Errorf("usage = (%d,%d), want (123,45)", in, out)
	}
}
