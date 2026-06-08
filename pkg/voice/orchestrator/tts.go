package orchestrator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// TTS is the orchestrator stage that hands one sentence at a time to a
// [tts.Synthesizer] and republishes the dispatch as a [voiceevent.TTSInvoked]
// on the shared bus.
//
// Per ADR-0021's TTS cassette policy this stage's observable contract is the
// dispatch signal, not the audio: synthesized chunks are drained and dropped
// here. Playback wiring (resampler, Opus encoder, Discord transport) attaches
// on top in a later tracer bullet.
type TTS struct {
	bus         *voiceevent.Bus
	synthesizer tts.Synthesizer

	mu        sync.Mutex
	nextIndex int
}

// NewTTS wires synthesizer into bus. Both must be non-nil; passing nil panics.
func NewTTS(bus *voiceevent.Bus, synthesizer tts.Synthesizer) *TTS {
	if bus == nil {
		panic("orchestrator.NewTTS: bus must not be nil")
	}
	if synthesizer == nil {
		panic("orchestrator.NewTTS: synthesizer must not be nil")
	}
	return &TTS{bus: bus, synthesizer: synthesizer}
}

// Dispatch hands one sentence to the configured synthesizer and publishes a
// [voiceevent.TTSInvoked] event identifying the sentence and its 0-based
// position in the turn.
//
// Per ADR-0022 callers must drain the returned audio channel to release the
// implementation's goroutines; this stage drains and discards the chunks
// itself (per ADR-0021's TTS cassette policy the audio is not observable to
// tests). Errors from the synthesizer are wrapped and returned without
// publishing, and do not advance the per-turn index.
//
// Forward-looking shape — when the playback pipeline lands, the drain loop
// becomes a forward into the resampler/aligner that feeds the Opus encoder
// driving Discord (ADR-0005: audio stays in-process). The bus event remains
// the same dispatch signal; production consumers attach to it for
// turn-tracking, the SSE relay (ADR-0014), and barge-in (Q13.5 open) —
// barge-in cancels ctx, the synthesizer closes the channel mid-stream, the
// forward loop unwinds, and ADR-0012's deliver-then-commit boundary decides
// which already-spoken sentences become committed Transcript entries.
// nextIndex is 0-based within the current turn; turn-taking (later tracer
// bullet) will scope a fresh TTS per Agent reply rather than reset this one
// in place.
func (s *TTS) Dispatch(ctx context.Context, sentence string, voice tts.Voice) error {
	ch, err := s.synthesizer.Synthesize(ctx, tts.SynthesizeRequest{
		Sentence: sentence,
		Voice:    voice,
	})
	if err != nil {
		return fmt.Errorf("orchestrator.TTS.Dispatch: %w", err)
	}
	// Assign the per-turn index under the lock so concurrent dispatches (an
	// Ensemble Turn or a barge-in canceller, both anticipated below) get
	// distinct, monotonically increasing indices. Only a successful synthesis
	// consumes an index — the error path above returns before this.
	s.mu.Lock()
	index := s.nextIndex
	s.nextIndex++
	s.mu.Unlock()

	s.bus.Publish(voiceevent.TTSInvoked{
		At:       time.Now(),
		Sentence: sentence,
		Index:    index,
		TurnID:   voiceevent.TurnIDFrom(ctx),
	})
	for range ch {
	}
	return nil
}
