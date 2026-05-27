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

func TestWordsMatch(t *testing.T) {
	cases := []struct {
		name         string
		want, actual string
		minRatio     float64
		match        bool
	}{
		{"identical at full ratio", "roll a perception check", "roll a perception check", 1.0, true},
		{"both empty", "", "", 1.0, true},
		{"empty actual fails", "roll a check", "", 0.7, false},
		{"half overlap below threshold", "a b c d", "a b x y", 0.7, false},
		{"half overlap meets lower threshold", "a b c d", "a b x y", 0.5, true},
		// Duplicates count as a multiset: "the" matches once, not twice, so
		// shared=2 over the longer side (4 words) = 0.5, under 0.7.
		{"duplicate words counted by multiset", "the cat the dog", "the dog", 0.7, false},
		{"unrelated text fails", "the quick brown fox", "completely different words here", 0.7, false},
		{
			// Real scribe_v2 drift on the German TTRPG intro: proper-noun
			// spelling (glyphoxa→glyphoxer), an interjected filler (äh), and a
			// compound split (raus gekommen→rausgekommen). ~90% of words
			// survive, comfortably past 0.7.
			name:     "german intro drift within tolerance",
			want:     "hallo zusammen dann lasst uns doch mal die heutige session beginnen okay glyphoxa butler wiederhol doch einfach einmal bitte was letzte session so passiert ist was mach ne kurze zusammenfassung und ja wo sind wir am ende bei raus gekommen",
			actual:   "hallo zusammen dann lasst uns doch mal die heutige session beginnen hey glyphoxer butler wiederhol doch einfach einmal bitte was letzte session so passiert ist äh was mach ne kurze zusammenfassung und ja wo sind wir am ende bei rausgekommen",
			minRatio: 0.7,
			match:    true,
		},
		{
			// Real scribe_v2 drift on the English TTRPG intro: glyphoxa→
			// "glyphox or", an inserted "s", happend→happened.
			name:     "english intro drift within tolerance",
			want:     "hey everyone so lets start our session for today okay glyphoxa butler can you give us a quick intro what happend last session and what did we do where did we leave the session whats the current status",
			actual:   "hey everyone so lets start our session for today okay glyphox or butler can you s give us a quick intro what happened last session and what did we do where did we leave the session whats the current status",
			minRatio: 0.7,
			match:    true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := voicetest.WordsMatch(tc.want, tc.actual, tc.minRatio); got != tc.match {
				t.Errorf("WordsMatch(%q, %q, %v) = %v, want %v", tc.want, tc.actual, tc.minRatio, got, tc.match)
			}
		})
	}
}
