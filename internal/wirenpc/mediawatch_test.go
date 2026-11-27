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

// set installs a snapshot from a connection whose DAVE session is ready — the
// state every tier-1/tier-2 test is about. setRaw leaves DaveReady as given.
func (h *watchHarness) set(ml gxvoice.MediaLiveness, ok bool) {
	ml.DaveReady = true
	h.setRaw(ml, ok)
}

func (h *watchHarness) setRaw(ml gxvoice.MediaLiveness, ok bool) {
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

func TestMediaWatchdogDisabledWindowNeverFiresOnDaveNotReady(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, -1)
	h.setRaw(gxvoice.MediaLiveness{Echoes: 5, Keepalives: 20, DavePeers: 1}, true)
	for range 30 { // 5 minutes of a stuck handshake
		h.advance(10 * time.Second)
	}
	if h.w.fired {
		t.Fatal("fired although the verdict is disabled")
	}
	if !bytes.Contains(h.logs.Bytes(), []byte("dave_ready=false")) {
		t.Fatalf("liveness line does not say the receiver is gated: %s", h.logs)
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

func TestMediaWatchdogAloneInEmptyChannelNeverFires(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, 45*time.Second)
	// A Voice Session started before any player joined: Discord establishes
	// the DAVE group only with a second member, so the Bot is not ready for as
	// long as it sits alone. Nothing is wrong and a rebuild would only rejoin
	// the same empty channel — Idle Close is this table's only policy.
	keepalives := uint64(0)
	for range 120 { // 20 minutes, past a whole Idle Close Window
		keepalives += 2
		h.setRaw(gxvoice.MediaLiveness{Keepalives: keepalives}, true)
		h.advance(10 * time.Second)
	}
	if h.w.fired {
		t.Fatalf("a Bot waiting alone in an empty channel was convicted: %s", h.logs)
	}
	if h.suspectCount() != 0 || h.metrics.count() != 0 {
		t.Fatalf("suspect/metric = %d/%d, want 0/0", h.suspectCount(), h.metrics.count())
	}
	if !bytes.Contains(h.logs.Bytes(), []byte("dave_ready=false dave_peers=0")) {
		t.Fatalf("liveness line does not show the Bot waiting alone: %s", h.logs)
	}
}

func TestMediaWatchdogLeftAloneAfterBeingReadyNeverFires(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, 45*time.Second)
	h.set(gxvoice.MediaLiveness{Packets: 100, Echoes: 5, Keepalives: 20, DavePeers: 1}, true)
	h.advance(10 * time.Second)
	// Everyone left and the voice connection re-negotiated in place: the group
	// is unestablished again and the Bot is alone. Being ready once does not
	// make this a stuck handshake.
	h.setRaw(gxvoice.MediaLiveness{Packets: 100, Echoes: 5, Keepalives: 24}, true)
	for range 60 {
		h.advance(10 * time.Second)
	}
	if h.w.fired {
		t.Fatalf("a Bot left alone was convicted: %s", h.logs)
	}
}

func TestMediaWatchdogPeerJoiningALoneBotGetsAFullWindow(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, 45*time.Second)
	h.setRaw(gxvoice.MediaLiveness{Keepalives: 3}, true)
	for range 30 { // five minutes alone
		h.advance(10 * time.Second)
	}
	// A player joins. The time spent alone is not handshake time: the group
	// gets the whole (doubled, never-ready) window from here.
	h.setRaw(gxvoice.MediaLiveness{Keepalives: 3, DavePeers: 1}, true)
	h.advance(10 * time.Second) // peer observed at base+310s
	for range 8 {               // 80s of handshake, under the doubled 90s
		h.advance(10 * time.Second)
	}
	if h.w.fired {
		t.Fatalf("the wait alone was charged to the handshake: %s", h.logs)
	}
	// The player leaves again before the window is out: back to waiting.
	h.setRaw(gxvoice.MediaLiveness{Keepalives: 3}, true)
	for range 30 {
		h.advance(10 * time.Second)
	}
	if h.w.fired {
		t.Fatalf("fired after the only peer left: %s", h.logs)
	}
}

