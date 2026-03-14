package memory

import (
	"time"
)

// SpeakerRole classifies the speaker within the session context.
type SpeakerRole string

const (
	// RoleDefault is the zero-value role for ordinary players.
	RoleDefault SpeakerRole = ""

	// RoleGM identifies the Game Master (DM) player.
	RoleGM SpeakerRole = "gm"

	// RoleGMAssistant identifies an NPC acting as the GM's AI assistant.
	RoleGMAssistant SpeakerRole = "gm_assistant"
)

// DisplaySuffix returns a human-readable label suitable for appending to
// speaker names in formatted transcripts. Returns "" for [RoleDefault].
func (r SpeakerRole) DisplaySuffix() string {
	switch r {
	case RoleGM:
		return " [GM]"
	case RoleGMAssistant:
		return " [GM Assistant]"
	default:
		return ""
	}
}

// TranscriptEntry is a complete exchange record written to the session log.
// It captures both the speaker's utterance and optionally the NPC's response,
// forming the atomic unit of session history.
type TranscriptEntry struct {
	// SpeakerID identifies who spoke (player user ID or NPC name).
	SpeakerID string

	// SpeakerName is the human-readable speaker name.
	SpeakerName string

	// SpeakerRole classifies the speaker (e.g., GM, GM assistant).
	// The zero value means ordinary player.
	SpeakerRole SpeakerRole

	// Text is the (possibly corrected) transcript text.
	Text string

	// RawText is the original uncorrected STT output. Preserved for debugging.
	RawText string

	// NPCID identifies the NPC agent that produced this entry.
	// Empty for non-NPC (e.g. player) entries.
	NPCID string

	// Timestamp is when this entry was recorded.
	Timestamp time.Time

	// Duration is the length of the utterance.
	Duration time.Duration
}

// IsNPC reports whether this entry was produced by an NPC agent.
func (e TranscriptEntry) IsNPC() bool { return e.NPCID != "" }

// SessionInfo holds metadata about a recorded session.
type SessionInfo struct {
	// SessionID is the unique session identifier.
	SessionID string

	// CampaignID identifies the campaign this session belongs to.
	CampaignID string

	// StartedAt is when the session was started.
	StartedAt time.Time

	// EndedAt is when the session ended. Zero value if still active.
	EndedAt time.Time
}

// Recap is the generated "Previously On..." voiced summary for a session.
type Recap struct {
	// SessionID is the session this recap was generated from.
	SessionID string

	// CampaignID identifies the campaign.
	CampaignID string

	// Text is the dramatic narrative recap text.
	Text string

	// AudioData is the rendered PCM audio bytes.
	AudioData []byte

	// SampleRate is the audio sample rate in Hz.
	SampleRate int

	// Channels is the number of audio channels.
	Channels int

	// Duration is the estimated speech duration.
	Duration time.Duration

	// GeneratedAt is when this recap was created.
	GeneratedAt time.Time
}
