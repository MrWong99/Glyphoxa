package agent

import "strings"

// selfPrefixWrappers are the decorations a model puts around a speaker label
// when it imitates the transcript's "Name: text" grammar — markdown emphasis,
// quotes, brackets. They are stripped symmetrically around the name so
// "**Mira**:", "[Mira]:" and "*Mira:*" are all recognized as the same mistake.
var selfPrefixWrappers = map[rune]rune{
	'*':  '*',
	'_':  '_',
	'"':  '"',
	'\'': '\'',
	'`':  '`',
	'[':  ']',
	'(':  ')',
	'<':  '>',
	'{':  '}',
}

// stripSelfPrefix removes a leading "<name>:" speaker label from one of the
// Agent's own replies (#528 follow-up, live finding 2026-07-27).
//
// Hot Context labels every human line "Name: text" so the Agent can tell its
// players apart (see [Replier.userLine] and the "## Who is speaking" prompt
// block). Models imitate the format they are shown: they answer "Mira: Good
// evening" — and since the whole reply is read aloud verbatim, the NPC says its
// own name before every line. Left alone it compounds, because the delivered
// text is what gets committed to history and chunked into the Transcript, so the
// next turn's context contains an ASSISTANT line in exactly that shape and the
// model copies itself.
//
// The gate is deliberately narrow: only the Agent's OWN name (case-insensitively,
// optionally wrapped per [selfPrefixWrappers]) immediately followed by a colon is
// removed. A generic "<word>:" rule would mangle real speech — "Listen: the
// bridge is out", "Achtung: Fallen" — and a bracketed audio tag ("[laughs] Hello",
// ADR-0022) is never touched because a tag is not the Agent's name. An empty name
// disables the strip entirely, which keeps every caller that has no display name
// byte-identical.
//
// Repetition is handled ("Mira: Mira: hi"), because a model that makes the
// mistake once often makes it twice on the same line.
func stripSelfPrefix(name, text string) string {
	if name == "" {
		return text
	}
	for {
		stripped, ok := cutSelfPrefix(name, text)
		if !ok {
			break
		}
		text = stripped
	}
	// The label may sit BEHIND a leading audio tag ("[laughs] Mira: Good
	// evening"): the TTS markup instruction (ADR-0022) actively teaches the model
	// to open with one, and a tag is not part of what the Agent says, so it must
	// not shield the label. Only ONE leading tag is skipped, and the tag is kept —
	// the reply is exactly what it would have been without the mistake.
	if tag, rest, ok := cutLeadingTag(text); ok {
		if stripped := stripSelfPrefix(name, rest); stripped != rest {
			return tag + " " + stripped
		}
	}
	return text
}

// cutLeadingTag splits a leading bracketed audio tag ("[laughs] hello") into the
// tag and the remainder. It reports false when the text does not open with a
// closed, non-empty tag — including the "[Mira]:" case, where the bracket is a
// wrapper around the speaker label rather than a tag, and which
// [cutSelfPrefix] has already had its chance at.
func cutLeadingTag(text string) (tag, rest string, ok bool) {
	trimmed := strings.TrimLeftFunc(text, isLabelSpace)
	if !strings.HasPrefix(trimmed, "[") {
		return "", "", false
	}
	end := strings.IndexByte(trimmed, ']')
	if end <= 1 {
		return "", "", false
	}
	return trimmed[:end+1], strings.TrimLeftFunc(trimmed[end+1:], isLabelSpace), true
}

// cutSelfPrefix removes ONE leading "<name>:" label from text, reporting whether
// it found one. Everything it consumes is leading whitespace, the (optionally
// wrapped) name, and the colon plus the whitespace after it — never a byte of
// the reply itself.
func cutSelfPrefix(name, text string) (string, bool) {
	rest := strings.TrimLeftFunc(text, isLabelSpace)

	// An optional opening wrapper, remembered so its closer can be consumed after
	// the name. A wrapper is doubled for markdown bold ("**Mira**"), so consume a
	// run of the same rune rather than a single one.
	var closer rune
	var openRun int
	if r := firstRune(rest); r != 0 {
		if c, wrapped := selfPrefixWrappers[r]; wrapped {
			closer, openRun = c, 0
			for firstRune(rest) == r {
				rest = rest[len(string(r)):]
				openRun++
			}
		}
	}

	if !hasPrefixFold(rest, name) {
		return text, false
	}
	rest = rest[len(name):]

	// The closing wrapper, if the name was opened with one. It is optional: a model
	// writing "*Mira: ..." (never closed) still made the same mistake, and the
	// colon below is what actually confirms the label.
	if closer != 0 {
		for i := 0; i < openRun && firstRune(rest) == closer; i++ {
			rest = rest[len(string(closer)):]
		}
	}
	rest = strings.TrimLeftFunc(rest, isLabelSpace)

	// The colon is what makes this a speaker label rather than a reply that simply
	// opens with the Agent's own name ("Mira would never do that").
	if !strings.HasPrefix(rest, ":") && !strings.HasPrefix(rest, "：") {
		return text, false
	}
	rest = strings.TrimPrefix(strings.TrimPrefix(rest, ":"), "：")

	// A closing wrapper AFTER the colon covers "*Mira:*".
	if closer != 0 {
		rest = strings.TrimLeftFunc(rest, func(r rune) bool { return r == closer })
	}
	return strings.TrimLeftFunc(rest, isLabelSpace), true
}

// hasPrefixFold reports whether s begins with prefix, compared
// case-insensitively. It compares byte length first so the caller can slice s by
// len(prefix): the names this runs against are the same string on both sides
// (the Agent's configured name against the model's echo of it), and a fold that
// changed length would make that slice wrong.
func hasPrefixFold(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return strings.EqualFold(s[:len(prefix)], prefix)
}

// firstRune returns the first rune of s, or 0 when s is empty.
func firstRune(s string) rune {
	for _, r := range s {
		return r
	}
	return 0
}

// isLabelSpace reports whether r is whitespace the label may be padded with. It
// is deliberately wider than the sentence splitter's isSpace — a model writing
// a label may pad it with a non-breaking space — and narrow enough that nothing
// a reply would actually voice is consumed.
func isLabelSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\r', '\v', '\f', ' ':
		return true
	}
	return false
}
