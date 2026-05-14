// Package stt defines the Speech-to-Text provider surface consumed by the
// orchestrator (ADR-0019).
//
// Real providers (Deepgram, whisper.cpp — see ADR-0004) and the cassette
// replayer (see [github.com/MrWong99/Glyphoxa/pkg/voice/voicecassette]) both
// implement [Recognizer]. The orchestrator's STT stage is provider-agnostic:
// it forwards [audio.Frame]s and republishes the final transcript onto the
// shared event bus as [voiceevent.STTFinal].
//
// The v1.0 surface is utterance-scoped batch transcription — the VAD stage
// segments audio into utterances and the orchestrator hands each segment to
// the Recognizer. Streaming partial transcripts are deferred until barge-in
// (Q13.5) requires them.
package stt

import (
	"context"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
)

// Transcript is the authoritative result of transcribing one utterance.
type Transcript struct {
	// Text is the recognized utterance. Empty if the provider produced no
	// confident hypothesis (e.g. all silence reached the recognizer).
	Text string
}

// Recognizer transcribes a complete utterance into a [Transcript].
//
// Implementations must not mutate the input frames. The returned Transcript
// is the authoritative final result — what the orchestrator publishes as
// [voiceevent.STTFinal] and what cassettes pin per ADR-0021.
type Recognizer interface {
	Transcribe(ctx context.Context, frames []audio.Frame) (Transcript, error)
}