func TestMediaWatchdogNeverReadyWithSpeakingPeerFiresDaveNotReady(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, 45*time.Second)
	h.setRaw(gxvoice.MediaLiveness{Keepalives: 3}, true)
	h.advance(10 * time.Second) // not-ready observed at base+10s
	// No peer was ever announced, but a remote participant announced speech
	// during the spell: someone is there, so this is a handshake.
	h.setRaw(gxvoice.MediaLiveness{Keepalives: 3, LastSpeaking: watchBase.Add(15 * time.Second)}, true)
	h.advance(10 * time.Second) // evidence observed at base+20s
	h.advance(80 * time.Second) // 80s of handshake
	if h.w.fired {
		t.Fatal("fired inside a first handshake's doubled grace window")
	}
	h.advance(10 * time.Second) // 90s
	if !h.w.fired {
		t.Fatal("a handshake that never completed while someone spoke was never convicted")
	}
	if !bytes.Contains(h.logs.Bytes(), []byte("evidence=dave_not_ready")) {
		t.Fatalf("verdict not labelled dave_not_ready: %s", h.logs)
	}
}

func TestMediaWatchdogDaveNeverReadyWithPeerFiresDaveNotReady(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, 45*time.Second)
	// A first handshake that never completes although another member is in
	// the channel: the receiver never reads, so no echo ever arms tier 1 and
	// nobody has to announce speech for tier 2.
	h.setRaw(gxvoice.MediaLiveness{Keepalives: 3, DavePeers: 1}, true)
	h.advance(10 * time.Second) // handshake observed at base+10s
	for range 8 {               // base+90s: 80s gated, under the doubled 90s
		h.advance(10 * time.Second)
	}
	if h.w.fired {
		t.Fatal("fired inside a first handshake's doubled grace window")
	}
	h.advance(10 * time.Second) // 90s gated
	if !h.w.fired {
		t.Fatal("a handshake that never completed was never convicted")
	}
	if cause := context.Cause(h.ctx); !errors.Is(cause, errMediaStall) {
		t.Fatalf("cycle cause = %v, want errMediaStall", cause)
	}
	if h.suspectCount() != 1 || h.metrics.count() != 1 {
		t.Fatalf("suspect/metric = %d/%d, want 1/1", h.suspectCount(), h.metrics.count())
	}
	if !bytes.Contains(h.logs.Bytes(), []byte("evidence=dave_not_ready")) {
		t.Fatalf("verdict not labelled dave_not_ready: %s", h.logs)
	}
}

func TestMediaWatchdogStuckHandshakeOnArmedSocketIsNotEchoCessation(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, 45*time.Second)
	h.set(gxvoice.MediaLiveness{Packets: 100, Echoes: 5, Keepalives: 20}, true)
	h.advance(10 * time.Second)
	h.advance(10 * time.Second)

	// A re-handshake sticks: the receiver stops reading, so both counters
	// freeze exactly as they would on a dead socket.
	h.setRaw(gxvoice.MediaLiveness{Packets: 100, Echoes: 5, Keepalives: 24, DavePeers: 1}, true)
	h.advance(10 * time.Second) // not-ready observed at base+30s
	h.advance(40 * time.Second) // 40s gated: under the window
	if h.w.fired {
		t.Fatal("fired before the stall window")
	}
	h.advance(10 * time.Second) // 50s gated; ready before, so no doubling
	if !h.w.fired {
		t.Fatal("a stuck handshake on a once-ready connection was not convicted after one window")
	}
	if !bytes.Contains(h.logs.Bytes(), []byte("evidence=dave_not_ready")) {
		t.Fatalf("verdict not labelled dave_not_ready: %s", h.logs)
	}
	if bytes.Contains(h.logs.Bytes(), []byte("echo_cessation")) {
		t.Fatalf("a gated receiver was blamed on the socket: %s", h.logs)
	}
}

func TestMediaWatchdogHandshakeWindowIsNotChargedToOtherTiers(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, 45*time.Second)
	armed := gxvoice.MediaLiveness{Packets: 100, Echoes: 5, Keepalives: 20, DavePeers: 1}
	h.set(armed, true)
	h.advance(10 * time.Second) // counters seen moving at base+10s

	h.setRaw(armed, true)
	h.advance(10 * time.Second) // not-ready observed at base+20s
	h.advance(30 * time.Second) // base+50s: 30s gated, under the window

	// The handshake completes, the counters are still where the gate froze
	// them, and someone is talking. The gated time must not count as stall.
	armed.LastSpeaking = watchBase.Add(55 * time.Second)
	h.set(armed, true)
	h.advance(10 * time.Second) // base+60s: ready again
	h.advance(10 * time.Second)
	h.advance(20 * time.Second) // base+90s: 30s since readiness returned
	if h.w.fired {
		t.Fatalf("the handshake window was charged as a stall: %s", h.logs)
	}
	h.advance(20 * time.Second) // base+110s: 50s of real silence since ready
	if !h.w.fired {
		t.Fatal("a socket that stayed dead after the handshake was never convicted")
	}
	if !bytes.Contains(h.logs.Bytes(), []byte("evidence=echo_cessation")) {
		t.Fatalf("verdict after readiness returned should be echo_cessation: %s", h.logs)
	}
}

