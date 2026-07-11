package wire

import (
	"context"
	"io"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// collectFirstAudio subscribes a slice to FirstAudio on a fresh bus.
func collectFirstAudio(bus *voiceevent.Bus) *[]voiceevent.FirstAudio {
	var got []voiceevent.FirstAudio
	voiceevent.On(bus, func(e voiceevent.FirstAudio) { got = append(got, e) })
	return &got
}

// TestFirstAudioSource_PublishesOncePrecedingFirstOpus pins #375's delivery-aligned
// FirstAudio: for a look-ahead sentence the wrapped source publishes exactly one
// FirstAudio (for the right turn) on the first frame pulled to the wire, and — since
// the FirstAudio wrapper is INNER of the FirstOpus wrapper — it fires BEFORE FirstOpus
// on that same first frame.
func TestFirstAudioSource_PublishesOncePrecedingFirstOpus(t *testing.T) {
	bus := voiceevent.NewBus()
	var order []string
	voiceevent.On(bus, func(voiceevent.FirstAudio) { order = append(order, "audio") })
	voiceevent.On(bus, func(voiceevent.FirstOpus) { order = append(order, "opus") })

	inner := &scriptedSource{frames: [][]byte{{0x01}, {0x02}, {0x03}}}
	// Same wrap order as playSentenceBus: FirstAudio inner, FirstOpus outer.
	src := newFirstAudioSource(inner, bus, "R9", true)
	src = newFirstOpusSource(src, bus, "R9")

	for i := 0; i < 3; i++ {
		if _, err := src.NextFrame(context.Background()); err != nil {
			t.Fatalf("frame %d: unexpected err %v", i, err)
		}
	}
	if _, err := src.NextFrame(context.Background()); !errorsIsEOF(err) {
		t.Fatalf("want EOF after the scripted frames, got %v", err)
	}

	if len(order) != 2 || order[0] != "audio" || order[1] != "opus" {
		t.Fatalf("event order = %v, want exactly [audio opus] on the first frame", order)
	}
}

// TestFirstAudioSource_TurnIDAndOnce pins that the single FirstAudio carries the turn
// id and fires exactly once across many frames.
func TestFirstAudioSource_TurnIDAndOnce(t *testing.T) {
	bus := voiceevent.NewBus()
	got := collectFirstAudio(bus)
	inner := &scriptedSource{frames: [][]byte{{0x01}, {0x02}}}

	src := newFirstAudioSource(inner, bus, "R9", true)
	for i := 0; i < 2; i++ {
		if _, err := src.NextFrame(context.Background()); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	if len(*got) != 1 {
		t.Fatalf("FirstAudio count = %d, want exactly 1", len(*got))
	}
	if (*got)[0].TurnID != "R9" {
		t.Fatalf("FirstAudio.TurnID = %q, want R9", (*got)[0].TurnID)
	}
}

// TestFirstAudioSource_NoFrameNoPublish pins "never plays ⇒ never publishes"
// structurally: a source that yields no frame (an EOF/error before any frame — a
// discarded look-ahead that is drained, never pulled) publishes NO FirstAudio.
func TestFirstAudioSource_NoFrameNoPublish(t *testing.T) {
	bus := voiceevent.NewBus()
	got := collectFirstAudio(bus)
	inner := &scriptedSource{err: io.EOF} // no frame ever

	src := newFirstAudioSource(inner, bus, "R9", true)
	if _, err := src.NextFrame(context.Background()); !errorsIsEOF(err) {
		t.Fatalf("want EOF, got %v", err)
	}
	if len(*got) != 0 {
		t.Fatalf("FirstAudio count = %d, want 0 for a no-frame source", len(*got))
	}
}

// TestFirstAudioSource_UnmarkedUnwrapped pins that an ORDINARY (non-look-ahead)
// sentence gets no FirstAudio from the playback source — the tee owns that path, so
// the wrapper returns the inner source untouched and emits nothing.
func TestFirstAudioSource_UnmarkedUnwrapped(t *testing.T) {
	bus := voiceevent.NewBus()
	got := collectFirstAudio(bus)
	inner := &scriptedSource{frames: [][]byte{{0x01}, {0x02}}}

	src := newFirstAudioSource(inner, bus, "T1", false) // lookahead=false → unwrapped
	if src != inner {
		t.Fatal("an unmarked sentence must not be wrapped (the tee owns its FirstAudio)")
	}
	for i := 0; i < 2; i++ {
		if _, err := src.NextFrame(context.Background()); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	if len(*got) != 0 {
		t.Fatalf("FirstAudio count = %d, want 0 from the wire for an ordinary sentence", len(*got))
	}
}

func errorsIsEOF(err error) bool { return err == io.EOF }
