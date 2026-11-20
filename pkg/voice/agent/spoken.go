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
//   - A "*" or "_" between two letters or digits ("2*3", "M*A*S*H",
//     "snake_case") or with whitespace on both sides ("3 * 4") is content, and
//     stays. A run that hugs a word on one side only ("**Wichtig:**", "*lacht*")
//     is emphasis, and goes — paired or not, since a sentence boundary routinely
//     separates an opening marker from its closer.
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
// "-5 Grad" and "*lacht*" are left for [scrubEmphasis] or left alone.
func scrubLineStart(line string) string {
	rest := strings.TrimLeft(line, " \t")
	for {
		switch {
		case strings.HasPrefix(rest, "> "):
			rest = strings.TrimLeft(rest[1:], " \t")
			continue
		case strings.HasPrefix(rest, "#"):
			n := 0
			for n < len(rest) && rest[n] == '#' {
				n++
			}
			if n <= 6 && n < len(rest) && (rest[n] == ' ' || rest[n] == '\t') {
				return strings.TrimLeft(rest[n:], " \t")
			}
		case strings.HasPrefix(rest, "- "), strings.HasPrefix(rest, "* "), strings.HasPrefix(rest, "+ "):
			return strings.TrimLeft(rest[1:], " \t")
		case strings.HasPrefix(rest, "• "):
			return strings.TrimLeft(rest[len("•"):], " \t")
		}
		break
	}
	if len(rest) == len(strings.TrimLeft(line, " \t")) {
		return line // nothing stripped: keep the line byte-identical
	}
	return rest
}

// scrubEmphasis drops the runs of "*" and "_" that markdown would read as
// emphasis — those touching a word on exactly one side — and keeps the ones that
// are content (see [scrubSpoken]).
func scrubEmphasis(line string) string {
	if !strings.ContainsAny(line, "*_") {
		return line
	}
	runes := []rune(line)
	var b strings.Builder
	b.Grow(len(line))
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
		prev, next := ' ', ' '
		if i > 0 {
			prev = runes[i-1]
		}
		if j < len(runes) {
			next = runes[j]
		}
		wordBefore, wordAfter := isWordRune(prev), isWordRune(next)
		spaceBefore, spaceAfter := unicode.IsSpace(prev), unicode.IsSpace(next)
		keep := (wordBefore && wordAfter) || // intraword: math, names, identifiers
			(spaceBefore && spaceAfter && i > 0 && j < len(runes)) // a free-standing operator
		if keep {
			b.WriteString(string(runes[i:j]))
		}
		i = j
	}
	return b.String()
}

// isWordRune reports whether r is part of a word for the emphasis test.
func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }
