package wire

import (
	"context"
	"sync"
	"testing"
	"time"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// lookaheadCtx marks a sentence context as a held look-ahead keyed by turnID (the
// wire tee installs both markers on the real path).
func lookaheadCtx(turnID string) context.Context {
	return voiceevent.WithPlaybackLookahead(voiceevent.WithTurnID(context.Background(), turnID))
}

// cancelledLookaheadCtx is a look-ahead ctx already cancelled (a barge that landed
// before the sentence was primed).
func cancelledLookaheadCtx(turnID string) context.Context {
	ctx, cancel := context.WithCancel(lookaheadCtx(turnID))
	cancel()
	return ctx
}

// TestPlaybackPump_PrimeCancelledCtxDrains pins F6 (#375): priming a look-ahead
// sentence whose ctx is already cancelled drains it at once (no wedge), leaves any
// other turn's held job untouched, and a subsequent live prime+release still plays.
func TestPlaybackPump_PrimeCancelledCtxDrains(t *testing.T) {
	p := newFakePlayer()
	pump := newPump(p, drainingCodec{}, nil, nil)
	defer pump.Close()

	// A live look-ahead for turn A sits held.
	cA, roA := openChunks()
	pump.HandleSentence(lookaheadCtx("A"), roA)
	probeA := blockProbe(t, cA, "held live turn A")

	// A cancelled look-ahead for turn B is drained on arrival, NOT held — A stays.
	cB, roB := openChunks()
	pump.HandleSentence(cancelledLookaheadCtx("B"), roB)
	sentB := make(chan struct{})
	go func() { cB <- tts.AudioChunk{}; close(cB); close(sentB) }()
	join(t, sentB, "cancelled-ctx prime drained on arrival")
	assertNoPlay(t, p, "a cancelled-ctx prime must not play")

	// A is still held: release it and it plays (its chunks then drain).
	pump.ReleaseLookahead("A")
	pb := p.waitPlay(t)
	pb.finish(nil)
	join(t, probeA, "turn A after its live release+play")
}

// drainingCodec models the real transcoder: its PlaybackSource eagerly drains the
// sentence's chunk channel (the real codec consumes chunks to make Opus frames), so
// a PLAYED sentence's channel is drained — unlike fakeCodec, which never pulls. The
// tests use it so a released look-ahead actually consumes its held producer.
type drainingCodec struct{}

func (drainingCodec) DecodeInbound(gxvoice.Frame) ([]audio.Frame, error) { return nil, nil }
func (drainingCodec) PlaybackSource(chunks <-chan tts.AudioChunk) (gxvoice.Source, error) {
	go drain(chunks)
	return fakeSource{}, nil
}

// assertNoPlay fails if a play starts within the window (bounded negative select,
// the house pattern for "must not happen").
func assertNoPlay(t *testing.T, p *fakePlayer, why string) {
	t.Helper()
	select {
	case pb := <-p.started:
		t.Fatalf("%s: a play started (%v) but none was expected", why, pb)
	case <-time.After(50 * time.Millisecond):
	}
}

// blockProbe starts a producer that sends one chunk on ch, then CLOSES ch, then
// closes the returned done channel. It asserts the send does NOT complete within the
// window (the pump is HOLDING ch, not draining it). The caller must later cause ch to
// drain (release+play / discard / Close) and then join done — the producer's close of
// ch lets any drain terminate, so no goroutine leaks (goleak is on in this package).
func blockProbe(t *testing.T, ch chan tts.AudioChunk, why string) <-chan struct{} {
	t.Helper()
	sent := make(chan struct{})
	done := make(chan struct{})
	go func() {
		ch <- tts.AudioChunk{}
		close(sent)
		close(ch)
		close(done)
	}()
	select {
	case <-sent:
		t.Fatalf("%s: a held sentence's chunks were drained (producer unblocked)", why)
	case <-time.After(50 * time.Millisecond):
	}
	return done
}

// join waits for a probe/drain done channel, failing on timeout.
func join(t *testing.T, done <-chan struct{}, why string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s: goroutine did not finish", why)
	}
}

