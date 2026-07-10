package wirenpc

import (
	"log/slog"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/highlight"
	"github.com/MrWong99/Glyphoxa/internal/tape"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// fakeHighlightSink records triggers for the wiring assertions.
type fakeHighlightSink struct{ n int }

func (s *fakeHighlightSink) HandleTrigger(highlight.Trigger) { s.n++ }

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
