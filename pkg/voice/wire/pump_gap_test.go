package wire

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// fakePumpRecorder collects what the pump records, so a test can assert on the
// inter-sentence gap samples and look-ahead lane events the real Prometheus
// adapter would see.
type fakePumpRecorder struct {
	mu     sync.Mutex
	gaps   []time.Duration
	events []observe.LookaheadEvent
}

func (r *fakePumpRecorder) IntersentenceGap(d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gaps = append(r.gaps, d)
}

func (r *fakePumpRecorder) PlaybackLookahead(ev observe.LookaheadEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *fakePumpRecorder) snapshotGaps() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.gaps...)
}

func (r *fakePumpRecorder) snapshotEvents() []observe.LookaheadEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]observe.LookaheadEvent(nil), r.events...)
}

// gapPlayer models disgo's 20 ms sender goroutine, which the plain fakePlayer does
// not: it PULLS the first frame from the playback Source, which is what stamps the
// first-frame moment the gap is measured to. delay pauses before that pull, so the
// injected silence is a lower bound on the recorded gap.
type gapPlayer struct {
	started chan *fakePlayback
	delay   time.Duration
}

func newGapPlayer(delay time.Duration) *gapPlayer {
	return &gapPlayer{started: make(chan *fakePlayback, 8), delay: delay}
}

func (p *gapPlayer) Play(ctx context.Context, src gxvoice.Source) (playback, error) {
	time.Sleep(p.delay)
	_, _ = src.NextFrame(ctx)
	pb := &fakePlayback{done: make(chan struct{})}
	p.started <- pb
	return pb, nil
}

func (p *gapPlayer) waitPlay(t *testing.T) *fakePlayback {
	t.Helper()
	select {
	case pb := <-p.started:
		return pb
	case <-time.After(time.Second):
		t.Fatal("Play was never called")
		return nil
	}
}

// turnCtx is an ordinary (non-look-ahead) sentence context keyed to a turn.
func turnCtx(turnID string) context.Context {
	return voiceevent.WithTurnID(context.Background(), turnID)
}

// TestPlaybackPump_IntersentenceGapSameTurn is the tracer bullet for #606: the
// audible silence between sentence N's playback end and sentence N+1's first frame
// on the wire is recorded once, for consecutive sentences of the SAME turn.
func TestPlaybackPump_IntersentenceGapSameTurn(t *testing.T) {
	const delay = 30 * time.Millisecond
	rec := &fakePumpRecorder{}
	p := newGapPlayer(delay)
	pump := newPump(p, fakeCodec{}, nil, nil, WithPumpRecorder(rec))
	defer pump.Close()

	ctx := turnCtx("T1")

	_, ro1 := openChunks()
	pump.HandleSentence(ctx, ro1)
	p.waitPlay(t).finish(nil)

	_, ro2 := openChunks()
	pump.HandleSentence(ctx, ro2)
	p.waitPlay(t).finish(nil)

	gaps := rec.snapshotGaps()
	if len(gaps) != 1 {
		t.Fatalf("gap samples = %d (%v), want exactly 1 (only the N→N+1 span)", len(gaps), gaps)
	}
	if gaps[0] < delay {
		t.Fatalf("gap = %v, want at least the injected silence %v", gaps[0], delay)
	}
}

// TestPlaybackPump_NoGapAcrossTurns pins the cross-turn rule: silence between two
// TURNS is conversational, not a pipeline stutter, so it is never sampled.
func TestPlaybackPump_NoGapAcrossTurns(t *testing.T) {
	rec := &fakePumpRecorder{}
	p := newGapPlayer(5 * time.Millisecond)
	pump := newPump(p, fakeCodec{}, nil, nil, WithPumpRecorder(rec))
	defer pump.Close()

	for _, turn := range []string{"T1", "T2"} {
		_, ro := openChunks()
		pump.HandleSentence(turnCtx(turn), ro)
		p.waitPlay(t).finish(nil)
	}

	if gaps := rec.snapshotGaps(); len(gaps) != 0 {
		t.Fatalf("gap samples = %v, want none across a turn boundary", gaps)
	}
}

// TestPlaybackPump_NoGapForTurnsFirstSentence pins that a turn's FIRST sentence
// records nothing: that span is response_latency's, not the pump's (ADR-0032).
func TestPlaybackPump_NoGapForTurnsFirstSentence(t *testing.T) {
	rec := &fakePumpRecorder{}
	p := newGapPlayer(5 * time.Millisecond)
	pump := newPump(p, fakeCodec{}, nil, nil, WithPumpRecorder(rec))
	defer pump.Close()

	_, ro := openChunks()
	pump.HandleSentence(turnCtx("T1"), ro)
	p.waitPlay(t).finish(nil)

	if gaps := rec.snapshotGaps(); len(gaps) != 0 {
		t.Fatalf("gap samples = %v, want none for a turn's first sentence", gaps)
	}
}

