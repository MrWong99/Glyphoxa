package voicetest_test

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

func TestNormalizeTranscript(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"lowercases", "Glyphoxa ROLL", "glyphoxa roll"},
		{"drops trailing period", "roll a perception check for me.", "roll a perception check for me"},
		{"drops interior punctuation", "Bart, what's the special tonight?", "bart whats the special tonight"},
		{"collapses whitespace", "  roll   a\tcheck\n", "roll a check"},
		{
			// The exact drift that broke the hello-test assertion: scribe_v2
			// returned the utterance without its trailing period.
			name: "period-only difference is equal after normalization",
			in:   "Glyphoxa, roll a perception check for me",
			want: voicetest.NormalizeTranscript("Glyphoxa, roll a perception check for me."),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := voicetest.NormalizeTranscript(tc.in); got != tc.want {
				t.Errorf("NormalizeTranscript(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
