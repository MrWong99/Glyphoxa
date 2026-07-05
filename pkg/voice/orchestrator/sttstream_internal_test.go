package orchestrator

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// fakeStream is a scriptable [stt.Stream] for StreamManager unit tests: it records
// the frames it is Sent and how many times it is Committed, can be told to fail
// Send, and resolves each Commit with a preset result (or leaves it pending for the
// commit-timeout path).
type fakeStream struct {
	mu        sync.Mutex
	sent      []audio.Frame
	commits   int
	closed    bool
	sendErr   error             // returned by Send once set
	result    *stt.CommitResult // resolves each Commit; nil leaves the commit pending
	onPartial func(string)
}

func (f *fakeStream) Send(frame audio.Frame) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, frame)
	return nil
}

func (f *fakeStream) Commit() (<-chan stt.CommitResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits++
	ch := make(chan stt.CommitResult, 1)
	if f.result != nil {
		ch <- *f.result
	}
	return ch, nil
}

func (f *fakeStream) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func (f *fakeStream) sentFrames() []audio.Frame {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]audio.Frame(nil), f.sent...)
}

func (f *fakeStream) commitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commits
}

// fakeStreamingRecognizer hands out a scripted [fakeStream] (or a dial error) and
// counts OpenStream calls; it wires each session's OnPartial back onto the stream
// so a test can drive partials through the adapter's callback path.
type fakeStreamingRecognizer struct {
	mu      sync.Mutex
	opens   int
	stream  *fakeStream
	openErr error
	lastCfg stt.StreamConfig
}

func (r *fakeStreamingRecognizer) OpenStream(_ context.Context, cfg stt.StreamConfig) (stt.Stream, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opens++
	r.lastCfg = cfg
	if r.openErr != nil {
		return nil, r.openErr
	}
	if r.stream != nil {
		r.stream.onPartial = cfg.OnPartial
	}
	return r.stream, nil
}

func (r *fakeStreamingRecognizer) openCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.opens
}

// spyStage records STTRequest calls; every other StageRecorder method is the no-op
// from the embedded Discard.
type spyStage struct {
	observe.Discard
	mu           sync.Mutex
	sttRequests  int
	lastProvider observe.Provider
}

func (s *spyStage) STTRequest(p observe.Provider, _ time.Duration) {
	s.mu.Lock()
	s.sttRequests++
	s.lastProvider = p
	s.mu.Unlock()
}

func (s *spyStage) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sttRequests
}

// streamFrame builds a 32 ms / 16 kHz frame with marker written to sample 0, so a
// test can read back which frame reached the stream and in what order.
func streamFrame(t *testing.T, marker int16) audio.Frame {
	t.Helper()
	s := make([]int16, 512)
	s[0] = marker
	f, err := audio.NewFrame(s, streamSampleRate, 32)
	if err != nil {
		t.Fatalf("audio.NewFrame: %v", err)
	}
	return f
}

// TestStreamManager_BeginSendsPreRollThenLiveFrames pins the per-utterance wire
// contract: idle frames only fill the pre-roll ring (silence is not billed);
// beginUtterance streams the ring first, in order; voiced frames follow via send;
// and endUtterance requests exactly one manual commit for a live utterance.
func TestStreamManager_BeginSendsPreRollThenLiveFrames(t *testing.T) {
	fs := &fakeStream{result: &stt.CommitResult{Transcript: stt.Transcript{Text: "ok"}}}
	m := NewStreamManager(&fakeStreamingRecognizer{}, WithPreRoll(3))
	m.stream = fs
	m.bus = voiceevent.NewBus()

	m.idleFrame(streamFrame(t, 1))
	m.idleFrame(streamFrame(t, 2))
	if got := len(fs.sentFrames()); got != 0 {
		t.Fatalf("idle frames streamed %d frames; want 0 (silence must not be billed)", got)
	}

	id := m.beginUtterance(time.Now())
	if id == "" {
		t.Fatal("beginUtterance returned an empty utterance id")
	}
	m.send(streamFrame(t, 9))

	sent := fs.sentFrames()
	if len(sent) != 3 {
		t.Fatalf("streamed %d frames; want 3 (2 pre-roll + 1 voiced)", len(sent))
	}
	markers := []int16{sent[0].Samples()[0], sent[1].Samples()[0], sent[2].Samples()[0]}
	want := []int16{1, 2, 9}
	for i := range want {
		if markers[i] != want[i] {
			t.Fatalf("streamed frame order = %v, want %v (pre-roll ring in order, then voiced)", markers, want)
		}
	}

	commit, _, ok := m.endUtterance()
	if !ok || commit == nil {
		t.Fatalf("endUtterance = (nil? %v, ok=%v); want a commit handle for a live utterance", commit == nil, ok)
	}
	if fs.commitCount() != 1 {
		t.Errorf("Commit called %d times; want exactly 1", fs.commitCount())
	}
}

