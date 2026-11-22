package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// dialScript is a streaming recognizer for session-lifecycle tests: every
// OpenStream hands out a fresh [fakeStream] (commits resolve with "ok") and keeps
// the session's OnClosed, so a test can kill session N the way the adapter reports
// a provider close. openErr, when set, fails the dial instead.
type dialScript struct {
	mu       sync.Mutex
	streams  []*fakeStream
	onClosed []func(error)
	openErr  error
	opened   chan int // receives the session index on every successful dial
}

func newDialScript() *dialScript { return &dialScript{opened: make(chan int, 64)} }

func (d *dialScript) OpenStream(_ context.Context, cfg stt.StreamConfig) (stt.Stream, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.openErr != nil {
		return nil, d.openErr
	}
	fs := &fakeStream{result: &stt.CommitResult{Transcript: stt.Transcript{Text: "ok"}}, onPartial: cfg.OnPartial}
	d.streams = append(d.streams, fs)
	d.onClosed = append(d.onClosed, cfg.OnClosed)
	d.opened <- len(d.streams) - 1
	return fs, nil
}

func (d *dialScript) session(i int) *fakeStream {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.streams[i]
}

// kill reports session i dead, as the adapter does when the provider closes it.
func (d *dialScript) kill(i int, cause error) {
	d.mu.Lock()
	cb := d.onClosed[i]
	d.mu.Unlock()
	cb(cause)
}

func (d *dialScript) setOpenErr(err error) {
	d.mu.Lock()
	d.openErr = err
	d.mu.Unlock()
}

// awaitDial waits for the next successful dial and returns its session index.
func (d *dialScript) awaitDial(t *testing.T) int {
	t.Helper()
	select {
	case i := <-d.opened:
		return i
	case <-time.After(2 * time.Second):
		t.Fatal("no dial observed")
		return -1
	}
}

// lifeClock is the manager's now/sleep seam for lifecycle tests: now only moves
// when the test advances it, and every sleep the maintainer asks for is recorded
// and returns at once.
type lifeClock struct {
	mu     sync.Mutex
	t      time.Time
	sleeps []time.Duration
}

func newLifeClock() *lifeClock { return &lifeClock{t: time.Unix(1_700_000_000, 0)} }

func (c *lifeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *lifeClock) advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func (c *lifeClock) sleep(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	c.mu.Unlock()
	return ctx.Err()
}

func (c *lifeClock) slept() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

func idleClose() error {
	return &stt.StreamError{Code: stt.CodeTransport, Fatal: true, Err: errors.New("websocket: close 1000 (normal)")}
}

// bindLifecycle binds m on a fresh bus with clock installed and returns once the
// eager first dial is up.
func bindLifecycle(t *testing.T, m *StreamManager, d *dialScript, clock *lifeClock) {
	t.Helper()
	m.now = clock.now
	m.sleep = clock.sleep
	stop := m.bind(t.Context(), voiceevent.NewBus())
	t.Cleanup(stop)
	if i := d.awaitDial(t); i != 0 {
		t.Fatalf("first dial opened session %d, want 0", i)
	}
}

