package vad

// VADEvent represents a voice activity detection result for a single audio frame.
type VADEvent struct {
	// Type is the detection result.
	Type VADEventType

	// Probability is the speech probability score (0.0–1.0).
	Probability float64
}

// VADEventType enumerates VAD detection states.
type VADEventType int

const (
	// VADSpeechStart indicates speech has just begun.
	VADSpeechStart VADEventType = iota

	// VADSpeechContinue indicates ongoing speech.
	VADSpeechContinue

	// VADSpeechEnd indicates speech has just ended.
	VADSpeechEnd

	// VADSilence indicates no speech detected.
	VADSilence

	// VADVoicingStopped indicates voicing has provisionally paused inside a
	// still-open utterance: the first sub-threshold frame after voiced frames,
	// while the end-of-speech hangover is still counting down. It is NOT a
	// segment boundary — the utterance stays open and either resumes
	// (VADVoicingResumed) or ends for real (VADSpeechEnd) once the hangover
	// elapses. It exists so latency-sensitive consumers (the barge-in confirm
	// window, #431) can observe when a speaker actually fell silent without
	// waiting out the hangover, whose job is utterance merging for STT.
	VADVoicingStopped

	// VADVoicingResumed indicates voicing picked back up inside the utterance
	// after a VADVoicingStopped, before the hangover elapsed — so no new
	// VADSpeechStart fires, but the speaker is audibly talking again.
	VADVoicingResumed
)
