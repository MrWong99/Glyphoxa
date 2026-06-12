package wire

import (
	"context"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// openChunks returns a chunk channel the test controls; closing it ends the
// sentence's synthesis (the tee does this on completion or barge-in).
func openChunks() (chan tts.AudioChunk, <-chan tts.AudioChunk) {
	ch := make(chan tts.AudioChunk)
	return ch, ch
}

// TestPlaybackPump_SerializesSentences is the load-bearing test: a second
// sentence handed to the pump while the first is still playing must NOT start
// until the first finishes — otherwise Session.Play would auto-interrupt and cut
// off sentence one. Order must also hold.
func TestPlaybackPump_SerializesSentences(t *testing.T) {
	p := newFakePlayer()
	pump := newPump(p, fakeCodec{}, nil, nil)
	defer pump.Close()

	c1, ro1 := openChunks()
	c2, _ := openChunks()

	// Sentence 1: enqueued and picked up by the worker → one Play, still running.
	pump.HandleSentence(context.Background(), ro1)
	pb1 := p.waitPlay(t)

	// Sentence 2: HandleSentence must return promptly (worker is busy on s1)...
	done := make(chan struct{})
	go func() { pump.HandleSentence(context.Background(), c2); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("HandleSentence blocked while the worker was busy")
	}

	// ...but s2 must NOT start playing while s1 is still going.
	select {
	case pb := <-p.started:
		t.Fatalf("sentence 2 started (play %v) before sentence 1 finished", pb)
	case <-time.After(50 * time.Millisecond):
	}

	// Finish sentence 1; the worker must then play sentence 2.
	close(c1) // (synthesis-side close; not strictly needed, the fake ignores chunks)
	pb1.finish(nil)
	pb2 := p.waitPlay(t)
	if pb2 == pb1 {
		t.Fatal("expected a distinct playback for sentence 2")
	}
	if got := p.plays.Load(); got != 2 {
		t.Fatalf("plays = %d, want 2 (one per sentence, sequential)", got)
	}
	pb2.finish(nil)
}

// TestPlaybackPump_HandleSentenceReturnsPromptly pins the PlaybackSink contract:
// the call must not block the synthesis goroutine for the duration of playback.
// The cap-1 queue absorbs the next sentence while the worker plays the current
// one, so the enqueue returns at once (the orchestrator never has more than one
// sentence in flight beyond the playing one — see the cap-1 rationale).
func TestPlaybackPump_HandleSentenceReturnsPromptly(t *testing.T) {
	p := newFakePlayer()
	pump := newPump(p, fakeCodec{}, nil, nil)
	defer pump.Close()

	_, ro1 := openChunks()
	pump.HandleSentence(context.Background(), ro1) // worker picks this up and blocks in Play
	pb1 := p.waitPlay(t)

	// With the worker busy on s1, enqueuing s2 must still return promptly.
	_, ro2 := openChunks()
	done := make(chan struct{})
	go func() { pump.HandleSentence(context.Background(), ro2); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("HandleSentence blocked the synthesis goroutine while the worker was busy")
	}

	// Finish s1, then s2, so the worker drains both and the deferred Close can
	// exit cleanly (Close blocks until the worker returns — leaving s2's playback
	// open would hang teardown).
	pb1.finish(nil)
	p.waitPlay(t).finish(nil)
}

// TestPlaybackPump_CloseStopsWorker verifies Close is idempotent, blocks until
// the worker exits, and that a HandleSentence after Close drains its channel
// rather than blocking the (lockstep) producer.
func TestPlaybackPump_CloseStopsWorker(t *testing.T) {
	p := newFakePlayer()
	pump := newPump(p, fakeCodec{}, nil, nil)

	pump.Close()
	pump.Close() // idempotent, must not panic or block

	// After Close, HandleSentence must drain the channel so a lockstep tee does
	// not block. Send a chunk then close; the pump's drain consumes it.
	ch, ro := openChunks()
	pump.HandleSentence(context.Background(), ro)
	sent := make(chan struct{})
	go func() {
		ch <- tts.AudioChunk{} // would block forever if nothing drains
		close(ch)
		close(sent)
	}()
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("post-Close HandleSentence did not drain the channel")
	}
}

// TestPlaybackPump_CloseDrainsQueuedJob pins the teardown contract for a
// sentence that was enqueued but never dequeued when Close fired: it must be
// drained (unblocking the tee's lockstep forwarder), not abandoned, and not
// spoken into the teardown.
func TestPlaybackPump_CloseDrainsQueuedJob(t *testing.T) {
	p := newFakePlayer()
	pump := newPump(p, fakeCodec{}, nil, nil)

	// Worker busy on s1.
	_, ro1 := openChunks()
	pump.HandleSentence(context.Background(), ro1)
	pb1 := p.waitPlay(t)

	// s2 sits in the queue behind it, with its producer mid-send (the tee's
	// lockstep forward goroutine).
	c2, ro2 := openChunks()
	pump.HandleSentence(context.Background(), ro2)
	sent := make(chan struct{})
	go func() { c2 <- tts.AudioChunk{}; close(sent) }()

	// Close while s2 is still queued; stop is signalled before Close blocks on
	// the worker, then finishing s1 lets the worker observe it.
	closed := make(chan struct{})
	go func() { pump.Close(); close(closed) }()
	select {
	case <-pump.stop:
	case <-time.After(time.Second):
		t.Fatal("Close did not signal stop")
	}
	pb1.finish(nil)

	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("queued sentence not drained after Close; the tee forwarder would block forever")
	}
	close(c2)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not return")
	}
	if got := p.plays.Load(); got != 1 {
		t.Fatalf("plays = %d, want 1 (a queued-but-unstarted sentence must be dropped at Close, not spoken)", got)
	}
}
