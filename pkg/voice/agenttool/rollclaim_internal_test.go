package agenttool

import (
	"slices"
	"testing"
)

// TestClaimsRollResult pins the recall-biased invented-roll detector (#399): after
// stripping explicit die notation (so "W20"/"d20" tokens do not count as a claimed
// result), a standalone 1..100 integer in the reply reads as a narrated roll result.
// The detector is language-free and deliberately over-triggers — a false positive
// only costs one regeneration on a turn where dice was already armed.
//
// #438 extends it to SPELLED-OUT numbers (EN + DE, 1–20 plus the tens up to 100),
// but only in roll-claim contexts (a roll verb, "natural"/"nat", a German article
// or würfeln/Wurf cue) — unlike the digit form, a bare number word is common prose
// ("twenty paces"), so the spelled form carries a precision guard.
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

		// --- #438: spelled-out numbers in claim contexts (EN). ---
		{"en natural twenty", "You rolled a natural twenty!", true},
		{"en natural without roll verb", "A natural twenty! Incredible.", true},
		{"en nat twenty", "Nat twenty, well done.", true},
		{"en rolled a seven", "He rolled a seven, alas.", true},
		{"en rolls a three", "The die rolls a three.", true},
		{"en roll of ninety", "A roll of ninety on the percentile dice.", true},
		{"en rolled a one hundred", "You rolled a one hundred!", true},

		// --- #438: spelled-out numbers in claim contexts (DE). ---
		{"de eine Zwanzig", "Ah, eine Zwanzig! Du hast Glück.", true},
		{"de natürliche Zwanzig", "Eine natürliche Zwanzig!", true},
		{"de eine Dreißig gewürfelt", "Du hast eine Dreißig gewürfelt.", true},
		{"de gewürfelt: siebzehn", "Gewürfelt: siebzehn.", true},
		{"de eine Eins", "Oh nein, eine Eins.", true},

		// --- #438: precision guard — plain narrative number words do NOT fire. ---
		{"en twenty paces", "You walk twenty paces down the corridor.", false},
		{"en seven travelers", "Seven travelers sit by the fire.", false},
		{"de zwanzig Schritte", "Du gehst zwanzig Schritte weiter.", false},
		{"de vier Gäste", "Vier Gäste sitzen am Feuer.", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claimsRollResult(tc.text); got != tc.want {
				t.Errorf("claimsRollResult(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestClaimedRollValues pins the VALUE extraction the #438 regen-consistency check
// compares against the dice Tool's actual result: digit claims parse to their
// integer, spelled-out claims map through the number-word tables, and die notation
// never contributes a value.
func TestClaimedRollValues(t *testing.T) {
	cases := []struct {
		text string
		want []int
	}{
		{"Ah, eine 19!", []int{19}},
		{"You rolled a natural twenty!", []int{20}},
		{"Du hast eine Dreißig gewürfelt.", []int{30}},
		{"I roll the d20... a 7! With +3 that's 10.", []int{7, 3, 10}},
		{"Let me roll a d20 for you.", nil},
		{"The bones are silent.", nil},
	}
	for _, tc := range cases {
		if got := claimedRollValues(tc.text); !slices.Equal(got, tc.want) {
			t.Errorf("claimedRollValues(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

// TestRollClaimConsistent pins the #438 regen-verification rule: a reply claiming
// no value is trivially consistent; a claimed value is consistent when it matches
// ANY number the dice Tool actually reported (rolls or totals — narration mixes
// the roll with derived numbers); a claim with NO matching Tool number — including
// no Tool result at all — contradicts.
func TestRollClaimConsistent(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		results []string
		want    bool
	}{
		{"claim matches the single roll", "The bones show a 16, traveler.", []string{"Rolled 1d20: 16."}, true},
		{"claim contradicts the roll", "A perfect 20!", []string{"Rolled 1d20: 7."}, false},
		{"spelled claim contradicts", "You rolled a natural twenty!", []string{"Rolled 1d20: 7."}, false},
		{"spelled claim matches", "You rolled a natural twenty!", []string{"Rolled 1d20: 20."}, true},
		{"claim with no tool result at all", "Trust me, a solid 17.", nil, false},
		{"no claim is trivially consistent", "The fates stay silent tonight.", nil, true},
		{"derived total counts as consistent", "That's 3 and 4 — 7 total!", []string{"Rolled 2d6: [3 4] (total 7)."}, true},
		{"notation in the result never legitimizes", "A 20, I swear!", []string{"Rolled 1d20: 7."}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rollClaimConsistent(tc.text, tc.results); got != tc.want {
				t.Errorf("rollClaimConsistent(%q, %v) = %v, want %v", tc.text, tc.results, got, tc.want)
			}
		})
	}
}
