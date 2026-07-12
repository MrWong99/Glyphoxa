package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// chunkOf builds a distinguishable 48k mono chunk whose first PCM byte tags it.
func chunkOf(tag byte) tts.AudioChunk {
	return tts.AudioChunk{PCM: []byte{tag, 0}, SampleRate: 48000, Channels: 1}
}

// forwardSink is a ClipSink that drains the chunk channel on its own goroutine
// (mirroring the pump's HandleSentence) and forwards each chunk to got, closing
// got at EOF. It also records the ctx it ran under and whether it was ever called.
type forwardSink struct {
	got    chan tts.AudioChunk
	mu     sync.Mutex
	called bool
	ctx    context.Context
}

func newForwardSink(buf int) *forwardSink { return &forwardSink{got: make(chan tts.AudioChunk, buf)} }

func (s *forwardSink) sink(ctx context.Context, chunks <-chan tts.AudioChunk) {
	s.mu.Lock()
	s.called = true
	s.ctx = ctx
	s.mu.Unlock()
	go func() {
		defer close(s.got)
		for c := range chunks {
			s.got <- c
		}
	}()
}

func (s *forwardSink) wasCalled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.called
}

func (s *forwardSink) sinkCtx() context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ctx
}

// TestClipReplay_StreamsChunksInOrder pins the happy path with no floor (voice
// standalone): the loaded clip's chunks reach the sink in order and the channel is
// closed at EOF.
func TestClipReplay_StreamsChunksInOrder(t *testing.T) {
	h := voicetest.New(t)
	want := []tts.AudioChunk{chunkOf(1), chunkOf(2), chunkOf(3)}
	load := func(context.Context, string) ([]tts.AudioChunk, error) { return want, nil }
	sink := newForwardSink(len(want))

	cr := orchestrator.NewClipReplay(load, sink.sink, nil)
	t.Cleanup(cr.Bind(t.Context(), h.Bus))

	go h.Bus.Publish(voiceevent.ReplayRequested{At: time.Now(), TurnID: "Tr", ClipKey: "clip/1"})

	var got []tts.AudioChunk
	timeout := time.After(2 * time.Second)
	for {
		select {
		case c, ok := <-sink.got:
			if !ok { // channel closed at EOF
				if len(got) != len(want) {
					t.Fatalf("got %d chunks, want %d", len(got), len(want))
				}
				for i := range want {
					if got[i].PCM[0] != want[i].PCM[0] {
						t.Fatalf("chunk %d tag = %d, want %d (out of order)", i, got[i].PCM[0], want[i].PCM[0])
					}
				}
				// The event's TurnID MUST reach the sink ctx: the pump keys FirstOpus on
				// it (→ Floor.MarkSpeaking → barge-able). Without the threading a replay
				// is uninterruptible, so pin it directly (a plain-ctx regression fails here).
				if got := voiceevent.TurnIDFrom(sink.sinkCtx()); got != "Tr" {
					t.Fatalf("sink ctx TurnID = %q, want the event's %q", got, "Tr")
				}
				return
			}
			got = append(got, c)
		case <-timeout:
			t.Fatalf("timed out; got %d chunks", len(got))
		}
	}
}

// TestClipReplay_BargeStopsClipAndReleasesFloor proves a human barge — a real
// Floor.Yield, the barge reactor's exact mechanism — cancels the replay mid-clip
// (the remaining chunks are never written) and the floor is released.
func TestClipReplay_BargeStopsClipAndReleasesFloor(t *testing.T) {
	h := voicetest.New(t)
	// Ten chunks, an unbuffered sink so the producer blocks between chunks — the
	// barge lands mid-clip, not after it drained.
	many := make([]tts.AudioChunk, 10)
	for i := range many {
		many[i] = chunkOf(byte(i + 1))
	}
	load := func(context.Context, string) ([]tts.AudioChunk, error) { return many, nil }
	sink := newForwardSink(0)

	floor := orchestrator.NewFloor()
	cr := orchestrator.NewClipReplay(load, sink.sink, nil)
	cr.SetFloor(floor)
	t.Cleanup(cr.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.ReplayRequested{At: time.Now(), TurnID: "Tr", ClipKey: "clip/1"})

	// The replay runs on the floor goroutine: read the first chunk to prove it is
	// playing, then Yield the floor (the barge).
	<-sink.got
	if !floor.Active() {
		t.Fatal("floor not held during replay — cr.floor not wired")
	}
	if _, yielded := floor.Yield(); !yielded {
		t.Fatal("Yield reported no held turn — the replay did not take the floor")
	}

	// Drain what is left: the channel must close well before all 10 chunks (the
	// barge stopped the writes).
	delivered := 1
	for range sink.got {
		delivered++
	}
	if delivered >= len(many) {
		t.Fatalf("delivered %d/%d chunks — the barge did not stop the clip", delivered, len(many))
	}
	// The turn goroutine's deferred release ran once dispatch returned.
	waitFor(t, func() bool { return !floor.Active() })
}

