package agent

import (
	"strings"
	"testing"
)

func TestAnswerAsText(t *testing.T) {
	long := strings.Repeat("a", 401)
	short := "Two sixes: 9."

	cases := []struct {
		name      string
		utterance string
		answer    string
		voiceless bool
		want      bool
	}{
		{"voiceless forces text even for a short answer", "Glyphoxa, roll a d6", short, true, true},
		{"short answer speaks by default", "Glyphoxa, roll two d6", short, false, false},
		{"long answer posts as text by default", "Glyphoxa, what happened last session?", long, false, true},
		{"explicit 'as text' overrides a short answer", "Glyphoxa, give it to me as text", short, false, true},
		{"explicit 'in chat' overrides a short answer", "Glyphoxa, post that in chat", short, false, true},
		{"explicit 'post it' overrides a short answer", "Glyphoxa, post it for me", short, false, true},
		{"explicit 'in voice' overrides a long answer", "Glyphoxa, tell us in voice", long, false, false},
		{"explicit 'aloud' overrides a long answer", "Glyphoxa, read it aloud", long, false, false},
		{"explicit 'say it' overrides a long answer", "Glyphoxa, just say it", long, false, false},
		{"German 'sag es' overrides a long answer", "Glyphoxa, sag es uns", long, false, false},
		{"German 'als text' forces text on a short answer", "Glyphoxa, gib es mir als text", short, false, true},
		{"voiceless wins even with a voice keyword", "Glyphoxa, say it aloud", short, true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AnswerAsText(tc.utterance, tc.answer, tc.voiceless); got != tc.want {
				t.Errorf("AnswerAsText(%q, len=%d, voiceless=%v) = %v, want %v",
					tc.utterance, len([]rune(tc.answer)), tc.voiceless, got, tc.want)
			}
		})
	}
}

// TestAnswerAsText_PhrasesMatchWholeWords pins O10: a modality phrase must match
// as words, not as a raw substring — "im Chateau" contains "im chat" and used to
// force a spoken answer into the channel chat.
func TestAnswerAsText_PhrasesMatchWholeWords(t *testing.T) {
	short := "Sure."
	for _, utterance := range []string{
		"Was gibt es im Chateau zu sehen?",
		"Bart, is the task textures thing done?",
		"Wer wohnt im Chatelet?",
		"Did you repost italics again?",
	} {
		if AnswerAsText(utterance, short, false) {
			t.Errorf("AnswerAsText(%q) = text, want voice (no whole-word text phrase)", utterance)
		}
	}
	long := strings.Repeat("x", textLengthThreshold+1)
	for _, utterance := range []string{
		"Is the door allowed to be opened?", // "aloud" is not in "allowed", but pin the neighbours
		"Sag essen ist fertig.",             // contains "sag es" only as a substring
		"He would say item three.",          // contains "say it" only as a substring
	} {
		if !AnswerAsText(utterance, long, false) {
			t.Errorf("AnswerAsText(%q, long) = voice, want text (no whole-word voice phrase)", utterance)
		}
	}
	// The real requests still win, with any punctuation around them.
	for _, utterance := range []string{"Schreib das im Chat!", "Post it, please.", "Antworte bitte als Text."} {
		if !AnswerAsText(utterance, short, false) {
			t.Errorf("AnswerAsText(%q) = voice, want text", utterance)
		}
	}
	for _, utterance := range []string{"Sag es!", "Read it aloud, please.", "Tell us in voice."} {
		if AnswerAsText(utterance, long, false) {
			t.Errorf("AnswerAsText(%q, long) = text, want voice", utterance)
		}
	}
}
