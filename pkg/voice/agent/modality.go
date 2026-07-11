package agent

import "strings"

// textLengthThreshold is the answer-length (in runes) above which a Butler reply
// defaults to text delivery (#297 decision 2): short results (a dice roll, a
// single fact) are spoken, long ones (search hit lists, recaps) are posted to the
// channel chat so the table can read them back.
const textLengthThreshold = 400

// textRequestPhrases force text delivery when they appear in the addressed
// utterance ("post that as text", "in chat"). EN + DE (#297 decision 2: the
// request can override the size default).
var textRequestPhrases = []string{"as text", "in chat", "post it", "als text", "im chat"}

// voiceRequestPhrases force spoken delivery when they appear in the addressed
// utterance ("tell us in voice", "read it aloud", "sag es"). EN + DE.
var voiceRequestPhrases = []string{"in voice", "aloud", "say it", "sag es"}

// AnswerAsText decides whether the Butler's answer to utterance is delivered as
// text (true) or spoken through its Voice (false), per #297 decision 2:
//
//   - A voiceless Butler (no configured Voice) can only answer as text.
//   - Otherwise an explicit modality request in the utterance wins: a
//     text-forcing phrase → text, a voice-forcing phrase → voice.
//   - Otherwise size decides: an answer longer than [textLengthThreshold] runes
//     posts as text, a short one is spoken.
//
// The utterance is matched case-insensitively. A voiceless Butler always returns
// text, even against a voice-forcing phrase — it has no Voice to honor it with.
func AnswerAsText(utterance, answer string, voiceless bool) bool {
	if voiceless {
		return true
	}
	u := strings.ToLower(utterance)
	if containsAny(u, textRequestPhrases) {
		return true
	}
	if containsAny(u, voiceRequestPhrases) {
		return false
	}
	return len([]rune(answer)) > textLengthThreshold
}

// containsAny reports whether s contains any of the phrases.
func containsAny(s string, phrases []string) bool {
	for _, p := range phrases {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}
