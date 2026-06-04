package wire_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire"
)

// Compile-time assertion: SequentialSink is a PlaybackSink (what NewTeeSynthesizer
// consumes).
var _ wire.PlaybackSink = (*wire.SequentialSink)(nil)

// closedChunkChan returns an already-closed chunk channel — a "sentence" with no
// audio, enough to drive the sink's queue/worker without a real codec.
func closedChunkChan() <-chan tts.AudioChunk {
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch
}

// TestSequentialSink_PlaysOneAtATime is the load-bearing test for #14: the sink
// MUST NOT let two sentences' playback overlap, because gxvoice.Session.Play
// auto-interrupts the current playback — overlap would clip every sentence but
// the last. It drives three sentences through a player that blocks until
// released and asserts at most one is ever in flight, and that they run in order.
// This test FAILS under the naive "go PlaySentence(...)" sink the design warns
// against.
func TestSequentialSink_PlaysOneAtATime(t *testing.T) {
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	var order []int
	var orderMu sync.Mutex

	// A SentencePlayer that records concurrency and blocks until the test
	// releases each sentence, so overlap (if the sink allowed it) would be
	// observable as inFlight > 1.
	release := make(chan struct{})
	started := make(chan int, 8)
	var seq atomic.Int32
	play := func(ctx context.Context, _ <-chan tts.AudioChunk) error {
		n := int(seq.Add(1))
		cur := inFlight.Add(1)
		for {
			m := maxInFlight.Load()
			if cur <= m || maxInFlight.CompareAndSwap(m, cur) {
				break
			}
		}
		orderMu.Lock()
		order = append(order, n)
		orderMu.Unlock()
		started <- n
		<-release // hold the "playback" open until the test lets it finish
		inFlight.Add(-1)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := wire.NewSequentialSink(ctx, play, nil)

	// Enqueue from a goroutine: HandleSentence applies back-pressure (the queue
	// buffers only one ahead of the in-flight sentence), so enqueuing all three
	// inline would block the test before it can release them. The serialization
	// the worker enforces is what we assert below.
	const n = 3
	go func() {
		for i := 0; i < n; i++ {
			sink.HandleSentence(ctx, closedChunkChan())
		}
	}()

	// Let each sentence finish one at a time; between releases only one may be
	// in flight.
	for i := 0; i < n; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("sentence %d never started (sink stalled)", i+1)
		}
		if got := inFlight.Load(); got != 1 {
			t.Fatalf("after sentence %d started, in-flight = %d, want exactly 1", i+1, got)
		}
		release <- struct{}{}
	}

	if got := maxInFlight.Load(); got != 1 {
		t.Errorf("max concurrent playbacks = %d, want 1 (no overlap; Session.Play auto-interrupts)", got)
	}
	orderMu.Lock()
	defer orderMu.Unlock()
	for i, v := range order {
		if v != i+1 {
			t.Errorf("playback order = %v, want sequential 1..%d", order, n)
			break
		}
	}
}

// TestSequentialSink_ReportsNonInterruptError pins that a sentence's playback
// error reaches onErr (so the live loop can log a failed sentence) — one report
// per failing sentence.
func TestSequentialSink_ReportsNonInterruptError(t *testing.T) {
	wantErr := context.DeadlineExceeded // any non-nil error; the sink does not classify
	play := func(context.Context, <-chan tts.AudioChunk) error {
		return wantErr
	}

	// onErr publishes over a channel so the test observes the error without
	// racing the worker goroutine on a shared field.
	gotErr := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := wire.NewSequentialSink(ctx, play, func(err error) { gotErr <- err })

	sink.HandleSentence(ctx, closedChunkChan())
	select {
	case got := <-gotErr:
		if got != wantErr {
			t.Errorf("onErr received %v, want %v", got, wantErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onErr was never called for a failing sentence")
	}
}

// TestSequentialSink_WorkerExitsOnContextCancel pins that the worker goroutine
// unwinds when the conversation context is cancelled (shutdown / barge-in), so a
// cancelled Run leaks no goroutine — the property `go test -race` guards.
func TestSequentialSink_WorkerExitsOnContextCancel(t *testing.T) {
	played := make(chan struct{}, 1)
	play := func(context.Context, <-chan tts.AudioChunk) error {
		played <- struct{}{}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	sink := wire.NewSequentialSink(ctx, play, nil)

	// One sentence flows before cancel.
	sink.HandleSentence(ctx, closedChunkChan())
	select {
	case <-played:
	case <-time.After(time.Second):
		t.Fatal("first sentence never played")
	}

	cancel()
	// After cancel, HandleSentence must not block forever and the worker must
	// stop consuming. Enqueue once more; it should be dropped (ctx done), not
	// played.
	sink.HandleSentence(ctx, closedChunkChan())
	select {
	case <-played:
		t.Error("worker played a sentence after context cancel")
	case <-time.After(100 * time.Millisecond):
		// expected: nothing played
	}
}
