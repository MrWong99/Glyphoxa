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
		{"three-digit non-roll number ignored", "Es kostet 250 Gold.", false},
		{"hundred is a valid result", "A perfect 100 on the nose.", true},

		// --- O5: a digit followed by a currency/time/distance noun is a quantity. ---
		{"de price", "Das macht 20 Goldstücke.", false},
		{"de copper", "Das macht 5 Kupferstücke, mein Freund.", false},
		{"de copper ascii", "Das macht 5 Kupferstuecke, mein Freund.", false},
		{"de days ago", "Vor 3 Tagen kam ein Fremder vorbei.", false},
		{"de eine N noun", "Ich habe eine 3 Tage alte Karte.", false},
		{"en years ago", "He died 40 years ago, traveler.", false},
		{"en a N noun", "It's a 5 gold coin.", false},
		{"de feet", "Die Brücke ist 30 Fuß lang.", false},

		// --- Every other bare 1..100 integer stays a claim (armed turns only). ---
		{"de initiative", "Meine Initiative ist 15.", true},
		{"en bare exclaim", "17! You hit.", true},
		{"en got", "I got 17.", true},
		{"en hits with", "Your attack hits with 18.", true},
		{"en to hit", "He attacks you: 18 to hit.", true},
		{"de crit", "Kritischer Treffer, 20!", true},
		{"de geschafft", "Geschafft mit 19.", true},
		{"de komme auf", "Ich komme auf 23.", true},
		{"de damage", "Der Angriff trifft: 12 Schaden.", true},
		{"en damage", "The attack hits for 12 damage.", true},
		{"de AC", "Du triffst mit 18 gegen seine Rüstungsklasse.", true},
		{"far from cue", "After a long and very tense moment of hesitation in the dark old hall, she finally manages a 14.", true},
		{"en rolled N", "You rolled 19.", true},
		{"en total", "That's 3 and 4, 7 total!", true},
		{"en solid N", "Trust me, a solid 17.", true},
		{"de gewürfelt", "Du hast eine 17 gewürfelt.", true},
		{"de N gewürfelt", "Du hast 17 gewürfelt.", true},
		{"de Ergebnis", "Ergebnis: 14.", true},
		{"de Würfel zeigt", "Der Würfel zeigt eine 6.", true},
		{"en notation then result", "I roll the d20 and get 12.", true},

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
			if got := claimsRollResult(tc.text, ""); got != tc.want {
				t.Errorf("claimsRollResult(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestClaimsRollResult_QuantityRoll pins #690: the unit-noun carve-out (O5) holds
// for an incidental quantity, but NOT when the armed user utterance asked for a
// roll of that same unit — then "12 Goldstücke" is the roll's result. The unit is
// matched at a word start in the utterance, and only the same unit unlocks.
func TestClaimsRollResult_QuantityRoll(t *testing.T) {
	const goldDE = "Würfel, wie viel Gold wir finden"
	cases := []struct {
		name string
		text string
		user string
		want bool
	}{
		{"price asked without a roll", "Das macht 5 Kupferstücke", "Was kostet das Bier?", false},
		{"price on an armed but unrelated utterance", "Das macht 5 Kupferstücke", "Würfel bitte Initiative", false},
		{"de quantity roll", "Du findest 12 Goldstücke", goldDE, true},
		{"en quantity roll", "You find 12 gold pieces", "Roll for how much gold we find", true},
		{"user unit in a compound", "Du findest 12 Goldstücke", "Würfel, wie viele Goldmünzen wir finden", true},
		{"en time roll", "It takes 3 days", "Roll how many days the journey takes", true},
		{"unit mismatch stays carved out", "Nach 3 Tagen findet ihr es", goldDE, false},
		{"word start, not substring", "Es dauert 3 Tage", "Lass uns am Montag würfeln", false},
		{"non-unit number still claims", "Meine Initiative ist 15.", goldDE, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := claimsRollResult(tc.text, tc.user); got != tc.want {
				t.Errorf("claimsRollResult(%q, %q) = %v, want %v", tc.text, tc.user, got, tc.want)
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
		user string
		want []int
	}{
		{"Ah, eine 19!", "", []int{19}},
		{"You rolled a natural twenty!", "", []int{20}},
		{"Du hast eine Dreißig gewürfelt.", "", []int{30}},
		{"I roll the d20... a 7! With +3 that's 10.", "", []int{7, 3, 10}},
		{"Let me roll a d20 for you.", "", nil},
		{"The bones are silent.", "", nil},
		// #690: a quantity the user asked a roll of is a claimed value.
		{"Du findest 12 Goldstücke", "", nil},
		{"Du findest 12 Goldstücke", "Würfel, wie viel Gold wir finden", []int{12}},
		{"Nach 3 Tagen findet ihr 12 Goldstücke", "Würfel, wie viel Gold wir finden", []int{12}},
	}
	for _, tc := range cases {
		if got := claimedRollValues(tc.text, tc.user); !slices.Equal(got, tc.want) {
			t.Errorf("claimedRollValues(%q, %q) = %v, want %v", tc.text, tc.user, got, tc.want)
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
		user    string
		results []string
		want    bool
	}{
		{"claim matches the single roll", "The bones show a 16, traveler.", "", []string{"Rolled 1d20: 16."}, true},
		{"claim contradicts the roll", "A perfect 20!", "", []string{"Rolled 1d20: 7."}, false},
		{"spelled claim contradicts", "You rolled a natural twenty!", "", []string{"Rolled 1d20: 7."}, false},
		{"spelled claim matches", "You rolled a natural twenty!", "", []string{"Rolled 1d20: 20."}, true},
		{"claim with no tool result at all", "Trust me, a solid 17.", "", nil, false},
		{"no claim is trivially consistent", "The fates stay silent tonight.", "", nil, true},
		{"derived total counts as consistent", "That's 3 and 4 — 7 total!", "", []string{"Rolled 2d6: [3 4] (total 7)."}, true},
		{"notation in the result never legitimizes", "A 20, I swear!", "", []string{"Rolled 1d20: 7."}, false},
		// A forced regen contradicting the roll in a cue-less shape must NOT pass
		// as "no claim" (O5 review).
		{"cue-less contradiction", "Kritischer Treffer, 20!", "", []string{"Rolled 1d20: 7."}, false},
		{"cue-less contradiction en", "17! You hit.", "", []string{"Rolled 1d20: 4."}, false},
		{"cue-less match", "Ich komme auf 23.", "", []string{"Rolled 1d20+5: 18 (total 23)."}, true},
		{"price beside the real roll", "A 7. That'll be 5 gold.", "", []string{"Rolled 1d20: 7."}, true},
		// #690: on a quantity roll the unit-suffixed number is the claim.
		{"quantity roll contradicts", "Du findest 12 Goldstücke", "Würfel, wie viel Gold wir finden", []string{"Rolled 1d20: 7."}, false},
		{"quantity roll matches", "Du findest 12 Goldstücke", "Würfel, wie viel Gold wir finden", []string{"Rolled 1d20: 12."}, true},
		{"quantity without the user naming it", "Du findest 12 Goldstücke", "", []string{"Rolled 1d20: 7."}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rollClaimConsistent(tc.text, tc.user, tc.results); got != tc.want {
				t.Errorf("rollClaimConsistent(%q, %q, %v) = %v, want %v", tc.text, tc.user, tc.results, got, tc.want)
			}
		})
	}
}
