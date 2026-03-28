package audio

import "time"

// Flusher is an optional interface that [Connection] implementations may
// support. When called, it discards any buffered outgoing audio and signals
// the remote end (e.g., the gateway) to do the same. This ensures that
// after a barge-in or mute, stale pre-buffered NPC audio stops immediately
// instead of continuing to play.
type Flusher interface {
	Flush()
}

// SelfHearingGuard is an optional interface that [Connection] implementations
// may support. When SetBotUserID is called with the bot's own user ID,
// incoming audio frames from that user are silently dropped. This prevents
// echo/feedback loops where an NPC hears its own TTS output.
type SelfHearingGuard interface {
	SetBotUserID(id string)
}

// AudioFrame represents a single frame of audio data flowing through the pipeline.
// Frames are the atomic unit of audio transport — captured from input streams,
// processed by VAD, encoded/decoded by codecs, and played through output streams.
type AudioFrame struct {
	// PCM audio data. Sample rate and channel count are determined by the pipeline config.
	Data []byte

	// SampleRate in Hz (e.g., 48000 for Discord Opus, 16000 for STT).
	SampleRate int

	// Channels: 1 for mono (STT input), 2 for stereo (Discord output).
	Channels int

	// Timestamp marks when this frame was captured, relative to stream start.
	Timestamp time.Duration
}
