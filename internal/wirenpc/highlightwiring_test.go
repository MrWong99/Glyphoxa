package wirenpc

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/highlight"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/tape"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// fakeHighlightSink records triggers for the wiring assertions.
type fakeHighlightSink struct {
	mu sync.Mutex
	n  int
}

func (s *fakeHighlightSink) HandleTrigger(highlight.Trigger) {
	s.mu.Lock()
	s.n++
	s.mu.Unlock()
}

func (s *fakeHighlightSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

// stubLLM replays a fixed score-1.0 classifier verdict (no network): below the
// engine default bar (8.0, so no trigger under default Config), but confirmable
// under a lowered campaign bar — the knobs test depends on exactly that split.
type stubLLM struct{}

func (stubLLM) Complete(_ context.Context, _ llm.Request) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 2)
	ch <- llm.StreamEvent{Type: llm.EventText, Text: `{"score": 1.0, "excerpt": "x", "reason": "y"}`}
	ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
	close(ch)
	return ch, nil
}

// labelSpy captures the provider label the detector meters usage under, and
// counts metered classifies (one LLMTokens call per classifier pass) so a test
// can await "at least N classifies have completed".
type labelSpy struct {
	observe.Discard
	mu       sync.Mutex
	provider observe.Provider
	seen     bool
	calls    int
}

func (s *labelSpy) LLMTokens(p observe.Provider, _ string, _, _ int) {
	s.mu.Lock()
	s.provider, s.seen = p, true
	s.calls++
	s.mu.Unlock()
}

func (s *labelSpy) get() (observe.Provider, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.provider, s.seen
}

func (s *labelSpy) classifies() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestBuildHighlightDetectorMetersProviderLabel asserts the ProviderLabel deviation
// is wired: buildHighlightDetector passes llmProviderLabel(cfg.llmProviderID) into
// the detector, so classifier spend is attributed to the ACTUAL provider — not the
// empty/groq default. A dropped label would meter this anthropic session as groq.
func TestBuildHighlightDetectorMetersProviderLabel(t *testing.T) {
	orig := newLLM
	newLLM = func(_, _ string) (llm.Provider, error) { return stubLLM{}, nil }
	defer func() { newLLM = orig }()

	bus := voiceevent.NewBus()
	tp := tape.New(tape.Window, nil, nil)
	defer tp.Close()
	spy := &labelSpy{}
	cfg := Config{Tape: tp, Highlights: &fakeHighlightSink{}, StageMetrics: spy, llmProviderID: "anthropic"}
	d := buildHighlightDetector(cfg, bus, slog.New(slog.DiscardHandler))
	if d == nil {
		t.Fatal("detector = nil, want non-nil")
	}
	defer d.Close()

	// Drive enough finals to trigger a classify (spaced so the worker folds each,
	// defeating coalescing), then wait for the metered usage.
	deadline := time.Now().Add(3 * time.Second)
	for i := 0; ; i++ {
		if _, seen := spy.get(); seen || !time.Now().Before(deadline) {
			break
		}
		bus.Publish(voiceevent.STTFinal{Text: fmt.Sprintf("line %d of the scene", i), At: time.Now()})
		time.Sleep(8 * time.Millisecond)
	}

	got, seen := spy.get()
	if !seen {
		t.Fatal("no classifier usage metered")
	}
	if got != observe.Provider("anthropic") {
		t.Errorf("metered provider = %q, want %q (label not wired from cfg.llmProviderID)", got, observe.ProviderAnthropic)
	}
}

// TestBuildHighlightDetectorCampaignKnobs (#632 follow-up): the campaign's
// highlightBar / highlightConfirmWindows reach the detector's confirmation gate.
// The stub classifier scores every window 1.0 — under the engine defaults
// (Bar 8.0, ConfirmWindows 2) that can NEVER confirm a trigger, so a trigger
// arriving at the sink proves the campaign knobs (bar 1.0, one confirm window)
// were wired through and not silently replaced by withDefaults.
func TestBuildHighlightDetectorCampaignKnobs(t *testing.T) {
	orig := newLLM
	newLLM = func(_, _ string) (llm.Provider, error) { return stubLLM{}, nil }
	defer func() { newLLM = orig }()

	bus := voiceevent.NewBus()
	tp := tape.New(tape.Window, nil, nil)
	defer tp.Close()
	spy := &labelSpy{}
	sink := &fakeHighlightSink{}
	cfg := Config{
		Tape: tp, Highlights: sink, StageMetrics: spy,
		highlightBar: 1.0, highlightConfirmWindows: 1,
	}
	d := buildHighlightDetector(cfg, bus, slog.New(slog.DiscardHandler))
	if d == nil {
		t.Fatal("detector = nil, want non-nil")
	}
	// Close is idempotent, so the defer backstops the Fatal paths (no leaked
	// worker) while the explicit Close below stays the flush point the final
	// assertion depends on.
	defer d.Close()

	// Feed spaced finals (defeating latest-wins coalescing) until at least one
	// classify has been metered — with bar 1.0 and one confirm window, that first
	// score-1.0 pass already schedules the cut.
	deadline := time.Now().Add(3 * time.Second)
	for i := 0; spy.classifies() < 1 && time.Now().Before(deadline); i++ {
		bus.Publish(voiceevent.STTFinal{Text: fmt.Sprintf("line %d of the scene", i), At: time.Now()})
		time.Sleep(8 * time.Millisecond)
	}
	if spy.classifies() < 1 {
		t.Fatal("no classify completed within the deadline")
	}

	// Close flushes the Tail-delayed cut immediately and waits it out, so the
	// sink count is settled when it returns.
	d.Close()
	if sink.count() < 1 {
		t.Fatal("no trigger reached the sink: campaign bar/confirm knobs not wired into highlight.Config")
	}
}

// TestBuildHighlightDetectorGating (TEST 11): the detector is armed ONLY when both
// the tape (clip source) and a highlight sink are wired; any missing half yields no
// detector, so the loop is byte-identical.
func TestBuildHighlightDetectorGating(t *testing.T) {
	bus := voiceevent.NewBus()
	log := slog.New(slog.DiscardHandler)
	tp := tape.New(tape.Window, nil, nil)
	defer tp.Close()

	cases := []struct {
		name       string
		tape       *tape.Tape
		highlights highlight.Sink
		wantNil    bool
	}{
		{"neither", nil, nil, true},
		{"tape only", tp, nil, true},
		{"sink only", nil, &fakeHighlightSink{}, true},
		{"both", tp, &fakeHighlightSink{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Tape: tc.tape, Highlights: tc.highlights}
			d := buildHighlightDetector(cfg, bus, log)
			if tc.wantNil {
				if d != nil {
					t.Fatalf("detector = non-nil, want nil for %q", tc.name)
				}
				if opts := highlightPCMOptions(d); opts != nil {
					t.Errorf("PCM options = %v, want nil for a nil detector", opts)
				}
				return
			}
			if d == nil {
				t.Fatal("detector = nil, want non-nil when tape and sink are both set")
			}
			// The PCM tap is wired as exactly one pipeline option.
			if opts := highlightPCMOptions(d); len(opts) != 1 {
				t.Errorf("PCM options = %d, want 1", len(opts))
			}
			// It subscribed to the bus (a published final must not panic) and Close
			// releases the subscription + worker at cycle end (a leak is a #44 bug):
			// Close must return promptly.
			bus.Publish(voiceevent.STTFinal{Text: "hello", At: time.Now()})
			done := make(chan struct{})
			go func() { d.Close(); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("detector.Close did not return (goroutine leak)")
			}
		})
	}
}
