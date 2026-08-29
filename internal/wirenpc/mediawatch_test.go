package wirenpc

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
)

// stallMetrics counts MediaStallRebuild; every other MetricsRecorder method is
// unreachable through the watchdog, so the embedded nil interface never fires.
type stallMetrics struct {
	gxvoice.MetricsRecorder
	mu sync.Mutex
	n  int
}

func (m *stallMetrics) MediaStallRebuild(string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.n++
}

func (m *stallMetrics) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.n
}

// watchHarness drives mediaWatchdog.check by hand — the idleclose test shape:
// a hand-advanced clock, a mutable liveness snapshot, and the cancel cause
// observable on a real context.
type watchHarness struct {
	w       *mediaWatchdog
	ctx     context.Context
	metrics *stallMetrics
	logs    *bytes.Buffer

	mu       sync.Mutex
	liveness gxvoice.MediaLiveness
	ok       bool
	suspects int
	now      time.Time
}

var watchBase = time.Date(2026, 8, 29, 20, 0, 0, 0, time.UTC)

func newWatchHarness(t *testing.T, window time.Duration) *watchHarness {
	t.Helper()
	ctx, cancel := context.WithCancelCause(context.Background())
	t.Cleanup(func() { cancel(nil) })
	h := &watchHarness{ctx: ctx, metrics: &stallMetrics{}, logs: &bytes.Buffer{}, ok: true, now: watchBase}
	h.w = newMediaWatchdog(
		func() (gxvoice.MediaLiveness, bool) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.liveness, h.ok
		},
		cancel,
		func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.suspects++
		},
		h.metrics,
		slog.New(slog.NewTextHandler(h.logs, nil)),
		"g1",
		window,
	)
	// run() stamps these; the by-hand tests drive check() directly, so mirror
	// run's initialization here.
	h.w.lastMove = watchBase
	h.w.lastAnyMove = watchBase
	h.w.lastLog = watchBase
	return h
}

func (h *watchHarness) set(ml gxvoice.MediaLiveness, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.liveness = ml
	h.ok = ok
}

// advance moves the clock and runs one check, like idleclose's advance helper.
func (h *watchHarness) advance(d time.Duration) {
	h.mu.Lock()
	h.now = h.now.Add(d)
	now := h.now
	h.mu.Unlock()
	h.w.check(now)
}

func (h *watchHarness) suspectCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.suspects
}

func TestMediaWatchdogQuietTableNeverFires(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, 45*time.Second)
	// Packets flowed once, then a genuinely quiet table: no Speaking events.
	h.set(gxvoice.MediaLiveness{Packets: 100}, true)
	for range 60 { // 10 minutes of silence
		h.advance(10 * time.Second)
	}
	if h.w.fired {
		t.Fatal("watchdog fired on a quiet table with no speaking evidence")
	}
	if context.Cause(h.ctx) != nil {
		t.Fatal("cycle was cancelled on a quiet table")
	}
}

func TestMediaWatchdogFiresOnStallWithSpeakingEvidence(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, 45*time.Second)
	h.set(gxvoice.MediaLiveness{Packets: 100}, true)
	h.advance(10 * time.Second) // counter move observed; lastMove = base+10s

	// The pipe dies; participants keep announcing speech on the healthy ws.
	h.set(gxvoice.MediaLiveness{Packets: 100, Keepalives: 7, LastSpeaking: watchBase.Add(30 * time.Second)}, true)
	h.advance(20 * time.Second) // 20s stall: under the window, no verdict
	if h.w.fired {
		t.Fatal("fired before the stall window elapsed")
	}
	h.advance(40 * time.Second) // 60s since lastMove: verdict
	if !h.w.fired {
		t.Fatal("did not fire on a stall with speaking evidence")
	}
	if cause := context.Cause(h.ctx); !errors.Is(cause, errMediaStall) {
		t.Fatalf("cycle cause = %v, want errMediaStall", cause)
	}
	if h.metrics.count() != 1 {
		t.Fatalf("MediaStallRebuild recorded %d times, want 1", h.metrics.count())
	}
	if h.suspectCount() != 1 {
		t.Fatalf("MediaSuspect called %d times, want 1", h.suspectCount())
	}
	if !bytes.Contains(h.logs.Bytes(), []byte("inbound media path stalled")) {
		t.Fatal("stall verdict was not logged")
	}
}

func TestMediaWatchdogMovingPacketsResetTheWindow(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, 45*time.Second)
	packets := uint64(0)
	for i := range 20 { // 200s of healthy flow with steady speaking
		packets += 50
		h.set(gxvoice.MediaLiveness{Packets: packets, LastSpeaking: watchBase.Add(time.Duration(i) * 10 * time.Second)}, true)
		h.advance(10 * time.Second)
	}
	if h.w.fired {
		t.Fatal("fired while packets kept flowing")
	}
}