// TestStreamManager_SessionDeath_RedialsBeforeTheNextUtterance is the headline
// fix (#702): the provider closes a session while the speaker is silent, the
// adapter reports it through OnClosed, and the manager has a fresh session up
// BEFORE the next utterance — which therefore streams, instead of being the one
// that discovers the death and pays for it with a batch round-trip.
func TestStreamManager_SessionDeath_RedialsBeforeTheNextUtterance(t *testing.T) {
	d := newDialScript()
	clock := newLifeClock()
	m := NewStreamManager(d)
	bindLifecycle(t, m, d, clock)

	clock.advance(15 * time.Second) // the provider's idle window
	d.kill(0, idleClose())
	if i := d.awaitDial(t); i != 1 {
		t.Fatalf("redial opened session %d, want 1", i)
	}
	if !m.WaitStreamUp(2 * time.Second) {
		t.Fatal("the redialed session was never installed")
	}
	if got := clock.slept(); len(got) != 0 {
		t.Errorf("the maintainer slept %v before redialing a session that had been healthy; want an immediate redial", got)
	}

	m.beginUtterance(clock.now())
	m.send(streamFrame(t, 1))
	c, ok := m.endUtterance()
	if !ok {
		t.Fatal("the utterance after an idle close did not stream")
	}
	if tr, ok := m.awaitCommit(c); !ok || tr.Text != "ok" {
		t.Fatalf("awaitCommit = (%+v, %v), want the committed text", tr, ok)
	}
	if n := len(d.session(1).sentFrames()); n != 1 {
		t.Errorf("fresh session received %d frames, want 1", n)
	}
	if n := len(d.session(0).sentFrames()); n != 0 {
		t.Errorf("dead session received %d frames, want 0", n)
	}
}

// TestStreamManager_HealthySessionDeaths_DoNotRatchetTheBackoff is the ratchet
// from the #702 research: a speaker who talked once every 15 s or more lost every
// session to the idle close before a commit could reset the backoff, so the
// redial delay climbed 1→2→4→…→30 s and streaming was effectively off. A session
// that lived long enough to be called healthy is redialed at once, every time.
func TestStreamManager_HealthySessionDeaths_DoNotRatchetTheBackoff(t *testing.T) {
	d := newDialScript()
	clock := newLifeClock()
	m := NewStreamManager(d, WithStreamBackoff(time.Second, 30*time.Second))
	bindLifecycle(t, m, d, clock)

	for i := range 6 {
		if !m.WaitStreamUp(2 * time.Second) {
			t.Fatalf("session %d never installed", i)
		}
		clock.advance(15 * time.Second)
		d.kill(i, idleClose())
		if got := d.awaitDial(t); got != i+1 {
			t.Fatalf("redial %d opened session %d", i, got)
		}
	}
	if got := clock.slept(); len(got) != 0 {
		t.Errorf("sleeps = %v, want none: healthy sessions are redialed at once", got)
	}
	m.mu.Lock()
	b := m.backoff
	m.mu.Unlock()
	if b != time.Second {
		t.Errorf("backoff = %v after six idle closes, want the 1s initial", b)
	}
}

