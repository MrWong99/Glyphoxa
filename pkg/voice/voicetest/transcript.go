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

// WordsMatch reports whether want and actual overlap by at least minRatio,
// comparing them as whitespace-separated bags of words rather than as exact
// strings.
//
// It is the tolerant counterpart to string equality for asserting STT output
// on long, natural-voice utterances. Even after [NormalizeTranscript] a real
// recognizer drifts on individual words — proper-noun spellings ("glyphoxa" →
// "glyphoxer"), interjected fillers ("äh"), or compound splits ("raus
// gekommen" → "rausgekommen") — that exact equality flags as failures.
// Pinning the fraction of words that survive keeps the assertion sensitive to
// a genuinely wrong transcription while tolerating that cosmetic drift.
//
// The score is the multiset intersection — for each distinct word, the lesser
// of its occurrence counts in want and actual — divided by the word count of
// the longer side. Words missing from either transcript and words inserted
// into either therefore lower the score symmetrically; word order is not
// considered. Two empty inputs match. Both inputs are expected to already be
// normalized (see [NormalizeTranscript]).
func WordsMatch(want, actual string, minRatio float64) bool {
	wantWords := strings.Fields(want)
	actualWords := strings.Fields(actual)
	if len(wantWords) == 0 && len(actualWords) == 0 {
		return true
	}

	remaining := make(map[string]int, len(wantWords))
	for _, w := range wantWords {
		remaining[w]++
	}
	shared := 0
	for _, w := range actualWords {
		if remaining[w] > 0 {
			remaining[w]--
			shared++
		}
	}

	denom := max(len(wantWords), len(actualWords))
	return float64(shared)/float64(denom) >= minRatio
}