// TestPlaybackPump_LookaheadHeldUntilRelease is the #375 headline (AC1): a sentence
// handed to the pump under a look-ahead context is NOT played and its chunks are NOT
// drained while it sits in the lane — until ReleaseLookahead(id). It then plays only
// AFTER the in-flight sentence finishes, preserving order.
func TestPlaybackPump_LookaheadHeldUntilRelease(t *testing.T) {
	p := newFakePlayer()
	pump := newPump(p, drainingCodec{}, nil, nil)
	defer pump.Close()

	// Sentence 1 (the Lead's last): worker picks it up and blocks in Play.
	c1, ro1 := openChunks()
	pump.HandleSentence(context.Background(), ro1)
	pb1 := p.waitPlay(t)

	// Reaction s1 handed to the pump as a look-ahead: HandleSentence returns promptly
	// and the sentence must NOT play nor have its chunks drained.
	cr, ror := openChunks()
	handled := make(chan struct{})
	go func() { pump.HandleSentence(lookaheadCtx("R"), ror); close(handled) }()
	join(t, handled, "HandleSentence on a look-ahead sentence")
	assertNoPlay(t, p, "held look-ahead before release")
	probe := blockProbe(t, cr, "held look-ahead before release")

	// Release: the held sentence moves to the queue but must still wait for the
	// in-flight sentence 1 — order preserved, no auto-interrupt.
	pump.ReleaseLookahead("R")
	assertNoPlay(t, p, "released look-ahead while sentence 1 still in flight")

	// Finish sentence 1; the released reaction then plays and its codec drains cr.
	close(c1)
	pb1.finish(nil)
	pb2 := p.waitPlay(t)
	if pb2 == pb1 {
		t.Fatal("expected a distinct playback for the released reaction")
	}
	if got := p.plays.Load(); got != 2 {
		t.Fatalf("plays = %d, want 2 (sentence 1 then the released reaction)", got)
	}
	pb2.finish(nil)
	join(t, probe, "held reaction producer after release+play")
}

// TestPlaybackPump_ReleaseBeforePrimeLatch pins the release-before-prime race
// (#375): ReleaseLookahead(id) BEFORE the sentence is primed latches, so the
// matching prime bypasses the lane straight to the queue and plays; a prime for a
// DIFFERENT id is still held (the latch is turn-keyed, not a bare flag).
func TestPlaybackPump_ReleaseBeforePrimeLatch(t *testing.T) {
	t.Run("matching id bypasses to the queue", func(t *testing.T) {
		p := newFakePlayer()
		pump := newPump(p, drainingCodec{}, nil, nil)
		defer pump.Close()

		pump.ReleaseLookahead("R") // latch: prime has not happened yet
		c, ro := openChunks()
		pump.HandleSentence(lookaheadCtx("R"), ro)

		// The latched sentence went to the queue and the worker plays it at once.
		pb := p.waitPlay(t)
		close(c) // let the play's codec drain terminate
		pb.finish(nil)
	})

	t.Run("different id stays held", func(t *testing.T) {
		p := newFakePlayer()
		pump := newPump(p, drainingCodec{}, nil, nil)
		defer pump.Close()

		pump.ReleaseLookahead("R") // latch for R
		cx, rox := openChunks()
		pump.HandleSentence(lookaheadCtx("X"), rox) // primes X, not R
		assertNoPlay(t, p, "a prime for an id other than the latched one must be held")
		pump.DiscardLookahead("X") // drain the held X
		close(cx)                  // terminate the discard's drain
	})
}

