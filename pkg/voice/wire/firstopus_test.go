package wire

import (
	"context"
	"errors"
	"io"
	"testing"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// scriptedSource yields the queued frames in order, then io.EOF (or a queued
// error). It records how many NextFrame calls it saw.
type scriptedSource struct {
	frames [][]byte
	err    error // returned in place of the first frame when set
	calls  int
}

func (s *scriptedSource) NextFrame(context.Context) ([]byte, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	if len(s.frames) == 0 {
		return nil, io.EOF
	}
	f := s.frames[0]
	s.frames = s.frames[1:]
	return f, nil
}

// collectFirstOpus subscribes a slice to FirstOpus on a fresh bus.
func collectFirstOpus(bus *voiceevent.Bus) *[]voiceevent.FirstOpus {
	var got []voiceevent.FirstOpus
	voiceevent.On(bus, func(e voiceevent.FirstOpus) { got = append(got, e) })
	return &got
}

// TestFirstOpusSource_PublishesOnFirstFrameOnly pins the audible-on-wire signal:
// the wrapped Source publishes exactly one FirstOpus (for the right turn) on the
// first frame it yields, and never again on later frames.
func TestFirstOpusSource_PublishesOnFirstFrameOnly(t *testing.T) {
	bus := voiceevent.NewBus()
	got := collectFirstOpus(bus)
	inner := &scriptedSource{frames: [][]byte{{0x01}, {0x02}, {0x03}}}

	src := newFirstOpusSource(inner, bus, "T9")

	for i := 0; i < 3; i++ {
		if _, err := src.NextFrame(context.Background()); err != nil {
			t.Fatalf("frame %d: unexpected err %v", i, err)
		}
	}
	if _, err := src.NextFrame(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF after the scripted frames, got %v", err)
	}

	if len(*got) != 1 {
		t.Fatalf("FirstOpus fired %d times, want exactly 1 (first frame only)", len(*got))
	}
	if (*got)[0].TurnID != "T9" {
		t.Errorf("FirstOpus TurnID = %q, want T9", (*got)[0].TurnID)
	}
}

// TestFirstOpusSource_NoFrameNoPublish proves a Source that errors/EOFs before
// yielding any frame publishes nothing — nothing reached the wire.
func TestFirstOpusSource_NoFrameNoPublish(t *testing.T) {
	bus := voiceevent.NewBus()
	got := collectFirstOpus(bus)
	inner := &scriptedSource{err: errors.New("cancelled before any frame")}

	src := newFirstOpusSource(inner, bus, "T9")
	if _, err := src.NextFrame(context.Background()); err == nil {
		t.Fatal("expected the inner error to propagate")
	}
	if len(*got) != 0 {
		t.Fatalf("FirstOpus fired %d times, want 0 (no frame reached the wire)", len(*got))
	}
}

// TestFirstOpusSource_NoWrapWhenNilBusOrNoTurnID proves the decorator is a
// transparent no-op (returns the inner Source unwrapped) when there is nothing to
// publish — the keyless/test/non-correlated path pays no overhead.
func TestFirstOpusSource_NoWrapWhenNilBusOrNoTurnID(t *testing.T) {
	inner := &scriptedSource{frames: [][]byte{{0x01}}}

	if got := newFirstOpusSource(inner, nil, "T9"); got != inner {
		t.Error("nil bus must return the inner Source unwrapped")
	}
	bus := voiceevent.NewBus()
	if got := newFirstOpusSource(inner, bus, ""); got != inner {
		t.Error("empty turn id must return the inner Source unwrapped")
	}
}

// pullingPlayer is a sessionPlayer that pulls the first frame from the Source it
// is handed (modelling disgo's sender), so an end-to-end playSentenceBus test can
// observe the FirstOpus the wrapped Source publishes.
type pullingPlayer struct{ src gxvoice.Source }

func (p *pullingPlayer) Play(ctx context.Context, src gxvoice.Source) (playback, error) {
	p.src = src
	_, _ = src.NextFrame(ctx) // pull one frame: the audible-on-wire moment
	done := make(chan struct{})
	close(done)
	return &fakePlayback{done: done}, nil
}

// TestPlaySentenceBus_WrapsWithCtxTurnID is the integration pin: playSentenceBus
// recovers the turn id from ctx and the first frame pulled to the wire publishes
// FirstOpus for that turn.
func TestPlaySentenceBus_WrapsWithCtxTurnID(t *testing.T) {
	bus := voiceevent.NewBus()
	got := collectFirstOpus(bus)

	// fakeCodec.PlaybackSource returns a single-frame source so Play's pull yields.
	codec := framingCodec{frames: [][]byte{{0x01}}}
	ctx := voiceevent.WithTurnID(context.Background(), "Twire")
	chunks := make(chan tts.AudioChunk)
	close(chunks) // no chunks needed; the codec ignores them here

	if err := playSentenceBus(ctx, &pullingPlayer{}, codec, chunks, bus, nil); err != nil {
		t.Fatalf("playSentenceBus: %v", err)
	}
	if len(*got) != 1 || (*got)[0].TurnID != "Twire" {
		t.Fatalf("FirstOpus = %+v, want one for Twire", *got)
	}
}

// TestPlaySentenceBus_TwoSentencesPublishPerSource pins the multi-sentence shape:
// a turn plays several sentences, each its OWN playSentenceBus call over its own
// Source, so each sentence's first frame publishes its own FirstOpus (the
// once-guard is per-Source). Two sentences ⇒ two FirstOpus, both for the same
// turn. The headline-SLO once-per-turn dedup therefore lives in the subscriber
// (latencyDone — see internal/observe TestStageSubscriberHeadlineExactlyOnce,
// which feeds multiple FirstOpus and records exactly one sample), which is robust
// to future pump refactors. This test guards the producer half of that contract.
func TestPlaySentenceBus_TwoSentencesPublishPerSource(t *testing.T) {
	bus := voiceevent.NewBus()
	got := collectFirstOpus(bus)
	codec := framingCodec{frames: [][]byte{{0x01}}}
	ctx := voiceevent.WithTurnID(context.Background(), "Tmulti")

	for i := 0; i < 2; i++ {
		chunks := make(chan tts.AudioChunk)
		close(chunks)
		if err := playSentenceBus(ctx, &pullingPlayer{}, codec, chunks, bus, nil); err != nil {
			t.Fatalf("sentence %d: playSentenceBus: %v", i, err)
		}
	}

	if len(*got) != 2 {
		t.Fatalf("FirstOpus fired %d times, want 2 (one per sentence Source)", len(*got))
	}
	for i, e := range *got {
		if e.TurnID != "Tmulti" {
			t.Errorf("FirstOpus[%d].TurnID = %q, want Tmulti", i, e.TurnID)
		}
	}
}

// framingCodec is a fakeCodec whose PlaybackSource yields scripted frames so the
// pulling player's NextFrame returns a real frame (triggering FirstOpus).
type framingCodec struct{ frames [][]byte }

func (c framingCodec) DecodeInbound(gxvoice.Frame) ([]audio.Frame, error) { return nil, nil }
func (c framingCodec) PlaybackSource(<-chan tts.AudioChunk) (gxvoice.Source, error) {
	return &scriptedSource{frames: c.frames}, nil
}