func TestMediaWatchdogSpeakingBeforeLastAudioIsNotEvidence(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, 45*time.Second)
	// Someone spoke, audio flowed, and THEN the table went quiet: the speaking
	// event predates the last audio movement, so silence is not suspicious.
	h.set(gxvoice.MediaLiveness{Packets: 100, LastSpeaking: watchBase.Add(5 * time.Second)}, true)
	h.advance(10 * time.Second) // move observed at base+10s, after the speak
	for range 30 {
		h.advance(10 * time.Second)
	}
	if h.w.fired {
		t.Fatal("fired although the last speaking event predates the last audio")
	}
}

func TestMediaWatchdogNeverReceivedDoublesTheWindow(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, 45*time.Second)
	// No packet ever arrived (e.g. a long DAVE handshake), but someone speaks
	// (comfortably past the one-tick slack on lastMove, which starts at base).
	h.set(gxvoice.MediaLiveness{Packets: 0, LastSpeaking: watchBase.Add(20 * time.Second)}, true)
	h.advance(60 * time.Second) // over the window, under 2x
	if h.w.fired {
		t.Fatal("fired inside the doubled never-received grace window")
	}
	h.advance(40 * time.Second) // 100s > 90s
	if !h.w.fired {
		t.Fatal("never fired on a connection that never received a packet despite speaking")
	}
}

func TestMediaWatchdogDisabledWindowLogsButNeverFires(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, -1)
	h.set(gxvoice.MediaLiveness{Packets: 100, LastSpeaking: watchBase.Add(30 * time.Second)}, true)
	for range 30 { // 5 minutes of dead pipe with speaking evidence
		h.advance(10 * time.Second)
	}
	if h.w.fired {
		t.Fatal("fired although the verdict is disabled")
	}
	if !bytes.Contains(h.logs.Bytes(), []byte("voice media liveness")) {
		t.Fatal("liveness log did not run with the verdict disabled")
	}
}

func TestMediaWatchdogNoTransportMonitorStaysInert(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, 45*time.Second)
	h.set(gxvoice.MediaLiveness{LastSpeaking: watchBase.Add(10 * time.Second)}, false)
	for range 30 {
		h.advance(10 * time.Second)
	}
	if h.w.fired {
		t.Fatal("fired without a transport monitor: no signal is not no traffic")
	}
	if bytes.Contains(h.logs.Bytes(), []byte("voice media liveness")) {
		t.Fatal("liveness log ran without a transport monitor to read")
	}
}

func TestMediaWatchdogLivenessLogCadence(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, 45*time.Second)
	h.set(gxvoice.MediaLiveness{Packets: 10, Keepalives: 3}, true)
	for range 6 { // one minute
		h.advance(10 * time.Second)
	}
	logs := h.logs.String()
	if got := bytes.Count([]byte(logs), []byte("voice media liveness")); got != 1 {
		t.Fatalf("liveness lines after 60s = %d, want exactly 1", got)
	}
	if !bytes.Contains([]byte(logs), []byte("packets_delta=10")) {
		t.Fatalf("liveness line missing packet delta: %s", logs)
	}
}

func TestClassifyFatalTreatsMediaStallAsTransient(t *testing.T) {
	t.Parallel()
	if fe := classifyFatal(errMediaStall); fe != nil {
		t.Fatalf("classifyFatal(errMediaStall) = %v; a media stall must reconnect, not kill the session", fe)
	}
}

func TestMediaWatchdogEchoCessationFiresWithoutSpeaking(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, 45*time.Second)
	// The socket proved the server echoes keepalives; then EVERYTHING stops —
	// the #633 signature, with no reliance on Speaking relays at all.
	h.set(gxvoice.MediaLiveness{Packets: 100, Echoes: 5, Keepalives: 20}, true)
	h.advance(10 * time.Second)
	h.advance(20 * time.Second)
	if h.w.fired {
		t.Fatal("fired before the stall window")
	}
	h.advance(30 * time.Second) // 50s since anything arrived
	if !h.w.fired {
		t.Fatal("echo cessation did not fire: the watchdog depends on Speaking relays Discord does not guarantee")
	}
	if cause := context.Cause(h.ctx); !errors.Is(cause, errMediaStall) {
		t.Fatalf("cycle cause = %v, want errMediaStall", cause)
	}
	if h.suspectCount() != 1 || h.metrics.count() != 1 {
		t.Fatalf("suspect/metric = %d/%d, want 1/1", h.suspectCount(), h.metrics.count())
	}
}