// TestPlaybackPump_DiscardDrainsAndClearsLatch pins the barge/yield path (#375,
// ADR-0027): DiscardLookahead(id) drains the held sentence (unblocking its producer)
// with zero plays; a wrong-id discard is a no-op; and it clears a pending release
// latch so a later prime is held, not bypassed.
func TestPlaybackPump_DiscardDrainsAndClearsLatch(t *testing.T) {
	t.Run("discard drains the held job", func(t *testing.T) {
		p := newFakePlayer()
		pump := newPump(p, drainingCodec{}, nil, nil)
		defer pump.Close()

		cr, ror := openChunks()
		pump.HandleSentence(lookaheadCtx("R"), ror)
		probe := blockProbe(t, cr, "primed but not discarded")

		pump.DiscardLookahead("R")
		join(t, probe, "after DiscardLookahead the held sentence must drain")
		assertNoPlay(t, p, "a discarded look-ahead must never play")
	})

	t.Run("wrong-id discard is a no-op", func(t *testing.T) {
		p := newFakePlayer()
		pump := newPump(p, drainingCodec{}, nil, nil)
		defer pump.Close()

		cr, ror := openChunks()
		pump.HandleSentence(lookaheadCtx("R"), ror)
		probe := blockProbe(t, cr, "primed R")
		pump.DiscardLookahead("W") // not R: must leave R held
		assertNoPlay(t, p, "no play after a wrong-id discard")
		pump.DiscardLookahead("R") // now drain R for a clean teardown
		join(t, probe, "R after the correct discard")
	})

	t.Run("discard clears a pending release latch", func(t *testing.T) {
		p := newFakePlayer()
		pump := newPump(p, drainingCodec{}, nil, nil)
		defer pump.Close()

		pump.ReleaseLookahead("L") // latch
		pump.DiscardLookahead("L") // clears the latch
		cl, rol := openChunks()
		pump.HandleSentence(lookaheadCtx("L"), rol) // must be HELD, not bypassed
		assertNoPlay(t, p, "a cleared latch must not bypass a later prime to the queue")
		pump.DiscardLookahead("L") // drain the held L
		close(cl)
	})
}

// TestPlaybackPump_CloseDrainsHeldLane pins teardown (#375): Close with a primed but
// never-released lane drains it (no wedge), and priming over a stale older-turn job
// drains the old one.
func TestPlaybackPump_CloseDrainsHeldLane(t *testing.T) {
	t.Run("Close drains a held lane", func(t *testing.T) {
		p := newFakePlayer()
		pump := newPump(p, drainingCodec{}, nil, nil)

		cr, ror := openChunks()
		pump.HandleSentence(lookaheadCtx("R"), ror)
		// A producer is parked mid-send on the held sentence; it closes cr after.
		sent := make(chan struct{})
		go func() { cr <- tts.AudioChunk{}; close(cr); close(sent) }()

		pump.Close() // must drain the lane so the parked producer is released
		join(t, sent, "Close draining the held look-ahead lane")
	})

	t.Run("prime over a stale older-turn job drains the old one", func(t *testing.T) {
		p := newFakePlayer()
		pump := newPump(p, drainingCodec{}, nil, nil)
		defer pump.Close()

		cOld, roOld := openChunks()
		pump.HandleSentence(lookaheadCtx("old"), roOld)
		probe := blockProbe(t, cOld, "the stale old job")
		cNew, roNew := openChunks()
		pump.HandleSentence(lookaheadCtx("new"), roNew) // supersedes old → drains old
		join(t, probe, "the stale old job drained by the supersede")
		assertNoPlay(t, p, "neither the stale nor the new held job plays")

		pump.DiscardLookahead("new") // drain the new held job for teardown
		close(cNew)
	})
}

// nChunkSynth streams n chunks then closes, honouring ctx on each send (a barge
// mid-stream ends it). It is a minimal [tts.Synthesizer] for the tee integration.
type nChunkSynth struct{ n int }

