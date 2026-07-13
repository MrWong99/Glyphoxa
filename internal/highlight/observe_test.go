package highlight

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/tape"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// ---- observability test doubles --------------------------------------------

// logCapture is a slog.Handler that retains every record so a test can assert the
// level, message and attrs the detector logged.
type logCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.records = append(c.records, r.Clone())
	c.mu.Unlock()
	return nil
}
func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

// find returns the first record with the given message, or ok=false.
func (c *logCapture) find(msg string) (slog.Record, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.records {
		if r.Message == msg {
			return r, true
		}
	}
	return slog.Record{}, false
}

// attrValue reads one attr's value off a record.
func attrValue(r slog.Record, key string) (slog.Value, bool) {
	var v slog.Value
	found := false
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			v, found = a.Value, true
			return false
		}
		return true
	})
	return v, found
}

// captureMetrics is a StageRecorder that also implements the highlight-classify
// outcome sink, retaining every outcome the detector counts.
type captureMetrics struct {
	observe.Discard
	mu       sync.Mutex
	outcomes []observe.HighlightOutcome
}

func (m *captureMetrics) HighlightClassify(o observe.HighlightOutcome) {
	m.mu.Lock()
	m.outcomes = append(m.outcomes, o)
	m.mu.Unlock()
}

func (m *captureMetrics) all() []observe.HighlightOutcome {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]observe.HighlightOutcome(nil), m.outcomes...)
}

// errCompleteProvider fails the Complete call outright (an llm_error before any
// stream frame).
type errCompleteProvider struct{}

func (errCompleteProvider) Complete(context.Context, llm.Request) (<-chan llm.StreamEvent, error) {
	return nil, errors.New("complete boom")
}

// streamErrProvider completes but surfaces an in-stream error frame.
type streamErrProvider struct{}

func (streamErrProvider) Complete(context.Context, llm.Request) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 2)
	ch <- llm.StreamEvent{Type: llm.EventError, Err: "mid-stream boom"}
	ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
	close(ch)
	return ch, nil
}

// startObserved builds and starts a detector with an injected clock, classified
// notify channel, capturing metrics and log — the observability-test analogue of
// buildDetector.
func startObserved(t *testing.T, bus *voiceevent.Bus, prov llm.Provider, snap SnapshotFunc, sink Sink, gate orchestrator.TurnGate, metrics observe.StageRecorder, log *slog.Logger, cfg Config) (*Detector, *testClock, <-chan classification) {
	t.Helper()
	clk := &testClock{t: time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)}
	d := newDetector(prov, "test-model", snap, sink, gate, metrics, log, cfg)
	d.now = clk.now
	classified := make(chan classification, 256)
	d.classified = classified
	d.handled = make(chan struct{}, 1)
	d.start(bus)
	t.Cleanup(d.Close)
	return d, clk, classified
}

// ---- tests -----------------------------------------------------------------

// TestClassifyBelowBarLogsInfo (AC1): a classify that parses and scores below Bar
// emits one INFO line carrying the score, a parsed=true flag, and the window line
// count, and counts outcome ok.
func TestClassifyBelowBarLogsInfo(t *testing.T) {
	bus := voiceevent.NewBus()
	prov := &fakeProvider{respJSON: func(int) string { return scoreJSON(2.0) }}
	sink := &fakeSink{}
	cap := &logCapture{}
	metrics := &captureMetrics{}
	d, clk, classified := startObserved(t, bus, prov, nil, sink, allowGate{}, metrics, slog.New(cap), Config{ClassifyEvery: 6, Bar: 8.0})

	for i := 0; i < 6; i++ {
		publishFinals(t, d, bus, clk, "A", 1)
	}
	awaitClassifications(t, classified, 1)

	rec, ok := cap.find("highlight classify")
	if !ok {
		t.Fatal("no INFO classify log line emitted")
	}
	if rec.Level != slog.LevelInfo {
		t.Errorf("classify log level = %v, want INFO", rec.Level)
	}
	if v, ok := attrValue(rec, "score"); !ok || v.Float64() != 2.0 {
		t.Errorf("score attr = %v (ok=%v), want 2.0", v, ok)
	}
	if v, ok := attrValue(rec, "parsed"); !ok || v.Bool() != true {
		t.Errorf("parsed attr = %v (ok=%v), want true", v, ok)
	}
	if v, ok := attrValue(rec, "window"); !ok || v.Int64() != 6 {
		t.Errorf("window attr = %v (ok=%v), want 6", v, ok)
	}
	if got := metrics.all(); len(got) != 1 || got[0] != observe.HighlightOK {
		t.Errorf("outcomes = %v, want [ok]", got)
	}
}

// TestClassifyParseFailWarns (AC2): a completed stream whose text carries no
// parseable verdict logs a WARN with a rune-bounded excerpt and counts parse_failed.
func TestClassifyParseFailWarns(t *testing.T) {
	long := strings.Repeat("é", 500) // 500 runes, 1000 bytes — no JSON object at all
	bus := voiceevent.NewBus()
	prov := &fakeProvider{respJSON: func(int) string { return long }}
	sink := &fakeSink{}
	cap := &logCapture{}
	metrics := &captureMetrics{}
	d, clk, classified := startObserved(t, bus, prov, nil, sink, allowGate{}, metrics, slog.New(cap), Config{ClassifyEvery: 2, Bar: 8.0})

	publishFinals(t, d, bus, clk, "A", 2)
	awaitClassifications(t, classified, 1)

	rec, ok := cap.find("highlight classify: unparseable verdict")
	if !ok {
		t.Fatal("no WARN parse-fail log line emitted")
	}
	if rec.Level != slog.LevelWarn {
		t.Errorf("parse-fail log level = %v, want WARN", rec.Level)
	}
	v, ok := attrValue(rec, "excerpt")
	if !ok {
		t.Fatal("no excerpt attr on parse-fail WARN")
	}
	exc := v.String()
	if n := utf8.RuneCountInString(exc); n > 200 {
		t.Errorf("excerpt rune count = %d, want <=200 (bounded)", n)
	}
	if !strings.HasPrefix(long, exc) {
		t.Errorf("excerpt is not a prefix of the raw model text")
	}
	if got := metrics.all(); len(got) != 1 || got[0] != observe.HighlightParseFailed {
		t.Errorf("outcomes = %v, want [parse_failed]", got)
	}
}

