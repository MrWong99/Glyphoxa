package recall

import (
	"strings"
	"unicode"
)

// normalize folds an utterance to its speculation-cache comparison form: lower
// case, punctuation stripped, whitespace collapsed to single spaces and trimmed.
// It is the self-heal key of ADR-0042 — the [STTFinal] text is normalized and
// matched against the normalized partial a speculation prefetched on, so casing,
// trailing punctuation, and interim-whitespace jitter between the partial and the
// final do not defeat the cache. Deliberately a tiny local function: the recall
// path must not import the voicetest helpers.
func normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	pendingSpace := false
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			if pendingSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			pendingSpace = false
			b.WriteRune(unicode.ToLower(r))
		case unicode.IsSpace(r):
			pendingSpace = true
		default:
			// Punctuation is dropped without joining the surrounding words: it acts
			// as a soft boundary so "well,now" and "well now" both normalize to
			// "well now", but the space is only emitted if a word actually follows.
			pendingSpace = true
		}
	}
	return b.String()
}
