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
}

// NewVAD wires session into bus. Both must be non-nil; passing nil for
// either panics. The caller owns session and is responsible for closing it.
func NewVAD(bus *voiceevent.Bus, session vad.SessionHandle) *VAD {
	if bus == nil {
		panic("orchestrator.NewVAD: bus must not be nil")
	}
	if session == nil {
		panic("orchestrator.NewVAD: session must not be nil")
	}
	return &VAD{bus: bus, session: session}
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
	switch evt.Type {
	case vad.VADSpeechStart:
		v.bus.Publish(voiceevent.VADSpeechStart{
			At:          time.Now(),
			Probability: evt.Probability,
		})
	case vad.VADSpeechEnd:
		v.bus.Publish(voiceevent.VADSpeechEnd{
			At:          time.Now(),
			Probability: evt.Probability,
		})
	}
	return nil
}