// TestStreamManager_IdleFrameRingIsBounded pins that the pre-roll ring keeps only
// the most recent preRoll frames (a paused speaker leaves unbounded idle silence).
func TestStreamManager_IdleFrameRingIsBounded(t *testing.T) {
	fs := &fakeStream{}
	m := NewStreamManager(&fakeStreamingRecognizer{}, WithPreRoll(2))
	m.stream = fs
	m.bus = voiceevent.NewBus()

	for i := int16(1); i <= 5; i++ {
		m.idleFrame(streamFrame(t, i))
	}
	m.beginUtterance(time.Now())

	sent := fs.sentFrames()
	if len(sent) != 2 {
		t.Fatalf("pre-roll streamed %d frames; want 2 (bounded ring)", len(sent))
	}
	if sent[0].Samples()[0] != 4 || sent[1].Samples()[0] != 5 {
		t.Errorf("pre-roll = [%d %d], want the two most recent idle frames [4 5]",
			sent[0].Samples()[0], sent[1].Samples()[0])
	}
}

// TestStreamManager_PublishesPartialsWithDedupe pins the STTPartial contract:
// interim texts publish with the utterance id, a consecutive duplicate is deduped,
// and once the utterance is committed no further partial publishes (no open
// utterance to attribute it to).
func TestStreamManager_PublishesPartialsWithDedupe(t *testing.T) {
	bus := voiceevent.NewBus()
	var partials []voiceevent.STTPartial
	voiceevent.On(bus, func(e voiceevent.STTPartial) { partials = append(partials, e) })

	fs := &fakeStream{result: &stt.CommitResult{Transcript: stt.Transcript{Text: "hello"}}}
	m := NewStreamManager(&fakeStreamingRecognizer{})
	m.stream = fs
	m.bus = bus

	id := m.beginUtterance(time.Now())
	m.onPartial("he")
	m.onPartial("he") // consecutive duplicate → deduped
	m.onPartial("hello")

	if len(partials) != 2 {
		t.Fatalf("published %d partials; want 2 (consecutive duplicate deduped)", len(partials))
	}
	if partials[0].Text != "he" || partials[1].Text != "hello" {
		t.Errorf("partial texts = [%q %q], want [he hello]", partials[0].Text, partials[1].Text)
	}
	if partials[0].UtteranceID != id || partials[1].UtteranceID != id {
		t.Errorf("partials not stamped with the utterance id %q", id)
	}

	m.endUtterance()
	m.onPartial("late")
	if len(partials) != 2 {
		t.Errorf("a partial published after endUtterance (%d total); want dropped (no open utterance)", len(partials))
	}
}

