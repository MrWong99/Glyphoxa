package agent

import (
	"strings"
	"unicode"
)

// spokenOutputRule tells the model its words are synthesized, not rendered. It
// is session-constant, so it rides the stable system prompt (ADR-0059's
// cacheable prefix), in English like every other prompt block here. Without it a
// chat-trained model formats for a screen — bold, bullet lists, headings — and a
// TTS voice either reads the markers out or stumbles over them. The
// deterministic [scrubSpoken] in the delivery path is the guarantee; this only
// makes the mistake rarer.
//
// It is rendered only for an Agent whose every reply is spoken: a voiced
// Persona without a [Config.TextSink]. A Butler that may answer in channel text
// (#299) is not told that everything it writes is read aloud, because that is
// not true of it (see [selfPrefixRule]).
const spokenOutputRule = "Everything you write is spoken aloud by a voice, never shown on a screen. " +
	"Write the way people talk: plain sentences, no markdown, no asterisks, no headings, " +
	"no bullet or numbered lists, no emojis, no code."

// spokenOutputMarkupExemption is appended to [spokenOutputRule] when the Voice
// has an audio-markup instruction (ADR-0022), so the "no markup" rule does not
// read as forbidding the bracketed audio tags the next block asks for.
const spokenOutputMarkupExemption = " The audio tags described below are the one exception."

// scrubSpoken removes markdown formatting from one sentence on its way to TTS
// (O12): emphasis markers, heading and bullet markers at a line start,
// blockquote markers, backticks and code fences. It is the delivery-path half of
// [spokenOutputRule], applied to every spoken sentence — and therefore to what
// is committed to history and recorded in the Transcript, which is what the room
// heard.
//
// It is deliberately conservative. Only formatting is removed, never words, and
// a marker is dropped only where markdown would treat it as formatting:
//
//   - Square brackets are never touched: "[cheerfully]" is an intentional
//     ElevenLabs v3 audio tag (ADR-0022).
//   - A "*" or "_" run is emphasis only where it OPENS on a word (after a
//     space or at the start: "**Wichtig", "*lacht") or CLOSES one — and a closer
//     counts only if an opener came before it on the line, or if it follows
//     punctuation ("Gefahr im Verzug.**": the sentence boundary routinely
//     separates a closer from its opener). Everything else is content and
//     stays: "2*3", "M*A*S*H", "snake_case", "3 * 4", "5*(2+1)", "F*** off".
//   - A "- ", "+ " or "> " in front of a digit is a sign, not a bullet or a
//     quote: "- 5 Grad draußen", "+ 2 auf den Wurf", "> 5 Gold?".
//   - A numbered list keeps its number ("1. Geh nach Norden"): the number is
//     speakable and carries the order.
//
// A sentence with no formatting is returned unchanged (same string).
func scrubSpoken(sentence string) string {
	if !strings.ContainsAny(sentence, "*_#`>-+•") {
		return sentence
	}
	lines := strings.Split(sentence, "\n")
	for i, line := range lines {
		line = strings.ReplaceAll(scrubFence(line), "`", "")
		lines[i] = scrubEmphasis(scrubLineStart(line))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// scrubFence blanks a code-fence line ("```" or "```json"): the fence and its
// language tag are markup with nothing to say. A line that merely opens with
// three backticks and goes on ("```Hallo Welt```") keeps its words — the
// backticks themselves are removed by the caller.
func scrubFence(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "```") {
		return line
	}
	tag := strings.TrimLeft(trimmed, "`")
	for _, r := range tag {
		if !isWordRune(r) && r != '+' && r != '-' {
			return line
		}
	}
	return ""
}

// scrubLineStart strips the block markers markdown recognizes at the start of a
// line: a "> " blockquote, a "# " … "###### " heading, and a "- " / "* " /
// "+ " / "• " bullet. Each marker needs the whitespace after it, so "#1",
// "-5 Grad" and "*lacht*" are left for [scrubEmphasis] or left alone; and "-",
// "+" and ">" are left alone in front of a digit, where they are a sign or a
// comparison the sentence needs ("- 5 Grad draußen").
func scrubLineStart(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	rest := trimmed
	for strings.HasPrefix(rest, "> ") && !startsWithDigit(rest[1:]) {
		rest = strings.TrimLeft(rest[1:], " \t")
	}
	switch {
	case strings.HasPrefix(rest, "#"):
		n := 0
		for n < len(rest) && rest[n] == '#' {
			n++
		}
		if n <= 6 && n < len(rest) && (rest[n] == ' ' || rest[n] == '\t') {
			rest = strings.TrimLeft(rest[n:], " \t")
		}
	case strings.HasPrefix(rest, "- "), strings.HasPrefix(rest, "+ "):
		if !startsWithDigit(rest[1:]) {
			rest = strings.TrimLeft(rest[1:], " \t")
		}
	case strings.HasPrefix(rest, "* "):
		rest = strings.TrimLeft(rest[1:], " \t")
	case strings.HasPrefix(rest, "• "):
		rest = strings.TrimLeft(rest[len("•"):], " \t")
	}
	if len(rest) == len(trimmed) {
		return line // nothing stripped: keep the line byte-identical
	}
	return rest
}

// startsWithDigit reports whether the first non-space rune of s is a digit.
func startsWithDigit(s string) bool {
	for _, r := range s {
		if r == ' ' || r == '\t' {
			continue
		}
		return unicode.IsDigit(r)
	}
	return false
}

// scrubEmphasis drops the runs of "*" and "_" that markdown would read as
// emphasis and keeps the ones that are content (see [scrubSpoken] for the
// rule).
func scrubEmphasis(line string) string {
	if !strings.ContainsAny(line, "*_") {
		return line
	}
	runes := []rune(line)
	var b strings.Builder
	b.Grow(len(line))
	open := 0 // openers dropped on this line and not yet closed
	for i := 0; i < len(runes); {
		r := runes[i]
		if r != '*' && r != '_' {
			b.WriteRune(r)
			i++
			continue
		}
		j := i
		for j < len(runes) && runes[j] == r {
			j++
		}
		atStart, atEnd := i == 0, j == len(runes)
		var prev, next rune
		if !atStart {
			prev = runes[i-1]
		}
		if !atEnd {
			next = runes[j]
		}
		opens := (atStart || unicode.IsSpace(prev) || isOpeningPunct(prev)) && !atEnd && isWordRune(next)
		closes := !atStart && !unicode.IsSpace(prev) && (atEnd || unicode.IsSpace(next) || isClosingPunct(next))
		switch {
		case opens:
			open++
		case closes && open > 0:
			open--
		case closes && !isWordRune(prev):
			// an orphaned closer behind punctuation: its opener was in the
			// previous sentence
		default:
			b.WriteString(string(runes[i:j])) // content
		}
		i = j
	}
	return b.String()
}

// isOpeningPunct reports whether r can stand directly in front of an emphasized
// word.
func isOpeningPunct(r rune) bool { return strings.ContainsRune("(\"'„“‚‘«»", r) }

// isClosingPunct reports whether r can stand directly behind an emphasized
// word.
func isClosingPunct(r rune) bool { return strings.ContainsRune(".,;:!?)\"'“”‘’«»…", r) }

// isWordRune reports whether r is part of a word for the emphasis test.
func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
