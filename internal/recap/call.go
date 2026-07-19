package recap

import (
	"strings"
)

// recapInstruction is the Butler-flavoured recap directive appended after the
// Butler's Persona. Fixed text: the cassette prompt hash (ADR-0021) depends on it.
const recapInstruction = "You are recapping a past tabletop RPG voice session for the players. " +
	"Summarize what happened as a single coherent narrative recap in your own voice, " +
	"preserving the key events, characters, and decisions in the order they occurred. " +
	"Do not invent details that are not in the transcript."

// neutralInstruction is the map-step directive: a plain factual condensation with no
// persona, used per window before the Butler-flavoured reduce.
const neutralInstruction = "Condense the following transcript excerpt into a factual, concise summary. " +
	"Preserve the key events, characters, names, and decisions in order. " +
	"Do not add commentary, flavor, or details not present in the excerpt."

// answerLanguageLine, when a Campaign Language is set, pins the output language.
func answerLanguageLine(language string) string {
	if language == "" {
		return ""
	}
	return "\n\nAnswer in " + language + "."
}

// butlerSystemPrompt is the Persona-flavoured system prompt for a single-call recap
// or the reduce step: Persona + recap instruction + language pin.
func butlerSystemPrompt(persona, language string) string {
	var b strings.Builder
	if persona != "" {
		b.WriteString(persona)
		b.WriteString("\n\n")
	}
	b.WriteString(recapInstruction)
	b.WriteString(answerLanguageLine(language))
	return b.String()
}

// neutralSystemPrompt is the persona-free factual system prompt for the map step.
func neutralSystemPrompt(language string) string {
	return neutralInstruction + answerLanguageLine(language)
}