// TestStreamManager_SendFailureMakesUtteranceBatch pins mid-utterance stream death
// (AC): a fatal send drops the dead session, nudges the maintainer, and forces the
// utterance onto the batch path (endUtterance ok=false), so no in-flight utterance
// is lost.
func TestStreamManager_SendFailureMakesUtteranceBatch(t *testing.T) {
	fs := &fakeStream{sendErr: &stt.StreamError{Code: stt.CodeTransport, Fatal: true, Err: errors.New("ws dead")}}
	m := NewStreamManager(&fakeStreamingRecognizer{})
	m.stream = fs
	m.bus = voiceevent.NewBus()

	m.beginUtterance(time.Now())
	m.send(streamFrame(t, 1))

	if _, _, ok := m.endUtterance(); ok {
		t.Fatal("endUtterance ok=true after a fatal send; want batch fallback (ok=false)")
	}
	m.mu.Lock()
	s := m.stream
	m.mu.Unlock()
	if s != nil {
		t.Error("a fatal send did not drop the dead session")
	}
	select {
	case <-m.poke:
	default:
		t.Error("a fatal send did not poke the maintainer to re-establish")
	}
}

// TestStreamManager_StreamDownAtBeginIsBatch pins the "stream down at speech_start"
// AC: the utterance is pure batch (no mid-utterance catch-up) and the maintainer is
// nudged to heal in the background.
func TestStreamManager_StreamDownAtBeginIsBatch(t *testing.T) {
	m := NewStreamManager(&fakeStreamingRecognizer{})
	m.bus = voiceevent.NewBus() // m.stream stays nil: session down

	m.beginUtterance(time.Now())
	m.send(streamFrame(t, 1)) // no-op: utterance already batch
	if _, _, ok := m.endUtterance(); ok {
		t.Fatal("endUtterance ok=true with no session; want batch (ok=false)")
	}
	select {
	case <-m.poke:
	default:
		t.Error("a stream-down begin did not nudge the maintainer")
	}
}

// TestStreamManager_AwaitCommitSuccess pins the happy path: a resolved commit
// yields the committed transcript (ok=true), records one stt_request span, and
// resets the backoff (a healthy session forgives past failures).
func TestStreamManager_AwaitCommitSuccess(t *testing.T) {
	spy := &spyStage{}
	m := NewStreamManager(&fakeStreamingRecognizer{},
		WithStreamMetrics(spy, observe.ProviderElevenLabs),
		WithStreamBackoff(time.Second, 30*time.Second),
		WithCommitTimeout(time.Second))
	m.backoff = 8 * time.Second // grown by prior failures

	ch := make(chan stt.CommitResult, 1)
	ch <- stt.CommitResult{Transcript: stt.Transcript{Text: "roll a d20"}}
	tr, ok := m.awaitCommit(ch, time.Now())
	if !ok || tr.Text != "roll a d20" {
		t.Fatalf("awaitCommit = (%q, %v); want (roll a d20, true)", tr.Text, ok)
	}
	if spy.requests() != 1 {
		t.Errorf("stt_request recorded %d times; want exactly 1 per streamed commit", spy.requests())
	}
	if spy.lastProvider != observe.ProviderElevenLabs {
		t.Errorf("stt_request provider = %q, want elevenlabs", spy.lastProvider)
	}
	m.mu.Lock()
	b := m.backoff
	m.mu.Unlock()
	if b != time.Second {
		t.Errorf("backoff after a successful commit = %v, want reset to the initial 1s", b)
	}
}

// TestStreamManager_AwaitCommitEmptyIsFinal pins insufficient_audio parity: an
// empty committed transcript with a nil error is a SUCCESS (ok=true, empty text),
// not a fallback — the batch path publishes an empty STTFinal too.
func TestStreamManager_AwaitCommitEmptyIsFinal(t *testing.T) {
	m := NewStreamManager(&fakeStreamingRecognizer{}, WithCommitTimeout(time.Second))
	ch := make(chan stt.CommitResult, 1)
	ch <- stt.CommitResult{Transcript: stt.Transcript{Text: ""}}
	tr, ok := m.awaitCommit(ch, time.Now())
	if !ok {
		t.Fatal("empty commit treated as a fallback; want ok=true (empty text is a valid final)")
	}
	if tr.Text != "" {
		t.Errorf("empty commit text = %q, want empty", tr.Text)
	}
}

