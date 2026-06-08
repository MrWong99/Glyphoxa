package wire_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire"
)

// fakeSynth is a minimal [tts.Synthesizer]: it streams a fixed list of chunks
// (one per Synthesize call), or returns startErr without opening a stream. It
// records the AudioMarkupPrompt voice so the pass-through can be asserted.
type fakeSynth struct {
	chunks   []tts.AudioChunk
	startErr error

	mu         sync.Mutex
	markupSeen tts.Voice
	markupRet  string
}

func (f *fakeSynth) Synthesize(ctx context.Context, _ tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	if f.startErr != nil {
		return nil, f.startErr
	}
	ch := make(chan tts.AudioChunk)
	go func() {
		defer close(ch)
		for _, c := range f.chunks {
			select {
			case ch <- c:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

func (f *fakeSynth) AudioMarkupPrompt(v tts.Voice) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markupSeen = v
	return f.markupRet
}

// drainAll reads every chunk from ch into a slice; used to consume one side of
// the tee concurrently with the other so the lockstep forward does not deadlock.
func drainAll(ch <-chan tts.AudioChunk) []tts.AudioChunk {
	var out []tts.AudioChunk
	for c := range ch {
		out = append(out, c)
	}
	return out
}

func chunk(n byte, rate int) tts.AudioChunk {
	return tts.AudioChunk{PCM: []byte{n, n}, SampleRate: rate, Channels: 1}
}

// TestTee_ForwardsToBothSides pins the core tee behaviour: every chunk reaches
// BOTH the orchestrator-side channel (drained-and-dropped in production) and the
// per-sentence playback sink, in order, and both channels close when the wrapped
// stream ends. This is the contract the outbound path depends on.
func TestTee_ForwardsToBothSides(t *testing.T) {
	want := []tts.AudioChunk{chunk(1, 24000), chunk(2, 24000), chunk(3, 24000)}
	inner := &fakeSynth{chunks: want}

	var sinkChunks []tts.AudioChunk
	var sinkCh <-chan tts.AudioChunk
	var sinkWG sync.WaitGroup
	sinkWG.Add(1)
	sink := wire.PlaybackSinkFunc(func(_ context.Context, chunks <-chan tts.AudioChunk) {
		sinkCh = chunks
		go func() {
			defer sinkWG.Done()
			sinkChunks = drainAll(chunks)
		}()
	})

	tee := wire.NewTeeSynthesizer(inner, sink, nil)
	out, err := tee.Synthesize(context.Background(), tts.SynthesizeRequest{Sentence: "hello"})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if sinkCh == nil {
		t.Fatal("sink was not handed a channel before chunks flowed")
	}

	// Drain the orchestrator side (production: drop). The sink drains concurrently.
	orchChunks := drainAll(out)
	sinkWG.Wait()

	if len(orchChunks) != len(want) {
		t.Fatalf("orchestrator got %d chunks, want %d", len(orchChunks), len(want))
	}
	if len(sinkChunks) != len(want) {
		t.Fatalf("sink got %d chunks, want %d", len(sinkChunks), len(want))
	}
	for i := range want {
		if orchChunks[i].SampleRate != want[i].SampleRate || string(orchChunks[i].PCM) != string(want[i].PCM) {
			t.Errorf("orchestrator chunk %d = %+v, want %+v", i, orchChunks[i], want[i])
		}
		if sinkChunks[i].SampleRate != want[i].SampleRate || string(sinkChunks[i].PCM) != string(want[i].PCM) {
			t.Errorf("sink chunk %d = %+v, want %+v", i, sinkChunks[i], want[i])
		}
	}
}

// TestTee_FreshChannelPerSentence pins the per-Dispatch granularity the codec's
// PlaybackSource consumes: each Synthesize call hands the sink its OWN channel,
// closed at that sentence's end — not one long-lived stream across sentences.
func TestTee_FreshChannelPerSentence(t *testing.T) {
	inner := &fakeSynth{chunks: []tts.AudioChunk{chunk(1, 24000)}}

	var got []<-chan tts.AudioChunk
	var mu sync.Mutex
	var wg sync.WaitGroup
	sink := wire.PlaybackSinkFunc(func(_ context.Context, chunks <-chan tts.AudioChunk) {
		mu.Lock()
		got = append(got, chunks)
		mu.Unlock()
		wg.Add(1)
		go func() { defer wg.Done(); drainAll(chunks) }()
	})

	tee := wire.NewTeeSynthesizer(inner, sink, nil)
	for i := 0; i < 2; i++ {
		out, err := tee.Synthesize(context.Background(), tts.SynthesizeRequest{Sentence: "s"})
		if err != nil {
			t.Fatalf("Synthesize %d: %v", i, err)
		}
		drainAll(out)
	}
	wg.Wait()

	if len(got) != 2 {
		t.Fatalf("sink handed %d channels, want 2 (one per sentence)", len(got))
	}
	if got[0] == got[1] {
		t.Error("the two sentences shared one channel; each Dispatch must get a fresh channel")
	}
}

// TestTee_BargeInClosesPlayback pins barge-in (ADR-0027): cancelling the
// synthesis context ends the sentence — the playback channel closes so the
// pump's Source ends and Session.Play stops, rather than hanging.
func TestTee_BargeInClosesPlayback(t *testing.T) {
	// A synth that blocks after the first chunk until ctx is cancelled, so the
	// tee is mid-stream when barge-in fires.
	blockUntilCancel := make(chan struct{})
	inner := &blockingSynth{first: chunk(1, 24000), release: blockUntilCancel}

	closed := make(chan struct{})
	sink := wire.PlaybackSinkFunc(func(_ context.Context, chunks <-chan tts.AudioChunk) {
		go func() {
			drainAll(chunks) // returns when the channel closes
			close(closed)
		}()
	})

	ctx, cancel := context.WithCancel(context.Background())
	tee := wire.NewTeeSynthesizer(inner, sink, nil)
	out, err := tee.Synthesize(ctx, tts.SynthesizeRequest{Sentence: "interrupt me"})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	// Consume the orchestrator side in the background so the lockstep forward can
	// deliver the first chunk and then block on the inner stream.
	go drainAll(out)

	cancel() // barge-in
	close(blockUntilCancel)

	select {
	case <-closed:
		// playback channel closed — Source would end, Play would stop.
	case <-time.After(2 * time.Second):
		t.Fatal("playback channel did not close after barge-in (ctx cancel)")
	}
}

// blockingSynth emits one chunk then blocks the stream goroutine until either
// release is closed or ctx is cancelled, leaving the tee mid-sentence.
type blockingSynth struct {
	first   tts.AudioChunk
	release chan struct{}
}

func (b *blockingSynth) Synthesize(ctx context.Context, _ tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	ch := make(chan tts.AudioChunk)
	go func() {
		defer close(ch)
		select {
		case ch <- b.first:
		case <-ctx.Done():
			return
		}
		select {
		case <-b.release:
		case <-ctx.Done():
		}
	}()
	return ch, nil
}

func (b *blockingSynth) AudioMarkupPrompt(tts.Voice) string { return "" }

// TestTee_StartErrorOpensNoPlayback pins that a failed Synthesize start returns
// the error untouched and does NOT hand the sink a channel — the sentence never
// speaks, and no goroutine/channel leaks.
func TestTee_StartErrorOpensNoPlayback(t *testing.T) {
	wantErr := errors.New("synth boom")
	inner := &fakeSynth{startErr: wantErr}

	var handed bool
	sink := wire.PlaybackSinkFunc(func(context.Context, <-chan tts.AudioChunk) { handed = true })

	tee := wire.NewTeeSynthesizer(inner, sink, nil)
	out, err := tee.Synthesize(context.Background(), tts.SynthesizeRequest{Sentence: "x"})
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if out != nil {
		t.Error("returned a non-nil channel on start error")
	}
	if handed {
		t.Error("sink was handed a channel despite the synth failing to start")
	}
}

// TestTee_AudioMarkupPromptPassesThrough pins that the decorator only intercepts
// Synthesize — AudioMarkupPrompt (used to build the Persona/system prompt)
// delegates to the wrapped Synthesizer unchanged.
func TestTee_AudioMarkupPromptPassesThrough(t *testing.T) {
	inner := &fakeSynth{markupRet: "use [brackets]"}
	sink := wire.PlaybackSinkFunc(func(context.Context, <-chan tts.AudioChunk) {})
	tee := wire.NewTeeSynthesizer(inner, sink, nil)

	v := tts.Voice{ProviderID: "elevenlabs", VoiceID: "george"}
	if got := tee.AudioMarkupPrompt(v); got != "use [brackets]" {
		t.Errorf("AudioMarkupPrompt = %q, want the inner value", got)
	}
	inner.mu.Lock()
	defer inner.mu.Unlock()
	if inner.markupSeen.VoiceID != "george" {
		t.Errorf("inner saw voice %+v, want the one passed through", inner.markupSeen)
	}
}

// TestTee_PublishesFirstAudioOncePerSentence pins A3 hook 1: a sentence with
// several chunks publishes exactly ONE voiceevent.FirstAudio, carrying the turn
// id installed on the synthesis context, at the moment the first chunk crosses
// to the sink — not one per chunk.
func TestTee_PublishesFirstAudioOncePerSentence(t *testing.T) {
	inner := &fakeSynth{chunks: []tts.AudioChunk{chunk(1, 24000), chunk(2, 24000), chunk(3, 24000)}}
	sink := wire.PlaybackSinkFunc(func(_ context.Context, chunks <-chan tts.AudioChunk) {
		go drainAll(chunks)
	})

	bus := voiceevent.NewBus()
	var mu sync.Mutex
	var got []voiceevent.FirstAudio
	voiceevent.On(bus, func(e voiceevent.FirstAudio) {
		mu.Lock()
		got = append(got, e)
		mu.Unlock()
	})

	tee := wire.NewTeeSynthesizer(inner, sink, bus)
	ctx := voiceevent.WithTurnID(context.Background(), "turn-abc")
	out, err := tee.Synthesize(ctx, tts.SynthesizeRequest{Sentence: "three chunks"})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	drainAll(out) // run the forward goroutine to completion

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("published %d FirstAudio events, want exactly 1 per sentence", len(got))
	}
	if got[0].TurnID != "turn-abc" {
		t.Errorf("FirstAudio.TurnID = %q, want the ctx turn id", got[0].TurnID)
	}
	if got[0].At.IsZero() {
		t.Error("FirstAudio.At is zero, want the first-chunk timestamp")
	}
}

// TestTee_NoFirstAudioWhenNoChunks pins that a sentence whose stream yields no
// chunk — a barge-in cancelled before the first chunk — publishes NO FirstAudio,
// so a turn that never produced audio never reports a response latency.
func TestTee_NoFirstAudioWhenNoChunks(t *testing.T) {
	inner := &fakeSynth{chunks: nil} // empty stream, closes immediately
	sink := wire.PlaybackSinkFunc(func(_ context.Context, chunks <-chan tts.AudioChunk) {
		go drainAll(chunks)
	})

	bus := voiceevent.NewBus()
	var count int
	var mu sync.Mutex
	voiceevent.On(bus, func(voiceevent.FirstAudio) {
		mu.Lock()
		count++
		mu.Unlock()
	})

	tee := wire.NewTeeSynthesizer(inner, sink, bus)
	out, err := tee.Synthesize(voiceevent.WithTurnID(context.Background(), "turn-x"), tts.SynthesizeRequest{Sentence: "silent"})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	drainAll(out)

	mu.Lock()
	defer mu.Unlock()
	if count != 0 {
		t.Errorf("published %d FirstAudio events for a chunkless sentence, want 0", count)
	}
}

// TestTee_NilBusPublishesNothing pins that the no-metrics path (nil bus) is
// inert: the tee still tees audio but publishes no FirstAudio and does not panic.
func TestTee_NilBusPublishesNothing(t *testing.T) {
	inner := &fakeSynth{chunks: []tts.AudioChunk{chunk(1, 24000)}}
	sink := wire.PlaybackSinkFunc(func(_ context.Context, chunks <-chan tts.AudioChunk) {
		go drainAll(chunks)
	})
	tee := wire.NewTeeSynthesizer(inner, sink, nil)
	out, err := tee.Synthesize(context.Background(), tts.SynthesizeRequest{Sentence: "no bus"})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	drainAll(out) // must not panic on a nil bus
}

// Compile-time assertion: the decorator is a drop-in [tts.Synthesizer].
var _ tts.Synthesizer = (*wire.TeeSynthesizer)(nil)
