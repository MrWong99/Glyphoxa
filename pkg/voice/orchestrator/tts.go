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
// TTSInvoked is published BEFORE the [tts.Synthesizer.Synthesize] call — it
// announces the dispatch attempt, not a success. A sentence whose Synthesize
// start-errors (empty VoiceID, auth failure, bad request) therefore still emits
// TTSInvoked, so the failed sentence is visible as invoked-but-never-spoke rather
// than vanishing with no per-sentence signal (#20); the start error is then
// wrapped and returned. (A FirstAudio for the sentence is what marks it actually
// spoken — see [voiceevent.FirstAudio].)
//
// Per ADR-0022 callers must drain the returned audio channel to release the
// implementation's goroutines; this stage drains and discards the chunks
// itself (per ADR-0021's TTS cassette policy the audio is not observable to
// tests).
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
	// Assign the per-turn index under the lock so concurrent dispatches (an
	// Ensemble Turn or a barge-in canceller, both anticipated below) get
	// distinct, monotonically increasing indices. The index counts dispatch
	// attempts within the turn, assigned before Synthesize so a start-errored
	// sentence still consumes its slot and stays visible.
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

	ch, err := s.synthesizer.Synthesize(ctx, tts.SynthesizeRequest{
		Sentence: sentence,
		Voice:    voice,
	})
	if err != nil {
		return fmt.Errorf("orchestrator.TTS.Dispatch: %w", err)
	}
	for range ch {
	}
	return nil
}