// TestStreamManager_AwaitCommitTimesOut pins the commit-timeout guard (R2): a
// stalled pending commit falls back to batch (ok=false) rather than wedging the
// worker.
func TestStreamManager_AwaitCommitTimesOut(t *testing.T) {
	m := NewStreamManager(&fakeStreamingRecognizer{}, WithCommitTimeout(20*time.Millisecond))
	ch := make(chan stt.CommitResult) // never resolves

	start := time.Now()
	if _, ok := m.awaitCommit(ch, time.Now()); ok {
		t.Fatal("awaitCommit ok=true on a stalled commit; want false → batch fallback")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("awaitCommit blocked %v; the commit timeout did not fire", elapsed)
	}
}

// TestStreamManager_AwaitCommitErrorFallsBack pins that a provider commit error
// falls back to batch (ok=false) and does not reset the backoff.
func TestStreamManager_AwaitCommitErrorFallsBack(t *testing.T) {
	m := NewStreamManager(&fakeStreamingRecognizer{}, WithStreamBackoff(time.Second, 30*time.Second), WithCommitTimeout(time.Second))
	m.backoff = 8 * time.Second
	ch := make(chan stt.CommitResult, 1)
	ch <- stt.CommitResult{Err: &stt.StreamError{Code: "commit_throttled", Fatal: false, Err: errors.New("throttled")}}
	if _, ok := m.awaitCommit(ch, time.Now()); ok {
		t.Fatal("awaitCommit ok=true on a commit error; want false → batch fallback")
	}
	m.mu.Lock()
	b := m.backoff
	m.mu.Unlock()
	if b != 8*time.Second {
		t.Errorf("a recoverable commit error changed the backoff to %v; want it unchanged", b)
	}
}

// TestStreamManager_AuthClassBacksOffAtCap pins that an auth-class error jumps the
// re-establish backoff straight to the cap (a durable rejection must not hammer),
// and that a healthy commit resets it back to the initial delay.
func TestStreamManager_AuthClassBacksOffAtCap(t *testing.T) {
	m := NewStreamManager(&fakeStreamingRecognizer{}, WithStreamBackoff(time.Second, 30*time.Second))
	if m.backoff != time.Second {
		t.Fatalf("initial backoff = %v, want 1s", m.backoff)
	}
	m.noteAuthBackoff(&stt.StreamError{Code: "auth_error", Fatal: true})
	if m.backoff != 30*time.Second {
		t.Errorf("after auth_error backoff = %v, want the 30s cap", m.backoff)
	}
	m.resetBackoff()
	if m.backoff != time.Second {
		t.Errorf("after a healthy commit backoff = %v, want reset to 1s", m.backoff)
	}
}

// TestStreamManager_BoundedBackoff pins the dial re-establish schedule: with the
// sleep seam injected, repeated dial failures back off 1s → 2s → 4s → … → 30s cap,
// no jitter.
func TestStreamManager_BoundedBackoff(t *testing.T) {
	rec := &fakeStreamingRecognizer{openErr: &stt.StreamError{Code: stt.CodeTransport, Fatal: true, Err: errors.New("dial fail")}}
	m := NewStreamManager(rec, WithStreamBackoff(time.Second, 30*time.Second))

	delays := make(chan time.Duration)
	release := make(chan struct{})
	m.sleep = func(ctx context.Context, d time.Duration) error {
		select {
		case delays <- d:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	stop := m.bind(ctx, voiceevent.NewBus())
	defer stop()
	defer cancel()

	want := []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 30 * time.Second, 30 * time.Second,
	}
	for i, w := range want {
		select {
		case d := <-delays:
			if d != w {
				t.Errorf("backoff sleep[%d] = %v, want %v", i, d, w)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("backoff sleep[%d]: none observed", i)
		}
		release <- struct{}{}
	}
	if rec.openCount() < len(want) {
		t.Errorf("only %d dial attempts; want at least %d (one per failed dial)", rec.openCount(), len(want))
	}
}
