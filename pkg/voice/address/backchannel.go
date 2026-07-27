package address

import "strings"

// Backchannel filler: the non-lexical noises a listener makes while somebody
// else holds the floor ("mhm", "äh", "aha"). They are speech, they transcribe to
// real tokens, and under the ambient heuristics they used to open a full
// LLM+TTS turn each — in a four-player session that multiplied the reply rate
// several times over for utterances that addressed nobody (ADR-0024 substance
// gate, 2026-07-27).
//
// The vocabulary holds ONLY vocalizations — sounds that cannot be the content of
// an answer. Lexical affirmatives and negatives ("ja", "nein", "yes", "no",
// "okay", "genau", "sure") are deliberately EXCLUDED even though they are the
// most frequent backchannel tokens in a real recording, because they are also
// the most frequent shape of a genuine reply: a Character NPC asks "Willst du
// den Schlüssel?" and the whole answer is "Ja." The matcher holds no state about
// whether the addressed Agent just asked a question, so it cannot tell the two
// apart — and when it cannot tell, the safe failure is an extra NPC line, not
// dead air after a direct answer.
//
// Scope: the gate suppresses AMBIENT routing only. An utterance carrying a name
// is an explicit address and never reaches it, so "Bart!" and "ja, Bart" still
// route (see [Matcher.match]). Transcription is untouched — the utterance stays
// on the bus, in the Transcript, and in the recency word buffer, exactly like
// the empty-final guard's contract.
var backchannelWords = map[string]map[string]struct{}{
	"en": wordSet(
		"mhm", "mm", "mmhm", "hm", "hmm", "uh", "uhhuh", "huh", "um", "erm",
		"oh", "ah", "aha", "wow",
	),
	"de": wordSet(
		"mhm", "mm", "mmh", "hm", "hmm", "äh", "ähm", "ah", "aha", "ach", "achso",
		"naja", "oh", "hä", "wow",
	),
}

// backchannelPhrases are multi-token backchannels whose individual tokens are
// NOT filler on their own. "Ach so." is the canonical one: written as two words
// it tokenizes to [ach so], and "so" alone is an ordinary German discourse
// opener ("So, weiter geht's") that must keep routing.
var backchannelPhrases = map[string]map[string]struct{}{
	"de": {"ach so": {}, "na ja": {}, "ach ja": {}},
	"en": {"uh huh": {}, "oh well": {}},
}

// wordSet builds a lookup set from tokenized forms, so the vocabulary is
// compared in exactly the shape [tokenize] produces (lowercased, punctuation
// stripped).
func wordSet(words ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(words))
	for _, w := range words {
		for _, tok := range tokenize(w) {
			set[tok] = struct{}{}
		}
	}
	return set
}

// isBackchannel reports whether the utterance is nothing but backchannel: a
// known multi-token phrase, or every token a filler vocalization in lang. An
// utterance with no tokens is NOT a backchannel — the empty-final guard upstream
// already owns that case.
//
// An unknown or empty language tag falls back to the English vocabulary (the
// matcher's own encoder fallback), but a language WITH a list consults only its
// own: cross-consulting made a German session drop "Er." and "Um." — a pronoun
// and a preposition — because they are English filler.
//
// The all-tokens rule is what keeps the gate safe: "mhm" and "mhm mhm" are
// filler, while "mhm, der Turm brennt" is not, because one content word makes
// the utterance a request.
func isBackchannel(words []string, lang string) bool {
	if len(words) == 0 {
		return false
	}
	tag := normalizeLang(lang)
	set, known := backchannelWords[tag]
	if !known {
		tag, set = "en", backchannelWords["en"]
	}
	if _, ok := backchannelPhrases[tag][strings.Join(words, " ")]; ok {
		return true
	}
	for _, w := range words {
		if _, ok := set[w]; !ok {
			return false
		}
	}
	return true
}
