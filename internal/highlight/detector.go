package highlight

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/tape"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

const (
	// featureMailboxCap is the PCM-tap mailbox depth. Matched to the tape's append
	// mailbox: deep enough to absorb scheduling jitter, dropped (never blocking the
	// audio loop) once full — the highlight signal is best-effort (ADR-0020).
	featureMailboxCap = 512
	// windowCap bounds the rolling transcript window fed to the classifier. A
	// classify sees at most this many recent finals; older lines roll off.
	windowCap = 40
)

// finalLine is one transcript final retained in the rolling window.
type finalLine struct {
	speaker string
	text    string
	at      time.Time
}

// render formats a line for the classifier prompt (deterministic, ADR-0021).
func (l finalLine) render() string {
	who := "Speaker"
	if l.speaker != "" {
		who = "Speaker " + l.speaker
	}
	return who + ": " + l.text
}

// Detector is the per-session highlight moment detector. Construct it with
// [NewDetector] per wirenpc Voice Session cycle and defer [Detector.Close]. Safe
// for concurrent bus callbacks and PCM-tap calls; all detector state lives on the
// single worker goroutine.
type Detector struct {
	provider llm.Provider
	model    string
	snap     SnapshotFunc
	sink     Sink
	gate     orchestrator.TurnGate
	metrics  observe.StageRecorder
	log      *slog.Logger
	cfg      Config

	now func() time.Time // injected in tests; time.Now in production

	ctx    context.Context
	cancel context.CancelFunc
	unsub  func()
	done   chan struct{}

	signal   chan struct{}     // 1-slot wake for a pending final (latest-wins)
	features chan frameFeature // buffered PCM feature mailbox (drop-oldest under load)

	mailMu     sync.Mutex
	pending    voiceevent.STTFinal
	hasPending bool

	// classified, when non-nil (set by white-box tests before start), receives one
	// value per completed classification so a test can await the async worker.
	classified chan classification
	// handled, when non-nil (white-box tests), is notified after each final is
	// folded into the window, so a test can serialize publishing (defeating the
	// latest-wins coalescing) for deterministic cadence assertions.
	handled chan struct{}
}

// workerState is the detector state owned solely by the worker goroutine.
type workerState struct {
	window              []finalLine
	feat                featureState
	finalsSinceClassify int
	consecutiveHigh     int
	candidateCount      int
	lastTriggerAt       time.Time
	disarmed            bool
}

// NewDetector builds the detector wired to the process bus (STTFinal), the LLM
// classifier provider/model, the tape snapshot cutter, the trigger sink, the spend
// gate, and the stage-metrics recorder. It subscribes to the bus and launches the
// single worker goroutine immediately; call [Detector.Close] at session end.
func NewDetector(bus *voiceevent.Bus, provider llm.Provider, model string, snap SnapshotFunc, sink Sink, gate orchestrator.TurnGate, metrics observe.StageRecorder, log *slog.Logger, cfg Config) *Detector {
	d := newDetector(provider, model, snap, sink, gate, metrics, log, cfg)
	d.start(bus)
	return d
}

