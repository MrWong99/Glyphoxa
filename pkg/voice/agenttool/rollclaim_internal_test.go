package agenttool

import "testing"

// TestClaimsRollResult pins the recall-biased invented-roll detector (#399): after
// stripping explicit die notation (so "W20"/"d20" tokens do not count as a claimed
// result), a standalone 1..100 integer in the reply reads as a narrated roll result.
// The detector is language-free and deliberately over-triggers — a false positive
// only costs one regeneration on a turn where dice was already armed.
func TestClaimsRollResult(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{"bare result number", "Ah, eine 19! Du hast einen scharfen Blick.", true},
		{"notation stripped leaves no number", "Ich würfle den W20 für dich.", false},
		{"english notation stripped", "Let me roll a d20 for you.", false},
		{"no numbers at all", "The bones are silent tonight, traveler.", false},
		{"recall bias: non-roll number still fires", "Das macht 20 Goldstücke.", true},
		{"three-digit non-roll number ignored", "Es kostet 250 Gold.", false},
		{"hundred is a valid result", "A perfect 100 on the nose.", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claimsRollResult(tc.text); got != tc.want {
				t.Errorf("claimsRollResult(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}