// TestPlaybackPump_BargeClearsGapSpan pins the barge rule: when sentence N is torn
// down (ErrInterrupted, ADR-0027), the silence that follows is the turn ending, not
// an inter-sentence gap — sampling it would fabricate a giant outlier.
func TestPlaybackPump_BargeClearsGapSpan(t *testing.T) {
	rec := &fakePumpRecorder{}
	p := newGapPlayer(5 * time.Millisecond)
	pump := newPump(p, fakeCodec{}, nil, nil, WithPumpRecorder(rec))
	defer pump.Close()

	ctx := turnCtx("T1")

	_, ro1 := openChunks()
	pump.HandleSentence(ctx, ro1)
	p.waitPlay(t).finish(gxvoice.ErrInterrupted)

	_, ro2 := openChunks()
	pump.HandleSentence(ctx, ro2)
	p.waitPlay(t).finish(nil)

	if gaps := rec.snapshotGaps(); len(gaps) != 0 {
		t.Fatalf("gap samples = %v, want none after an interrupted sentence", gaps)
	}
}

// TestPlaybackPump_GapPerSentenceBoundary pins that every consecutive pair of one
// turn is sampled: three sentences yield two gaps, not one and not three.
func TestPlaybackPump_GapPerSentenceBoundary(t *testing.T) {
	rec := &fakePumpRecorder{}
	p := newGapPlayer(5 * time.Millisecond)
	pump := newPump(p, fakeCodec{}, nil, nil, WithPumpRecorder(rec))
	defer pump.Close()

	ctx := turnCtx("T1")
	for i := 0; i < 3; i++ {
		_, ro := openChunks()
		pump.HandleSentence(ctx, ro)
		p.waitPlay(t).finish(nil)
	}

	if gaps := rec.snapshotGaps(); len(gaps) != 2 {
		t.Fatalf("gap samples = %v, want 2 (one per sentence boundary of a 3-sentence turn)", gaps)
	}
}

// assertEvents compares the recorded look-ahead lane events with what the lane
// transition should have counted.
func assertEvents(t *testing.T, rec *fakePumpRecorder, want ...observe.LookaheadEvent) {
	t.Helper()
	got := rec.snapshotEvents()
	if len(got) != len(want) {
		t.Fatalf("lookahead events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("lookahead events = %v, want %v", got, want)
		}
	}
}

// TestPlaybackPump_LookaheadReleasedCounted pins the #375 lane's gap-hiding as its
// own series: moving a held Reaction sentence into the play queue counts a release.
func TestPlaybackPump_LookaheadReleasedCounted(t *testing.T) {
	rec := &fakePumpRecorder{}
	p := newFakePlayer()
	pump := newPump(p, drainingCodec{}, nil, nil, WithPumpRecorder(rec))
	defer pump.Close()

	c, ro := openChunks()
	pump.HandleSentence(lookaheadCtx("A"), ro)
	pump.ReleaseLookahead("A")
	p.waitPlay(t).finish(nil)
	close(c)

	assertEvents(t, rec, observe.LookaheadReleased)
}

// TestPlaybackPump_LookaheadLatchedCounted pins the release-before-prime race as a
// distinct event: the release found nothing held and latched instead.
func TestPlaybackPump_LookaheadLatchedCounted(t *testing.T) {
	rec := &fakePumpRecorder{}
	p := newFakePlayer()
	pump := newPump(p, drainingCodec{}, nil, nil, WithPumpRecorder(rec))
	defer pump.Close()

	pump.ReleaseLookahead("A")
	assertNoPlay(t, p, "a latched release plays nothing until the prime arrives")

	assertEvents(t, rec, observe.LookaheadLatched)
}

// TestPlaybackPump_LookaheadDiscardedCounted pins the barge/yield path: draining a
// held-but-unplayed sentence counts a discard — pre-rendered audio nobody heard.
func TestPlaybackPump_LookaheadDiscardedCounted(t *testing.T) {
	rec := &fakePumpRecorder{}
	p := newFakePlayer()
	pump := newPump(p, drainingCodec{}, nil, nil, WithPumpRecorder(rec))
	defer pump.Close()

	c, ro := openChunks()
	pump.HandleSentence(lookaheadCtx("A"), ro)
	pump.DiscardLookahead("A")
	sent := make(chan struct{})
	go func() { c <- tts.AudioChunk{}; close(c); close(sent) }()
	join(t, sent, "discarded look-ahead drained")

	assertEvents(t, rec, observe.LookaheadDiscarded)
}

// TestPlaybackPump_LookaheadNoopDiscardCountsNothing pins that the coordinator's
// unconditional deferred discard (a stale keyed no-op) never inflates the series —
// only a real held job dropped counts. Clearing a pending latch counts nothing too.
func TestPlaybackPump_LookaheadNoopDiscardCountsNothing(t *testing.T) {
	rec := &fakePumpRecorder{}
	pump := newPump(newFakePlayer(), drainingCodec{}, nil, nil, WithPumpRecorder(rec))
	defer pump.Close()

	pump.DiscardLookahead("nothing-held")

	assertEvents(t, rec)
}
