package voicetest

import (
	"strings"
	"unicode"
)

// NormalizeTranscript folds a transcript to a canonical form for tolerant
// equality in tests: it lowercases, drops every rune that is not a letter or
// number, and collapses runs of whitespace to single spaces.
//
// STT assertions compare the words a recognizer produced against an expected
// utterance. Pinning the exact string couples the test to provider-specific
// casing, punctuation, and spacing — e.g. ElevenLabs scribe_v2 transcribes
// the hello-test clip without a trailing period. Comparing normalized forms
// asserts the recognizer got the words right while tolerating that cosmetic
// variation, so a re-recorded cassette (ADR-0021) only fails the test when the
// transcribed words genuinely change.
func NormalizeTranscript(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsNumber(r):
			b.WriteRune(r)
		case unicode.IsSpace(r):
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