// TestStreamManager_FlappingSession_BacksOff is the other side: a provider that
// accepts the dial and drops the session straight away (a concurrency or resource
// limit) must not be redialed in a tight loop just because each dial "succeeded".
func TestStreamManager_FlappingSession_BacksOff(t *testing.T) {
	d := newDialScript()
	clock := newLifeClock()
	m := NewStreamManager(d, WithStreamBackoff(time.Second, 30*time.Second))
	bindLifecycle(t, m, d, clock)

	for i := range 4 {
		if !m.WaitStreamUp(2 * time.Second) {
			t.Fatalf("session %d never installed", i)
		}
		// no clock advance: the session dies the instant it is up
		d.kill(i, &stt.StreamError{Code: "resource_exhausted", Fatal: true, Err: errors.New("busy")})
		d.awaitDial(t)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	got := clock.slept()
	if len(got) != len(want) {
		t.Fatalf("sleeps = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sleep[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestStreamManager_AuthClassDeath_WaitsAtTheCapEvenAfterALongLife: quota running
// out mid-session is durable, however long the session had been healthy.
func TestStreamManager_AuthClassDeath_WaitsAtTheCapEvenAfterALongLife(t *testing.T) {
	d := newDialScript()
	clock := newLifeClock()
	m := NewStreamManager(d, WithStreamBackoff(time.Second, 30*time.Second))
	bindLifecycle(t, m, d, clock)

	clock.advance(time.Hour)
	d.kill(0, &stt.StreamError{Code: "quota_exceeded", Fatal: true, Err: errors.New("out of credits")})
	d.awaitDial(t)
	if got := clock.slept(); len(got) != 1 || got[0] != 30*time.Second {
		t.Errorf("sleeps = %v, want one 30s wait at the cap", got)
	}
}

// TestStreamManager_DeathMidUtterance_UtteranceStaysBatchAfterTheRedial: when the
// session dies under an open utterance and the redial wins the race against the
// next frame, the utterance must NOT continue on the fresh session — that session
// would hold only its tail and commit a fragment as the authoritative final.
func TestStreamManager_DeathMidUtterance_UtteranceStaysBatchAfterTheRedial(t *testing.T) {
	d := newDialScript()
	clock := newLifeClock()
	spy := &spyStage{}
	m := NewStreamManager(d, WithStreamMetrics(spy, "elevenlabs"))
	bindLifecycle(t, m, d, clock)

	m.beginUtterance(clock.now())
	m.send(streamFrame(t, 1))
	clock.advance(time.Minute)
	d.kill(0, idleClose())
	d.awaitDial(t)
	if !m.WaitStreamUp(2 * time.Second) {
		t.Fatal("redial never installed")
	}
	m.send(streamFrame(t, 2))
	m.send(streamFrame(t, 3))

	if n := len(d.session(1).sentFrames()); n != 0 {
		t.Errorf("the fresh session received %d frames of an utterance that began on the dead one; want 0", n)
	}
	if _, ok := m.endUtterance(); ok {
		t.Fatal("endUtterance ok=true; a split utterance must go to batch")
	}
	if n := d.session(1).commitCount(); n != 0 {
		t.Errorf("the fresh session was asked to commit %d times; want 0", n)
	}
	want := spyFinal{observe.STTTransportBatchFallback, observe.STTFinalStreamDied}
	if got := spy.finalCounts(); len(got) != 1 || got[0] != want {
		t.Errorf("stt_final = %+v, want one %+v", got, want)
	}

	// The next utterance streams on the fresh session.
	m.beginUtterance(clock.now())
	m.send(streamFrame(t, 4))
	c, ok := m.endUtterance()
	if !ok {
		t.Fatal("the utterance after the redial did not stream")
	}
	if _, ok := m.awaitCommit(c); !ok {
		t.Fatal("its commit did not resolve")
	}
	if got := spy.finalCounts(); len(got) != 2 || got[1] != (spyFinal{observe.STTTransportStream, observe.STTFinalCommitted}) {
		t.Errorf("stt_final = %+v, want a second stream/committed", got)
	}
}

// TestStreamManager_CommitError_RetiresTheSession (#702): audio streamed for an
// utterance whose commit failed is still at the provider, uncommitted. On a
// session that lived on it would be transcribed as the start of the NEXT
// utterance. Any commit error therefore costs the session.
func TestStreamManager_CommitError_RetiresTheSession(t *testing.T) {
	d := newDialScript()
	clock := newLifeClock()
	spy := &spyStage{}
	m := NewStreamManager(d, WithStreamMetrics(spy, "elevenlabs"))
	bindLifecycle(t, m, d, clock)
	clock.advance(time.Minute)

	first := d.session(0)
	first.mu.Lock()
	first.result = &stt.CommitResult{Err: &stt.StreamError{Code: "some_future_commit_error", Fatal: false, Err: errors.New("nope")}}
	first.mu.Unlock()

	m.beginUtterance(clock.now())
	m.send(streamFrame(t, 1))
	c, ok := m.endUtterance()
	if !ok {
		t.Fatal("endUtterance ok=false on a live utterance")
	}
	if _, ok := m.awaitCommit(c); ok {
		t.Fatal("awaitCommit ok=true on a commit error")
	}
	if i := d.awaitDial(t); i != 1 {
		t.Fatalf("no redial after a commit error (opened %d)", i)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		first.mu.Lock()
		closed := first.closed
		first.mu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the session holding uncommitted audio was never closed")
		}
		time.Sleep(time.Millisecond)
	}
	want := spyFinal{observe.STTTransportBatchFallback, observe.STTFinalCommitError}
	if got := spy.finalCounts(); len(got) != 1 || got[0] != want {
		t.Errorf("stt_final = %+v, want one %+v", got, want)
	}
}

// TestStreamManager_CountsWhyAnUtteranceWentBatch pins the remaining fallback
// reasons of the stt_final counter: no session at speech start, and a commit that
// never resolves.
func TestStreamManager_CountsWhyAnUtteranceWentBatch(t *testing.T) {
	t.Run("stream_down", func(t *testing.T) {
		spy := &spyStage{}
		m := NewStreamManager(&fakeStreamingRecognizer{}, WithStreamMetrics(spy, "elevenlabs"))
		m.bus = voiceevent.NewBus()
		m.beginUtterance(time.Now()) // no session installed
		m.send(streamFrame(t, 1))
		if _, ok := m.endUtterance(); ok {
			t.Fatal("endUtterance ok=true with no session")
		}
		want := spyFinal{observe.STTTransportBatchFallback, observe.STTFinalStreamDown}
		if got := spy.finalCounts(); len(got) != 1 || got[0] != want {
			t.Errorf("stt_final = %+v, want one %+v", got, want)
		}
	})
	t.Run("commit_timeout", func(t *testing.T) {
		spy := &spyStage{}
		m := NewStreamManager(&fakeStreamingRecognizer{}, WithStreamMetrics(spy, "elevenlabs"),
			WithCommitTimeout(10*time.Millisecond))
		m.stream = &fakeStream{} // commits never resolve
		m.bus = voiceevent.NewBus()
		m.beginUtterance(time.Now())
		m.send(streamFrame(t, 1))
		c, ok := m.endUtterance()
		if !ok {
			t.Fatal("endUtterance ok=false on a live utterance")
		}
		if _, ok := m.awaitCommit(c); ok {
			t.Fatal("awaitCommit ok=true on a stalled commit")
		}
		want := spyFinal{observe.STTTransportBatchFallback, observe.STTFinalCommitTimeout}
		if got := spy.finalCounts(); len(got) != 1 || got[0] != want {
			t.Errorf("stt_final = %+v, want one %+v", got, want)
		}
	})
}

// TestStreamManager_LogsDialFailuresOncePerRun (#702): a deployment whose key is
// missing or rejected used to redial every 30 s forever without a single log
// line. The first failure of a run is logged, the retries are not, and the
// reconnect is.
func TestStreamManager_LogsDialFailuresOncePerRun(t *testing.T) {
	d := newDialScript()
	d.setOpenErr(&stt.StreamError{Code: "auth_error", Fatal: true, Err: errors.New("elevenlabs.OpenStream: missing API key")})
	logs := &lockedBuffer{}
	m := NewStreamManager(d, WithStreamLogger(slog.New(slog.NewTextHandler(logs, nil))))

	attempts := make(chan struct{}, 16)
	gate := make(chan struct{})
	m.sleep = func(ctx context.Context, _ time.Duration) error {
		attempts <- struct{}{}
		select {
		case <-gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	stop := m.bind(t.Context(), voiceevent.NewBus())
	t.Cleanup(stop)

	for range 3 { // three failed dials
		select {
		case <-attempts:
		case <-time.After(2 * time.Second):
			t.Fatal("no dial retry observed")
		}
		if n := strings.Count(logs.String(), "dial failed"); n != 1 {
			t.Fatalf("dial-failure log lines = %d, want exactly 1 for a run of identical failures\n%s", n, logs.String())
		}
		gate <- struct{}{}
	}
	<-attempts
	if !strings.Contains(logs.String(), "missing API key") || !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("the failure log does not carry the cause at WARN:\n%s", logs.String())
	}

	d.setOpenErr(nil)
	gate <- struct{}{}
	d.awaitDial(t)
	if !m.WaitStreamUp(2 * time.Second) {
		t.Fatal("never reconnected")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(logs.String(), "reconnected") {
		if time.Now().After(deadline) {
			t.Fatalf("the reconnect was not logged:\n%s", logs.String())
		}
		time.Sleep(time.Millisecond)
	}
}

// lockedBuffer is a goroutine-safe log sink.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// deadOnArrival reports its session closed BEFORE OpenStream returns it — the
// adapter documents that OnClosed may fire that early.
type deadOnArrival struct {
	mu    sync.Mutex
	opens int
	last  *fakeStream
}

func (d *deadOnArrival) OpenStream(_ context.Context, cfg stt.StreamConfig) (stt.Stream, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.opens++
	d.last = &fakeStream{}
	cfg.OnClosed(&stt.StreamError{Code: stt.CodeTransport, Fatal: true, Err: errors.New("closed during the handshake")})
	return d.last, nil
}

// TestStreamManager_SessionDeadOnArrival_IsNotInstalled: a session that ended
// before the dial returned must never become the live session (nothing would ever
// report its death again), and counts as a failed dial.
func TestStreamManager_SessionDeadOnArrival_IsNotInstalled(t *testing.T) {
	rec := &deadOnArrival{}
	m := NewStreamManager(rec)
	slept := make(chan time.Duration, 1)
	m.sleep = func(ctx context.Context, d time.Duration) error {
		select {
		case slept <- d:
		default:
		}
		<-ctx.Done()
		return ctx.Err()
	}
	stop := m.bind(t.Context(), voiceevent.NewBus())
	t.Cleanup(stop)

	select {
	case d := <-slept:
		if d != time.Second {
			t.Errorf("backoff after a dead-on-arrival session = %v, want the 1s initial", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the maintainer did not treat a dead-on-arrival session as a failed dial")
	}
	if m.sessionUp() {
		t.Error("a session that was already closed got installed")
	}
	rec.mu.Lock()
	last := rec.last
	rec.mu.Unlock()
	last.mu.Lock()
	closed := last.closed
	last.mu.Unlock()
	if !closed {
		t.Error("the dead-on-arrival session was not closed")
	}
}

// TestSegmenter_CountsLanesWithoutAStream: a lane that has no stream at all is
// counted as plain batch, with the reason an operator can act on — streaming is
// off, or the lane is past GLYPHOXA_STT_STREAM_MAX_LANES.
func TestSegmenter_CountsLanesWithoutAStream(t *testing.T) {
	frames := []audio.Frame{streamFrame(t, 1)}
	for _, tc := range []struct {
		name      string
		streaming bool
		speaker   string
		want      observe.STTFinalReason
	}{
		{"streaming off", false, "A", observe.STTFinalStreamingOff},
		{"streaming off, default lane", false, "", observe.STTFinalStreamingOff},
		{"lane past the cap", true, "A", observe.STTFinalLaneCap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bus := voiceevent.NewBus()
			spy := &spyStage{}
			seg := NewSegmenter(NewVAD(bus, silentSession{}),
				NewSTT(bus, stubRec{}, WithSTTMetrics(spy, observe.ProviderElevenLabs)))
			if tc.streaming {
				seg.laneStreamFactory = func(string) *StreamManager { return nil }
			}
			if err := seg.transcribe(transcribeJob{ctx: t.Context(), seg: frames, speakerID: tc.speaker}); err != nil {
				t.Fatalf("transcribe: %v", err)
			}
			want := spyFinal{observe.STTTransportBatch, tc.want}
			if got := spy.finalCounts(); len(got) != 1 || got[0] != want {
				t.Errorf("stt_final = %+v, want one %+v", got, want)
			}
		})
	}
}

// TestSegmenter_StreamedLaneIsNotCountedTwice: a lane WITH a stream is counted by
// its StreamManager (stream or batch_fallback); the Segmenter must not add a
// plain-batch count for the same utterance.
func TestSegmenter_StreamedLaneIsNotCountedTwice(t *testing.T) {
	bus := voiceevent.NewBus()
	spy := &spyStage{}
	seg := NewSegmenter(NewVAD(bus, silentSession{}),
		NewSTT(bus, stubRec{}, WithSTTMetrics(spy, observe.ProviderElevenLabs)))
	sm := NewStreamManager(&fakeStreamingRecognizer{}) // down: the utterance fell back before this job
	job := transcribeJob{ctx: t.Context(), seg: []audio.Frame{streamFrame(t, 1)}, stream: sm}
	if err := seg.transcribe(job); err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	if got := spy.finalCounts(); len(got) != 0 {
		t.Errorf("the Segmenter counted %+v for a lane that has a stream; its manager already did", got)
	}
}

// TestStreamManager_DialOnDemand_WaitsForTheFirstSpeech: the default (unattributed)
// lane carries no speech when inbound frames are speaker-stamped, yet it used to
// dial a session at bind. Harmless while the provider idled it out after 15 s; with
// the keepalive it would hold a realtime connection for the whole Voice Session.
// A dial-on-demand manager opens nothing until its lane first speaks.
func TestStreamManager_DialOnDemand_WaitsForTheFirstSpeech(t *testing.T) {
	d := newDialScript()
	spy := &spyStage{}
	m := NewStreamManager(d, WithStreamDialOnDemand(), WithStreamMetrics(spy, "elevenlabs"))
	stop := m.bind(t.Context(), voiceevent.NewBus())
	t.Cleanup(stop)

	select {
	case <-d.opened:
		t.Fatal("a dial-on-demand manager dialed at bind")
	case <-time.After(50 * time.Millisecond):
	}

	m.beginUtterance(time.Now()) // first speech: this utterance is batch, and it triggers the dial
	m.send(streamFrame(t, 1))
	if _, ok := m.endUtterance(); ok {
		t.Fatal("the first utterance streamed with no session up")
	}
	d.awaitDial(t)
	if !m.WaitStreamUp(2 * time.Second) {
		t.Fatal("the first speech did not bring a session up")
	}

	m.beginUtterance(time.Now())
	m.send(streamFrame(t, 2))
	c, ok := m.endUtterance()
	if !ok {
		t.Fatal("the second utterance did not stream")
	}
	if _, ok := m.awaitCommit(c); !ok {
		t.Fatal("its commit did not resolve")
	}
	want := []spyFinal{
		{observe.STTTransportBatchFallback, observe.STTFinalStreamDown},
		{observe.STTTransportStream, observe.STTFinalCommitted},
	}
	got := spy.finalCounts()
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("stt_final = %+v, want %+v", got, want)
	}
}

// TestStreamManager_LogsAnUnexpectedSessionEnd_NotTheTeardown: the end of a live
// session is worth a log line — unless it is the manager's own shutdown (a reaped
// lane, the Voice Session ending), where the adapter reports the close it was
// just asked for and "reconnecting" would be a lie repeated for every lane.
func TestStreamManager_LogsAnUnexpectedSessionEnd_NotTheTeardown(t *testing.T) {
	d := newDialScript()
	clock := newLifeClock()
	logs := &lockedBuffer{}
	m := NewStreamManager(d, WithStreamLogger(slog.New(slog.NewTextHandler(logs, nil))))
	m.now = clock.now
	m.sleep = clock.sleep
	ctx, cancel := context.WithCancel(t.Context())
	stop := m.bind(ctx, voiceevent.NewBus())
	d.awaitDial(t)
	if !m.WaitStreamUp(2 * time.Second) {
		t.Fatal("never up")
	}

	clock.advance(time.Minute)
	d.kill(0, idleClose())
	d.awaitDial(t)
	if !m.WaitStreamUp(2 * time.Second) {
		t.Fatal("never redialed")
	}
	if n := strings.Count(logs.String(), "session ended"); n != 1 {
		t.Fatalf("session-ended log lines = %d, want 1 for the unexpected close\n%s", n, logs.String())
	}

	cancel() // the Voice Session ends: the adapter closes the session and says so
	d.kill(1, &stt.StreamError{Code: stt.CodeTransport, Fatal: true, Err: context.Canceled})
	stop()
	if n := strings.Count(logs.String(), "session ended"); n != 1 {
		t.Errorf("the teardown logged a session end (%d lines total):\n%s", n, logs.String())
	}
	if !d.session(1).isClosed() {
		t.Error("the live session was not closed at teardown")
	}
}

func (f *fakeStream) isClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

// TestStreamManager_UnansweredCommits_BackOffHoweverOldTheSession is the provider
// brownout (#702 review): the handshake is accepted and the session sits there
// looking healthy, but no commit is ever answered. Age alone is not evidence the
// session works — a session retired for a commit that failed or never resolved
// has proved the opposite — so its redial must wait the growing backoff. With an
// instant, forgiven redial every turn would stream, wait the full commit timeout
// and fall back to batch for the whole incident.
func TestStreamManager_UnansweredCommits_BackOffHoweverOldTheSession(t *testing.T) {
	cases := []struct {
		name   string
		result *stt.CommitResult // nil = the commit never resolves
	}{
		{"commit timeout", nil},
		{"commit error", &stt.CommitResult{Err: &stt.StreamError{Code: "rate_limited", Err: errors.New("slow down")}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDialScript()
			clock := newLifeClock()
			m := NewStreamManager(d, WithStreamBackoff(time.Second, 30*time.Second),
				WithCommitTimeout(20*time.Millisecond))
			bindLifecycle(t, m, d, clock)

			for i := range 4 {
				if !m.WaitStreamUp(2 * time.Second) {
					t.Fatalf("session %d never installed", i)
				}
				s := d.session(i)
				s.mu.Lock()
				s.result = tc.result
				s.mu.Unlock()
				clock.advance(15 * time.Second) // well past the healthy lifetime
				m.beginUtterance(clock.now())
				m.send(streamFrame(t, 1))
				c, ok := m.endUtterance()
				if !ok {
					t.Fatalf("turn %d did not stream", i)
				}
				if _, ok := m.awaitCommit(c); ok {
					t.Fatalf("turn %d: awaitCommit ok=true on a failed commit", i)
				}
				d.awaitDial(t)
			}
			want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
			got := clock.slept()
			if len(got) != len(want) {
				t.Fatalf("sleeps = %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("sleep[%d] = %v, want %v", i, got[i], want[i])
				}
			}
		})
	}
}

// TestStreamManager_HealthyDeath_ForgivesAGrownBackoff: the idle close of a
// session that had been healthy does not just skip the sleep, it resets a backoff
// that earlier failures had grown.
func TestStreamManager_HealthyDeath_ForgivesAGrownBackoff(t *testing.T) {
	d := newDialScript()
	clock := newLifeClock()
	m := NewStreamManager(d, WithStreamBackoff(time.Second, 30*time.Second))
	bindLifecycle(t, m, d, clock)

	for i := range 3 { // flap: grows the backoff
		if !m.WaitStreamUp(2 * time.Second) {
			t.Fatalf("session %d never installed", i)
		}
		d.kill(i, &stt.StreamError{Code: "resource_exhausted", Fatal: true, Err: errors.New("busy")})
		d.awaitDial(t)
	}
	if !m.WaitStreamUp(2 * time.Second) {
		t.Fatal("session 3 never installed")
	}
	clock.advance(15 * time.Second)
	d.kill(3, idleClose())
	d.awaitDial(t)

	if got := clock.slept(); len(got) != 3 {
		t.Errorf("sleeps = %v, want three (the flaps) and none for the healthy death", got)
	}
	m.mu.Lock()
	b := m.backoff
	m.mu.Unlock()
	if b != time.Second {
		t.Errorf("backoff = %v after a healthy death, want the 1s initial", b)
	}
}
