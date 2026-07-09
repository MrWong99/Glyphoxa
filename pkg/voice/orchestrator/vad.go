// Package orchestrator hosts the voice pipeline stages under test in slice 1
// (per ADR-0019). Each stage owns a piece of voice-pipeline logic — VAD,
// STT, address detection, turn-taking, barge-in, sentence-streamed TTS,
// agent + butler — and publishes its observable behaviour onto a shared
// [voiceevent.Bus] (per ADR-0020).
//
// The orchestrator is the system under test. Vendor adapters (silero,
// Anthropic, Deepgram, ElevenLabs, …) are inputs/outputs we trust; the
// orchestrator is what TDD is shaped against.
package orchestrator

import (
	"fmt"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// VAD is the orchestrator stage that wraps a [vad.SessionHandle] and
// republishes its frame-level transitions onto a [voiceevent.Bus] using the
// shared event taxonomy (ADR-0020).
//
// The wrapped session is synchronous and frame-driven; callers feed PCM
// frames via [VAD.Process] in the audio loop. Per-frame "still speaking"
// and "still silent" states are not republished — only the transitions
// (speech_start, speech_end) cross the bus boundary.
type VAD struct {
	bus     *voiceevent.Bus
	session vad.SessionHandle

	// rec/hangover carry the #125 instrumentation: one vad_hangover span per
	// speech_end, recording the fixed end-of-speech detection lag
	// (minSilenceFrames*frameMs) the stage is constructed with. The value is a
	// constant, not derived per-frame, because vad.Session does not expose its
	// minSilenceFrames — wirenpc computes it from the same consts it configures the
	// session with. rec defaults to a no-op (observe.Discard) so the keyless path
	// stays silent.
	rec      observe.StageRecorder
	hangover time.Duration
}

// VADOption configures a [VAD] at construction. [WithVADMetrics] opts the
// vad_hangover instrumentation in.
type VADOption func(*VAD)

// WithVADMetrics injects the #125 instrumentation: rec receives one
// [observe.StageRecorder.VADHangover] span per speech_end, valued hangover (the
// fixed minSilenceFrames*frameMs end-of-speech lag). A nil rec leaves the no-op
// default in place.
func WithVADMetrics(rec observe.StageRecorder, hangover time.Duration) VADOption {
	return func(v *VAD) {
		if rec != nil {
			v.rec = rec
		}
		v.hangover = hangover
	}
}

// NewVAD wires session into bus. Both must be non-nil; passing nil for
// either panics. The caller owns session and is responsible for closing it.
// Pass [WithVADMetrics] to record the end-of-speech hangover; without it the
// stage records nothing (the keyless default).
func NewVAD(bus *voiceevent.Bus, session vad.SessionHandle, opts ...VADOption) *VAD {
	if bus == nil {
		panic("orchestrator.NewVAD: bus must not be nil")
	}
	if session == nil {
		panic("orchestrator.NewVAD: session must not be nil")
	}
	v := &VAD{bus: bus, session: session, rec: observe.Discard{}}
	for _, o := range opts {
		o(v)
	}
	return v
}

// Process feeds one PCM frame through the underlying VAD session and
// republishes the per-frame transitions onto the bus: speech_start at the
// onset of an utterance, speech_end when the speaker has been quiet for the
// session's silence-frame threshold. Per-frame "still speaking" / "still
// silent" states stay inside the session and are not published.
func (v *VAD) Process(frame audio.Frame) error {
	evt, err := v.session.ProcessFrame(frame)
	if err != nil {
		return fmt.Errorf("orchestrator.VAD.Process: %w", err)
	}
	// The transition inherits the frame's attribution (ADR-0050): a per-lane VAD is
	// fed only its speaker's frames, so stamping frame.Speaker() names the lane the
	// segmenter routes this transition to. An unattributed ("") frame — the default
	// lane / single-lane MVP path — leaves SpeakerID empty, unchanged.
	speaker := frame.Speaker()
	switch evt.Type {
	case vad.VADSpeechStart:
		v.bus.Publish(voiceevent.VADSpeechStart{
			At:          time.Now(),
			Probability: evt.Probability,
			SpeakerID:   speaker,
		})
	case vad.VADSpeechEnd:
		v.bus.Publish(voiceevent.VADSpeechEnd{
			At:          time.Now(),
			Probability: evt.Probability,
			SpeakerID:   speaker,
		})
		// #125: stamp the fixed end-of-speech detection lag once per speech_end.
		v.rec.VADHangover(v.hangover)
	}
	return nil
}