// TestClipReplay_LoaderErrorReleasesFloorNoSink proves a clip-load failure ends the
// turn cleanly: the floor is released (defer) and the sink is never called.
func TestClipReplay_LoaderErrorReleasesFloorNoSink(t *testing.T) {
	h := voicetest.New(t)
	loadErr := errors.New("blob gone")
	load := func(context.Context, string) ([]tts.AudioChunk, error) { return nil, loadErr }
	sink := newForwardSink(1)

	var gotErr error
	var mu sync.Mutex
	onError := func(err error) { mu.Lock(); gotErr = err; mu.Unlock() }

	floor := orchestrator.NewFloor()
	cr := orchestrator.NewClipReplay(load, sink.sink, onError)
	cr.SetFloor(floor)
	t.Cleanup(cr.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.ReplayRequested{At: time.Now(), TurnID: "Tr", ClipKey: "gone"})

	waitFor(t, func() bool { return !floor.Active() }) // released via defer
	if sink.wasCalled() {
		t.Fatal("sink was called despite the load error")
	}
	mu.Lock()
	defer mu.Unlock()
	if !errors.Is(gotErr, loadErr) {
		t.Fatalf("onError got %v, want %v", gotErr, loadErr)
	}
}

// TestClipReplay_RealBargeCutsReplay drives the ACTUAL barge gate (ADR-0027): a
// real BargeIn reactor shares the replay's floor, the sink publishes FirstOpus for
// the replay turn (as the live pump does — arming Floor.MarkSpeaking), and a human
// VADSpeechStart then cuts the clip mid-playback. This proves the end-to-end path a
// human uses to interrupt a replay — not just a direct floor.Yield().
func TestClipReplay_RealBargeCutsReplay(t *testing.T) {
	h := voicetest.New(t)
	many := make([]tts.AudioChunk, 10)
	for i := range many {
		many[i] = chunkOf(byte(i + 1))
	}
	load := func(context.Context, string) ([]tts.AudioChunk, error) { return many, nil }

	// The sink mimics the live pump: it publishes FirstOpus keyed on the ctx TurnID
	// (the audible-on-wire signal that arms the barge), then forwards chunks.
	got := make(chan tts.AudioChunk)
	var gotCtx context.Context
	var ctxMu sync.Mutex
	sink := func(ctx context.Context, chunks <-chan tts.AudioChunk) {
		ctxMu.Lock()
		gotCtx = ctx
		ctxMu.Unlock()
		h.Bus.Publish(voiceevent.FirstOpus{At: time.Now(), TurnID: voiceevent.TurnIDFrom(ctx)})
		go func() {
			defer close(got)
			for c := range chunks {
				got <- c
			}
		}()
	}

	floor := orchestrator.NewFloor()
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))
	cr := orchestrator.NewClipReplay(load, sink, nil)
	cr.SetFloor(floor)
	t.Cleanup(cr.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.ReplayRequested{At: time.Now(), TurnID: "Tr", ClipKey: "clip/1"})

	<-got // playing, and FirstOpus already armed the floor
	if !floor.Speaking() {
		t.Fatal("floor not marked speaking — FirstOpus (keyed on the threaded TurnID) did not arm the barge")
	}
	// A human speaks: the real barge reactor fires, yields the floor, cancels the clip.
	h.Bus.Publish(voiceevent.VADSpeechStart{})

	delivered := 1
	for range got {
		delivered++
	}
	if delivered >= len(many) {
		t.Fatalf("delivered %d/%d — the human barge did not cut the replay", delivered, len(many))
	}
	voicetest.AssertEventCount[voiceevent.BargeDetected](t, h, 1)
	ctxMu.Lock()
	defer ctxMu.Unlock()
	if id := voiceevent.TurnIDFrom(gotCtx); id != "Tr" {
		t.Fatalf("sink ctx TurnID = %q, want Tr", id)
	}
}