func TestMediaWatchdogQuietTableWithEchoesNeverFires(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, 45*time.Second)
	// A quiet table on a HEALTHY socket: RTP stops but the server keeps
	// echoing our keepalives — the echoes moving is proof of life.
	echoes := uint64(0)
	for range 90 { // 15 minutes of silence
		echoes += 2
		h.set(gxvoice.MediaLiveness{Packets: 100, Echoes: echoes, Keepalives: echoes}, true)
		h.advance(10 * time.Second)
	}
	if h.w.fired {
		t.Fatal("fired on a quiet table whose socket was demonstrably alive")
	}
}

func TestMediaWatchdogUnprovenEchoesNeverArmTheEchoTier(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, 45*time.Second)
	// A server generation that never echoes: echo count stays under the arm
	// threshold, so echo absence alone must never be read as death.
	h.set(gxvoice.MediaLiveness{Packets: 100, Echoes: 2, Keepalives: 50}, true)
	for range 60 { // 10 minutes of quiet
		h.advance(10 * time.Second)
	}
	if h.w.fired {
		t.Fatal("fired on echo absence although the socket never proved the server echoes")
	}
}

func TestMediaWatchdogCounterResetRestartsTheGrace(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, 45*time.Second)
	h.set(gxvoice.MediaLiveness{Packets: 500, Echoes: 5}, true)
	h.advance(10 * time.Second)

	// The transport re-opened (voice server migration): both counters reset.
	// The rebuilt path earns a fresh grace window — and a fresh, DOUBLED one
	// for the speaking tier, since the new socket has yet to carry a packet.
	// Speaking evidence from BEFORE the reset must not convict the new socket,
	// so the announcement below lands after it.
	h.set(gxvoice.MediaLiveness{Packets: 0, Echoes: 0}, true)
	h.advance(10 * time.Second) // reset observed at base+20s
	if h.w.everMoved {
		t.Fatal("a reset packet counter did not reset everMoved")
	}
	h.set(gxvoice.MediaLiveness{Packets: 0, Echoes: 0, LastSpeaking: watchBase.Add(35 * time.Second)}, true)
	h.advance(60 * time.Second) // base+80s: 60s stall < doubled 90s window
	if h.w.fired {
		t.Fatal("fired inside the re-opened connection's doubled grace window")
	}
	h.advance(40 * time.Second) // base+120s: 100s > 90s
	if !h.w.fired {
		t.Fatal("never fired on a re-opened connection that stayed dead despite speaking")
	}
}

func TestMediaWatchdogRunLifecycle(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	ticks := make(chan time.Time)
	var mu sync.Mutex
	ml := gxvoice.MediaLiveness{Packets: 1, Echoes: 5}
	now := watchBase
	w := newMediaWatchdog(func() (gxvoice.MediaLiveness, bool) {
		mu.Lock()
		defer mu.Unlock()
		return ml, true
	}, cancel, nil, nil, slog.New(slog.DiscardHandler), "g1", 45*time.Second)
	w.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	w.ticks = ticks

	done := make(chan struct{})
	go func() { w.run(ctx); close(done) }()

	// Healthy tick, then everything freezes past the window: run must fire on
	// its injected ticks and exit after firing (it fires at most once).
	tick := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
		ticks <- now
	}
	tick(10 * time.Second)
	tick(50 * time.Second)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not exit after firing")
	}
	if cause := context.Cause(ctx); !errors.Is(cause, errMediaStall) {
		t.Fatalf("cause = %v, want errMediaStall", cause)
	}
}

func TestMediaWatchdogRunExitsOnCycleEnd(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancelCause(context.Background())
	w := newMediaWatchdog(func() (gxvoice.MediaLiveness, bool) { return gxvoice.MediaLiveness{}, true },
		cancel, nil, nil, slog.New(slog.DiscardHandler), "g1", 45*time.Second)
	w.ticks = make(chan time.Time) // never fired

	done := make(chan struct{})
	go func() { w.run(ctx); close(done) }()
	cancel(nil)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run leaked past its cycle's cancellation")
	}
}

func TestResolveStallCause(t *testing.T) {
	t.Parallel()
	plain := context.Background()
	if err := resolveStallCause(plain, nil); err != nil {
		t.Fatalf("clean cycle rewritten: %v", err)
	}

	fired, cancel := context.WithCancelCause(context.Background())
	cancel(errMediaStall)
	if err := resolveStallCause(fired, context.Canceled); !errors.Is(err, errMediaStall) {
		t.Fatalf("watchdog cancellation = %v, want errMediaStall", err)
	}
	// An independent error in the same tick keeps its identity — rewriting it
	// could hide a fatal classification.
	other := errors.New("gateway exploded")
	if err := resolveStallCause(fired, other); !errors.Is(err, other) {
		t.Fatalf("independent error rewritten to %v", err)
	}
}
