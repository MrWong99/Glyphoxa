// Package textnorm holds the single text-normalisation used across Glyphoxa to
// compare free text for equivalence: lower case, punctuation stripped, whitespace
// collapsed to single spaces and trimmed. It is a stdlib-only leaf so any package
// can depend on it without risking an import cycle (the ADR-0042 speculation cache
// in internal/recall and the #411 write-time proposal dedup in pkg/tool share it).
package textnorm

import (
	"strings"
	"unicode"
)

// Normalize folds a string to its comparison form: lower case, punctuation
// stripped, whitespace collapsed to single spaces and trimmed. Punctuation acts
// as a soft word boundary — "well,now" and "well now" both normalize to
// "well now" — but a space is only emitted when a word actually follows, so
// trailing punctuation and edge whitespace never leak into the result.
func Normalize(s string) string {
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
