package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// STT is the orchestrator stage that hands utterance audio to a
// [stt.Recognizer] and republishes the authoritative transcript as
// [voiceevent.STTFinal] on the shared bus.
//
// In the full slice-1 wiring the VAD stage segments audio into utterances
// and calls Transcribe with each segment's frames; tracer-bullet tests
// short-circuit that and feed pre-segmented clips directly.
type STT struct {
	bus        *voiceevent.Bus
	recognizer stt.Recognizer
}

// NewSTT wires recognizer into bus. Both must be non-nil; passing nil panics.
func NewSTT(bus *voiceevent.Bus, recognizer stt.Recognizer) *STT {
	if bus == nil {
		panic("orchestrator.NewSTT: bus must not be nil")
	}
	if recognizer == nil {
		panic("orchestrator.NewSTT: recognizer must not be nil")
	}
	return &STT{bus: bus, recognizer: recognizer}
}

// Transcribe forwards frames to the recognizer and, on success, publishes
// the resulting transcript as a [voiceevent.STTFinal]. Errors from the
// recognizer are wrapped and returned without publishing.
//
// An empty transcript (Text == "") is still published. Downstream consumers
// — not this stage — decide what to do with a "the recognizer heard nothing"
// signal; the orchestrator's job is to faithfully relay whatever the
// recognizer authoritatively returns.
func (s *STT) Transcribe(ctx context.Context, frames []audio.Frame) error {
	t, err := s.recognizer.Transcribe(ctx, frames)
	if err != nil {
		return fmt.Errorf("orchestrator.STT.Transcribe: %w", err)
	}
	// A turn is born here (A3): mint its correlation id and carry forward the
	// segment's speech-end time (from the Segmenter via ctx) so the headline
	// response-latency span is anchored at the true end-of-speech.
	s.bus.Publish(voiceevent.STTFinal{
		At:          time.Now(),
		Text:        t.Text,
		TurnID:      voiceevent.NewTurnID(),
		SpeechEndAt: speechEndAtFrom(ctx),
	})
	return nil
}

// speechEndAtKey carries the segment's [voiceevent.VADSpeechEnd.At] from the
// [Segmenter] to [STT.Transcribe] without widening the Transcribe signature
// (which tracer-bullet tests and the cassette path call directly). Unexported
// and orchestrator-internal.
type speechEndAtKey struct{}

// withSpeechEndAt returns ctx carrying the turn's speech-end time; a zero time
// (a Flush with no speech-end transition) is carried as-is.
func withSpeechEndAt(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, speechEndAtKey{}, at)
}

// speechEndAtFrom recovers the speech-end time, or the zero time if the segment
// was transcribed without one (a direct Transcribe call, or an end-of-stream
// Flush).
func speechEndAtFrom(ctx context.Context) time.Time {
	at, _ := ctx.Value(speechEndAtKey{}).(time.Time)
	return at
}