func (s nChunkSynth) Synthesize(ctx context.Context, _ tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	ch := make(chan tts.AudioChunk)
	go func() {
		defer close(ch)
		for i := 0; i < s.n; i++ {
			select {
			case ch <- tts.AudioChunk{PCM: []byte{byte(i)}, SampleRate: 24000, Channels: 1}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func (nChunkSynth) AudioMarkupPrompt(tts.Voice) string { return "" }

// TestPump_LookaheadTeeIntegration_HoldsThenDrains is #375 AC2 end-to-end over the
// real [TeeSynthesizer]: with a look-ahead context, the orchestrator-side channel
// does NOT close while the sentence is held (no delivery mark → nothing commits,
// ADR-0012), then drains and closes once ReleaseLookahead lets the pump play it.
func TestPump_LookaheadTeeIntegration_HoldsThenDrains(t *testing.T) {
	p := newFakePlayer()
	pump := newPump(p, drainingCodec{}, nil, nil)
	defer pump.Close()
	tee := NewTeeSynthesizer(nChunkSynth{n: 3}, pump, nil)

	out, err := tee.Synthesize(lookaheadCtx("R"), tts.SynthesizeRequest{Sentence: "held"})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	// Drain the orchestrator side concurrently. While the sink HOLDS the sentence, the
	// tee is blocked on its play<-chunk send, so out must NOT close — the
	// deliver-then-commit boundary (channel close) is unreachable.
	orchDone := make(chan struct{})
	var got int
	go func() {
		for range out {
			got++
		}
		close(orchDone)
	}()
	select {
	case <-orchDone:
		t.Fatal("orchestrator channel closed while the look-ahead was held (premature delivery mark)")
	case <-time.After(100 * time.Millisecond):
	}

	// Release: the worker plays it, the codec drains the chunks, the tee forwards the
	// rest and closes both channels — the delivery mark is now legitimate.
	pump.ReleaseLookahead("R")
	p.waitPlay(t).finish(nil)
	join(t, orchDone, "orchestrator channel closing after release + play")
	if got != 3 {
		t.Fatalf("orchestrator got %d chunks, want 3", got)
	}
}

// faCounter subscribes a FirstAudio counter to bus.
func faCounter(bus *voiceevent.Bus) func() int {
	var mu sync.Mutex
	var n int
	voiceevent.On(bus, func(voiceevent.FirstAudio) {
		mu.Lock()
		n++
		mu.Unlock()
	})
	return func() int { mu.Lock(); defer mu.Unlock(); return n }
}

// TestPlaybackPump_HeldLookaheadNeverFirstAudio pins the structural guarantee (#375):
// a held look-ahead sentence that is DRAINED rather than played — by Close, by
// DiscardLookahead, or by a same-turn supersede — never publishes FirstAudio, because
// the drain paths never build or pull a playback source. The ctx stays LIVE throughout
// (the guarantee is structural, not a cancelled-ctx side effect), and each assertion
// follows a positive drain-completion signal (no cancel-vs-drain race).
func TestPlaybackPump_HeldLookaheadNeverFirstAudio(t *testing.T) {
	t.Run("Close drains the held job", func(t *testing.T) {
		bus := voiceevent.NewBus()
		fa := faCounter(bus)
		p := newFakePlayer()
		pump := newPump(p, drainingCodec{}, nil, bus)

		cr, ror := openChunks()
		pump.HandleSentence(lookaheadCtx("R"), ror) // live ctx, held
		probe := blockProbe(t, cr, "held live-ctx look-ahead")
		pump.Close()
		join(t, probe, "Close draining the held job")
		if fa() != 0 {
			t.Fatalf("FirstAudio = %d for a Close-drained held sentence, want 0", fa())
		}
	})

	t.Run("Discard drains the held job", func(t *testing.T) {
		bus := voiceevent.NewBus()
		fa := faCounter(bus)
		p := newFakePlayer()
		pump := newPump(p, drainingCodec{}, nil, bus)
		defer pump.Close()

		cr, ror := openChunks()
		pump.HandleSentence(lookaheadCtx("R"), ror)
		probe := blockProbe(t, cr, "held live-ctx look-ahead")
		pump.DiscardLookahead("R")
		join(t, probe, "Discard draining the held job")
		if fa() != 0 {
			t.Fatalf("FirstAudio = %d for a discarded held sentence, want 0", fa())
		}
	})

	t.Run("supersede drains the stale job", func(t *testing.T) {
		bus := voiceevent.NewBus()
		fa := faCounter(bus)
		p := newFakePlayer()
		pump := newPump(p, drainingCodec{}, nil, bus)
		defer pump.Close()

		cOld, roOld := openChunks()
		pump.HandleSentence(lookaheadCtx("old"), roOld)
		probe := blockProbe(t, cOld, "stale held look-ahead")
		cNew, roNew := openChunks()
		pump.HandleSentence(lookaheadCtx("new"), roNew) // supersedes old → drains it
		join(t, probe, "supersede draining the stale job")
		if fa() != 0 {
			t.Fatalf("FirstAudio = %d for a superseded held sentence, want 0", fa())
		}
		pump.DiscardLookahead("new")
		close(cNew)
	})
}
