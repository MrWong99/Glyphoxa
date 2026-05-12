// Package tts defines the v2 Text-to-Speech provider surface.
//
// The package splits a small required core ([Synthesizer]) from optional
// capability interfaces ([VoiceLister], [VoiceCloner], [VoiceDesigner],
// [DialogueSynthesizer]). Hot-path callers depend only on Synthesizer;
// admin and catalog tooling type-asserts the capabilities it needs.
//
// Per ADR-0022:
//   - Synthesize is one-call-per-sentence: the returned channel's close marks
//     the sentence as fully synthesized, aligning with ADR-0012's
//     deliver-then-commit boundary.
//   - [AudioChunk] is self-describing; the orchestrator owns resampling.
//   - [Voice] carries identity plus opaque provider-typed Settings.
//   - Sentence text is opaque; Persona prompts learn provider-appropriate
//     markup syntax via [Synthesizer.AudioMarkupPrompt].
package tts

import "context"

// Synthesizer is the hot-path interface implemented by every TTS provider.
//
// Per ADR-0022 the lifecycle is one call per sentence: each Synthesize call
// renders exactly one sentence, and the returned channel's close marks the
// sentence as fully synthesized. ADR-0012's deliver-then-commit semantics
// align with this boundary — once the orchestrator has forwarded the last
// frame from the channel to Discord, the Transcript utterance for that
// sentence may be committed.
type Synthesizer interface {
	// Synthesize renders one sentence as a stream of [AudioChunk]s.
	//
	// The returned channel is closed by the implementation when synthesis is
	// complete or ctx is cancelled. Callers must drain the channel to release
	// the implementation's goroutines.
	//
	// Returns a non-nil error only if the call cannot be started; mid-stream
	// failures close the channel early.
	Synthesize(ctx context.Context, req SynthesizeRequest) (<-chan AudioChunk, error)

	// AudioMarkupPrompt returns a system-prompt fragment instructing an LLM
	// how to format spoken text for the given Voice. The Persona layer
	// concatenates the returned string into the Agent's LLM system prompt.
	//
	// Implementations must return non-empty text describing either what
	// markup the LLM should use (e.g. ElevenLabs v3 brackets) or that no
	// markup should be emitted (e.g. OpenAI). Empty return is a contract
	// violation.
	AudioMarkupPrompt(voice Voice) string
}
