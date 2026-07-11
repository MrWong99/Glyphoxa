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