// TestClassifyLLMErrorCounts (AC3): a provider Complete error and an in-stream
// error frame both count llm_error, and the existing WARNs are retained.
func TestClassifyLLMErrorCounts(t *testing.T) {
	t.Run("complete error", func(t *testing.T) {
		bus := voiceevent.NewBus()
		sink := &fakeSink{}
		cap := &logCapture{}
		metrics := &captureMetrics{}
		d, clk, classified := startObserved(t, bus, errCompleteProvider{}, nil, sink, allowGate{}, metrics, slog.New(cap), Config{ClassifyEvery: 2})

		publishFinals(t, d, bus, clk, "A", 2)
		awaitClassifications(t, classified, 1)

		if _, ok := cap.find("highlight classify: llm complete"); !ok {
			t.Error("existing complete-error WARN not retained")
		}
		if got := metrics.all(); len(got) != 1 || got[0] != observe.HighlightLLMError {
			t.Errorf("outcomes = %v, want [llm_error]", got)
		}
	})

	t.Run("stream error frame", func(t *testing.T) {
		bus := voiceevent.NewBus()
		sink := &fakeSink{}
		cap := &logCapture{}
		metrics := &captureMetrics{}
		d, clk, classified := startObserved(t, bus, streamErrProvider{}, nil, sink, allowGate{}, metrics, slog.New(cap), Config{ClassifyEvery: 2})

		publishFinals(t, d, bus, clk, "A", 2)
		awaitClassifications(t, classified, 1)

		if _, ok := cap.find("highlight classify: llm stream error"); !ok {
			t.Error("existing stream-error WARN not retained")
		}
		if got := metrics.all(); len(got) != 1 || got[0] != observe.HighlightLLMError {
			t.Errorf("outcomes = %v, want [llm_error]", got)
		}
	})
}

// TestTriggerLogsInfo (AC4): a confirmed trigger logs at INFO with the score and
// the clip From/To range.
func TestTriggerLogsInfo(t *testing.T) {
	bus := voiceevent.NewBus()
	prov := &fakeProvider{respJSON: func(int) string { return scoreJSON(9.0) }}
	sink := &fakeSink{}
	snap := func(from, to time.Time) tape.Snapshot { return tape.Snapshot{From: from, To: to} }
	cap := &logCapture{}
	metrics := &captureMetrics{}
	d, clk, _ := startObserved(t, bus, prov, snap, sink, allowGate{}, metrics, slog.New(cap), Config{
		ClassifyEvery: 2, Bar: 8.0, ConfirmWindows: 1, Cooldown: time.Hour,
		Lead: 15 * time.Second, Tail: 40 * time.Millisecond,
	})

	at := clk.now()
	publishFinals(t, d, bus, clk, "A", 2)
	waitForTriggers(t, sink, 1)

	rec, ok := cap.find("highlight trigger confirmed")
	if !ok {
		t.Fatal("no INFO trigger log line emitted")
	}
	if rec.Level != slog.LevelInfo {
		t.Errorf("trigger log level = %v, want INFO", rec.Level)
	}
	if v, ok := attrValue(rec, "score"); !ok || v.Float64() != 9.0 {
		t.Errorf("score attr = %v (ok=%v), want 9.0", v, ok)
	}
	if v, ok := attrValue(rec, "from"); !ok || v.Time().Before(at.Add(-16*time.Second)) || v.Time().After(at.Add(-14*time.Second)) {
		t.Errorf("from attr = %v (ok=%v), want ~at-15s", v, ok)
	}
	if _, ok := attrValue(rec, "to"); !ok {
		t.Error("no to attr on trigger INFO")
	}
}

// TestOutcomeCounterIncrementsPerPass (AC5): every classify pass increments the
// outcome counter exactly once — three low-score passes yield three ok increments.
func TestOutcomeCounterIncrementsPerPass(t *testing.T) {
	bus := voiceevent.NewBus()
	prov := &fakeProvider{respJSON: func(int) string { return scoreJSON(1.0) }}
	sink := &fakeSink{}
	metrics := &captureMetrics{}
	d, clk, classified := startObserved(t, bus, prov, nil, sink, allowGate{}, metrics, slog.New(&logCapture{}), Config{ClassifyEvery: 2, Bar: 8.0})

	for i := 0; i < 3; i++ {
		publishFinals(t, d, bus, clk, "A", 2)
		awaitClassifications(t, classified, 1)
	}

	if got := metrics.all(); len(got) != 3 {
		t.Fatalf("outcome count = %d, want 3 (one per pass)", len(got))
	}
	for i, o := range metrics.all() {
		if o != observe.HighlightOK {
			t.Errorf("outcome[%d] = %q, want ok", i, o)
		}
	}
}
