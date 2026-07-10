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
type fakeHighlightSink struct{ n int }

func (s *fakeHighlightSink) HandleTrigger(highlight.Trigger) { s.n++ }

// stubLLM replays a fixed low-score classifier verdict (no trigger, no network).
type stubLLM struct{}

func (stubLLM) Complete(_ context.Context, _ llm.Request) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 2)
	ch <- llm.StreamEvent{Type: llm.EventText, Text: `{"score": 1.0, "excerpt": "x", "reason": "y"}`}
	ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
	close(ch)
	return ch, nil
}

// labelSpy captures the provider label the detector meters usage under.
type labelSpy struct {
	observe.Discard
	mu       sync.Mutex
	provider observe.Provider
	seen     bool
}

func (s *labelSpy) LLMTokens(p observe.Provider, _ string, _, _ int) {
	s.mu.Lock()
	s.provider, s.seen = p, true
	s.mu.Unlock()
}

func (s *labelSpy) get() (observe.Provider, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.provider, s.seen
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
