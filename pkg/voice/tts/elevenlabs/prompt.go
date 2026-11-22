package elevenlabs

import (
	"encoding/json"
	"strings"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// audioTagVocabulary is the eleven_v3 tag vocabulary the prompt teaches, by
// category, in prompt order.
var audioTagVocabulary = []struct {
	name string
	tags []string
}{
	{"emotion", []string{"cheerfully", "sad", "angry", "excited", "curious", "nervous", "confident", "whispers", "shouting"}},
	{"non-verbal", []string{"laughs", "chuckles", "sighs", "gasps", "clears throat", "coughs", "hesitates"}},
	{"pacing", []string{"pause", "long pause", "slow", "fast", "drawn out", "stuttering"}},
}

// AudioTags returns the audio-tag vocabulary [Client.AudioMarkupPrompt] lists,
// without the brackets. A bracketed span outside it is not a tag the model was
// offered: an invented one, or a stage direction in tag clothing, and the
// engine may read it aloud. A Voice's SuggestedAudioTags are not included.
func AudioTags() []string {
	var out []string
	for _, cat := range audioTagVocabulary {
		out = append(out, cat.tags...)
	}
	return out
}

// plainProsePrompt is the markup instruction for a voice on a model with no
// audio-tag support (flash, multilingual v2, turbo v2.5): those models read
// brackets aloud, so the LLM must not emit any. Non-empty per ADR-0022 ("use
// plain prose only" is an answer).
const plainProsePrompt = "This voice renders plain spoken prose only. " +
	"Do not write audio tags in square brackets, SSML or any other markup: it would be read aloud."

// AudioMarkupPrompt implements [tts.Synthesizer].
//
// Returns the system-prompt fragment that teaches the LLM how to format spoken
// text for the voice's model ([ModelOf]). The v3/v4 families take inline
// bracketed audio tags; the prompt is intentionally specific: tags are not SSML,
// and unrecognised brackets may be read aloud verbatim, so a vague "use SSML or
// whatever" instruction would regress quality. Pauses are taught as tags only:
// eleven_v4_turbo silently strips SSML <break/> (measured, #702), and an
// ellipsis would split the sentence at the agent's sentence splitter. A model
// without tag support gets [plainProsePrompt].
//
// If the Voice's Settings carry SuggestedAudioTags ([Settings.SuggestedAudioTags]),
// they are appended as a per-voice preference list so the LLM biases toward
// the tag palette that matches the voice's character (e.g. a Butler voice
// might prefer [confident], a frightened-NPC voice might prefer [whispers]).
func (c *Client) AudioMarkupPrompt(voice tts.Voice) string {
	if !supportsAudioTags(ModelOf(voice)) {
		return plainProsePrompt
	}
	var b strings.Builder
	b.WriteString("This voice is rendered by an ElevenLabs model that understands inline audio tags. ")
	b.WriteString("Format spoken text with inline audio tags in square brackets to control delivery (NOT SSML). ")
	b.WriteString("Each tag affects approximately the next 4–5 words before delivery returns to normal. ")
	b.WriteString("Place tags before the words they modify; never wrap a whole sentence in a tag. ")
	b.WriteString("Use sparingly: 1–3 tags per sentence is typical. ")
	b.WriteString("For a pause write [pause] or [long pause]; never use SSML break elements, which are ignored. ")
	b.WriteString("Do not invent tags outside the listed vocabulary — unrecognised brackets may be read aloud verbatim. ")
	b.WriteString("Supported tag vocabulary by category: ")
	for i, cat := range audioTagVocabulary {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(cat.name)
		b.WriteString(" ([")
		b.WriteString(strings.Join(cat.tags, "], ["))
		b.WriteString("])")
	}
	b.WriteString(".")

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

// supportsAudioTags reports whether model renders inline bracketed audio tags:
// the eleven_v3 and eleven_v4 families (eleven_v3, eleven_v3_conversational,
// eleven_v4, eleven_v4_turbo). Older families read brackets aloud.
func supportsAudioTags(model string) bool {
	return strings.HasPrefix(model, "eleven_v3") || strings.HasPrefix(model, "eleven_v4")
}