// newDetector builds the struct with production seams but does NOT subscribe or
// start the goroutine — the split lets white-box tests inject the now clock and the
// classified notify channel BEFORE the worker reads them (no data race), then call
// [Detector.start]. Production goes through [NewDetector].
func newDetector(provider llm.Provider, model string, snap SnapshotFunc, sink Sink, gate orchestrator.TurnGate, metrics observe.StageRecorder, log *slog.Logger, cfg Config) *Detector {
	if log == nil {
		log = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	}
	if metrics == nil {
		metrics = observe.Discard{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Detector{
		provider: provider,
		model:    model,
		snap:     snap,
		sink:     sink,
		gate:     gate,
		metrics:  metrics,
		log:      log,
		cfg:      cfg.withDefaults(),
		now:      time.Now,
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
		signal:   make(chan struct{}, 1),
		features: make(chan frameFeature, featureMailboxCap),
	}
}

// start subscribes to the bus and launches the worker. Called once by [NewDetector]
// (production) or a white-box test after seams are set.
func (d *Detector) start(bus *voiceevent.Bus) {
	d.unsub = voiceevent.On(bus, d.onFinal)
	go d.worker()
}

// Close unsubscribes from the bus and stops the worker. Idempotent enough to defer:
// unsubscribe and cancel are no-ops on a second call and the closed done channel
// returns immediately — but wire it once, at session end (a leak is a #44 bug).
func (d *Detector) Close() {
	d.unsub()
	d.cancel()
	<-d.done
}

// PCMTap returns the tap wired via wire.WithPCMTap: it summarizes each decoded PCM
// frame's energy inline and hands it to the buffered feature mailbox, dropping
// under load. It NEVER blocks the audio loop (ADR-0020/0026).
func (d *Detector) PCMTap() func(audio.Frame) {
	return func(f audio.Frame) {
		ff := computeFrameFeature(f)
		select {
		case d.features <- ff:
		default:
			// Mailbox full: drop the oldest to make room, then retry once. The audio
			// loop never waits (the highlight signal is best-effort).
			select {
			case <-d.features:
			default:
			}
			select {
			case d.features <- ff:
			default:
			}
		}
	}
}

// onFinal is the bus callback: latest-wins mailbox, never blocks (ADR-0020). Under
// load faster than the worker, intermediate finals are coalesced — acceptable
// degradation for a best-effort side-consumer (internal/recall precedent).
func (d *Detector) onFinal(e voiceevent.STTFinal) {
	d.mailMu.Lock()
	d.pending = e
	d.hasPending = true
	d.mailMu.Unlock()
	select {
	case d.signal <- struct{}{}:
	default:
	}
}

// take pops the pending final, or reports none.
func (d *Detector) take() (voiceevent.STTFinal, bool) {
	d.mailMu.Lock()
	defer d.mailMu.Unlock()
	if !d.hasPending {
		return voiceevent.STTFinal{}, false
	}
	e := d.pending
	d.hasPending = false
	return e, true
}

// worker is the single owner of all detector state. It folds PCM features and
// handles finals until Close (ctx cancel). The done channel is closed on exit.
func (d *Detector) worker() {
	defer close(d.done)
	w := &workerState{feat: featureState{}}
	for {
		select {
		case <-d.ctx.Done():
			return
		case ff := <-d.features:
			w.feat.fold(ff)
		case <-d.signal:
			if e, ok := d.take(); ok {
				d.handleFinal(w, e)
			}
		}
	}
}

// handleFinal appends the final to the rolling window and classifies every
// ClassifyEvery processed finals.
func (d *Detector) handleFinal(w *workerState, e voiceevent.STTFinal) {
	defer d.notifyHandled()
	w.appendLine(e)
	w.finalsSinceClassify++
	if w.finalsSinceClassify < d.cfg.ClassifyEvery {
		return
	}
	w.finalsSinceClassify = 0
	d.classify(w, e)
}

// notifyHandled is the white-box serialization hook (no-op in production).
func (d *Detector) notifyHandled() {
	if d.handled == nil {
		return
	}
	select {
	case d.handled <- struct{}{}:
	default:
	}
}

// appendLine adds a final to the window, dropping the oldest past windowCap.
func (w *workerState) appendLine(e voiceevent.STTFinal) {
	text := strings.TrimSpace(e.Text)
	if text == "" {
		return
	}
	w.window = append(w.window, finalLine{speaker: e.SpeakerID, text: text, at: e.At})
	if len(w.window) > windowCap {
		w.window = w.window[len(w.window)-windowCap:]
	}
}

// classify runs one classifier pass, honoring the cap, cooldown, and spend gate,
// and promotes to a trigger after ConfirmWindows consecutive at-or-above-Bar
// scores.
func (d *Detector) classify(w *workerState, e voiceevent.STTFinal) {
	now := d.now()
	// Per-session cap: once enough candidates are found, stop classifying (and stop
	// spending) for the rest of the session (ADR-0051 bounded candidates).
	if w.candidateCount >= d.cfg.MaxCandidates {
		return
	}
	// A gate that has ever denied a turn disarms the detector permanently: AllowTurn
	// is monotonic (ADR-0046). A Highlight never ends a session, so this is silent.
	if w.disarmed {
		return
	}
	// Cooldown: after a trigger, suppress classification (and reset the streak) so a
	// single sustained moment yields one highlight, then rearm.
	if !w.lastTriggerAt.IsZero() && now.Sub(w.lastTriggerAt) < d.cfg.Cooldown {
		w.consecutiveHigh = 0
		return
	}
	// Spend gate: never classify (an LLM call is spend) when the session's soft cap
	// is crossed. Checked before EVERY classify (ADR-0046).
	if d.gate != nil && !d.gate.AllowTurn() {
		w.disarmed = true
		d.log.Info("highlight detector disarmed: spend gate closed")
		return
	}

	req := buildRequest(d.model, w.window, w.feat.summarize())
	cls := d.runClassifier(req)
	d.notifyClassified(cls)

	if cls.score >= d.cfg.Bar {
		w.consecutiveHigh++
	} else {
		w.consecutiveHigh = 0
	}
	if w.consecutiveHigh < d.cfg.ConfirmWindows {
		return
	}
	d.emit(w, e, cls, now)
	w.consecutiveHigh = 0
	w.candidateCount++
	w.lastTriggerAt = now
}

// emit cuts the tape snapshot AT trigger time and hands the trigger to the sink.
func (d *Detector) emit(w *workerState, e voiceevent.STTFinal, cls classification, now time.Time) {
	at := e.At
	if at.IsZero() {
		at = now
	}
	from := at.Add(-d.cfg.Lead)
	to := at.Add(d.cfg.Tail)
	// Clamp From into the tape's retention window: audio older than the ring is gone
	// anyway, and the snapshot must not claim a range the tape cannot back.
	if lo := now.Add(-tape.Window); from.Before(lo) {
		from = lo
	}
	var snap tape.Snapshot
	if d.snap != nil {
		snap = d.snap(from, to)
	}
	excerpt := cls.excerpt
	if excerpt == "" {
		excerpt = w.recentText()
	}
	d.sink.HandleTrigger(Trigger{
		At:         at,
		From:       from,
		To:         to,
		Score:      cls.score,
		SpeakerIDs: w.speakerIDs(),
		Excerpt:    excerpt,
		Reason:     cls.reason,
		Snapshot:   snap,
	})
}

// runClassifier drives one provider completion, meters its token usage on the
// stage recorder (ADR-0045/0046), and parses the verdict. It never crashes the
// worker: a provider error, a truncated stream, or malformed JSON yields a zero
// score (the moment is simply not confirmed).
func (d *Detector) runClassifier(req llm.Request) classification {
	stream, err := d.provider.Complete(d.ctx, req)
	if err != nil {
		d.log.Warn("highlight classify: llm complete", "err", err)
		return classification{}
	}
	var sb strings.Builder
	var usage llm.Usage
	var haveUsage bool
	for ev := range stream {
		switch ev.Type {
		case llm.EventText:
			sb.WriteString(ev.Text)
		case llm.EventUsage:
			usage, haveUsage = ev.Usage, true
		case llm.EventError:
			d.log.Warn("highlight classify: llm stream error", "err", ev.Err)
		}
	}
	in, out := usage.InputTokens, usage.OutputTokens
	if !haveUsage {
		in = estimateTokens(promptRunes(req))
		out = estimateTokens(utf8.RuneCountInString(sb.String()))
	}
	d.metrics.LLMTokens(d.cfg.ProviderLabel, d.model, in, out)
	return parseClassification(sb.String())
}

// notifyClassified is the white-box test hook: production leaves classified nil, so
// it is a no-op. The buffered test channel is drained by the test.
func (d *Detector) notifyClassified(c classification) {
	if d.classified == nil {
		return
	}
	select {
	case d.classified <- c:
	default:
	}
}

// recentText joins the tail of the window into a fallback excerpt.
func (w *workerState) recentText() string {
	n := len(w.window)
	if n > 4 {
		n = 4
	}
	parts := make([]string, 0, n)
	for _, l := range w.window[len(w.window)-n:] {
		parts = append(parts, l.text)
	}
	return strings.Join(parts, " ")
}

// speakerIDs returns the distinct non-empty Speaker Lanes in the window, in
// first-seen order.
func (w *workerState) speakerIDs() []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range w.window {
		if l.speaker == "" || seen[l.speaker] {
			continue
		}
		seen[l.speaker] = true
		out = append(out, l.speaker)
	}
	return out
}
