package elevenlabs

import (
	"encoding/json"
	"strings"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// AudioMarkupPrompt implements [tts.Synthesizer].
//
// Returns the system-prompt fragment that teaches the LLM how to format
// spoken text for ElevenLabs eleven_v3 using its inline bracketed
// audio-tag vocabulary. The prompt is intentionally specific: v3 tags are
// not SSML, and unrecognised brackets may be read aloud verbatim, so a
// vague "use SSML or whatever" instruction would regress quality.
//
// If the Voice's Settings carry SuggestedAudioTags ([Settings.SuggestedAudioTags]),
// they are appended as a per-voice preference list so the LLM biases toward
// the tag palette that matches the voice's character (e.g. a Butler voice
// might prefer [confident], a frightened-NPC voice might prefer [whispers]).
func (c *Client) AudioMarkupPrompt(voice tts.Voice) string {
	var b strings.Builder
	b.WriteString("This voice is rendered by ElevenLabs eleven_v3. ")
	b.WriteString("Format spoken text with inline audio tags in square brackets to control delivery (NOT SSML). ")
	b.WriteString("Each tag affects approximately the next 4–5 words before delivery returns to normal. ")
	b.WriteString("Place tags before the words they modify; never wrap a whole sentence in a tag. ")
	b.WriteString("Use sparingly: 1–3 tags per sentence is typical. ")
	b.WriteString("Do not invent tags outside the listed vocabulary — unrecognised brackets may be read aloud verbatim. ")
	b.WriteString("Supported tag vocabulary by category: ")
	b.WriteString("emotion ([cheerfully], [sad], [angry], [excited], [curious], [nervous], [confident], [whispers], [shouting]); ")
	b.WriteString("non-verbal ([laughs], [chuckles], [sighs], [gasps], [clears throat], [coughs], [hesitates]); ")
	b.WriteString("pacing ([pause], [long pause], [slow], [fast], [drawn out], [stuttering]).")

	if len(voice.Settings) > 0 {
		var s Settings
		if err := json.Unmarshal(voice.Settings, &s); err == nil && len(s.SuggestedAudioTags) > 0 {
			b.WriteString(" Prefer these tags for this voice: ")
			for i, tag := range s.SuggestedAudioTags {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString("[")
				b.WriteString(tag)
				b.WriteString("]")
			}
			b.WriteString(".")
		}
	}
	return b.String()
}