func TestMediaWatchdogShortHandshakeNeverFires(t *testing.T) {
	t.Parallel()
	h := newWatchHarness(t, 45*time.Second)
	h.setRaw(gxvoice.MediaLiveness{DavePeers: 1}, true)
	h.advance(10 * time.Second)
	h.advance(10 * time.Second) // an ordinary handshake
	echoes := uint64(0)
	for range 30 { // 5 minutes of a healthy, quiet socket
		echoes += 2
		h.set(gxvoice.MediaLiveness{Echoes: echoes, Keepalives: echoes, DavePeers: 1}, true)
		h.advance(10 * time.Second)
	}
	if h.w.fired {
		t.Fatalf("fired after an ordinary handshake: %s", h.logs)
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
	// The cycle the watchdog ends. run is handed a DIFFERENT context, one that
	// outlives the verdict: run returning below is then run's own "fired, nothing
	// left to watch" exit, not the cancellation it has just caused.
	cycle, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	runCtx, stopRun := context.WithCancel(context.Background())
	defer stopRun()

	ticks := make(chan time.Time)
	var mu sync.Mutex
	now := watchBase
	reads := 0                        // clock reads the watchdog has made
	readSig := make(chan struct{}, 1) // wakes awaitReads; one token is enough
	w := newMediaWatchdog(func() (gxvoice.MediaLiveness, bool) {
		return gxvoice.MediaLiveness{Packets: 1, Echoes: 5, DaveReady: true}, true
	}, cancel, nil, nil, slog.New(slog.DiscardHandler), "g1", 45*time.Second)
	w.now = func() time.Time {
		mu.Lock()
		at := now
		reads++
		mu.Unlock()
		select {
		case readSig <- struct{}{}:
		default:
		}
		return at
	}
	w.ticks = ticks

	done := make(chan struct{})
	go func() { w.run(runCtx); close(done) }()

	// awaitReads blocks until the watchdog has read the clock n times. The clock
	// may only move once the watchdog has taken the reading it is owed: run reads
	// it on its own goroutine AFTER receiving a tick, so a test that advanced the
	// clock for the next tick straight after delivering this one could hand this
	// tick's check the next tick's time — and the stall would never be observed.
	awaitReads := func(n int) {
		t.Helper()
		timeout := time.After(5 * time.Second)
		for {
			mu.Lock()
			got := reads
			mu.Unlock()
			if got >= n {
				return
			}
			select {
			case <-readSig:
			case <-timeout:
				t.Fatalf("the watchdog read the clock %d times, want %d", got, n)
			}
		}
	}
	// tick advances the clock, delivers one tick and waits for the watchdog to
	// read the clock for it.
	tick := func(d time.Duration) {
		t.Helper()
		mu.Lock()
		now = now.Add(d)
		at, before := now, reads
		mu.Unlock()
		select {
		case ticks <- at:
		case <-time.After(5 * time.Second):
			t.Fatal("run did not take the tick")
		}
		awaitReads(before + 1)
	}

	// Healthy tick, then everything freezes past the window: run must fire on
	// its injected ticks and exit after firing (it fires at most once).
	awaitReads(1)          // run's start stamp is base, not a later reading
	tick(10 * time.Second) // base+10s: counters seen moving, the stall clock starts
	tick(50 * time.Second) // base+60s: 50s with nothing inbound >= the 45s window

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not exit after firing")
	}
	if cause := context.Cause(cycle); !errors.Is(cause, errMediaStall) {
		t.Fatalf("cause = %v, want errMediaStall", cause)
	}
}

func TestMediaWatchdogRunExitsOnCycleEnd(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancelCause(context.Background())
	w := newMediaWatchdog(func() (gxvoice.MediaLiveness, bool) { return gxvoice.MediaLiveness{DaveReady: true}, true },
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
