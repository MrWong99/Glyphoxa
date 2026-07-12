package agenttool

import "regexp"

// diceHardeningInstruction is appended to the system prompt on dice-armed turns
// (#399, Option B): cheap prompt hardening that tells the model to call the dice
// Tool and never narrate an invented result. Not airtight on its own — the
// post-hoc [claimsRollResult] guard is the enforcement backstop.
const diceHardeningInstruction = "When asked to roll dice, call the dice tool and report its actual result. Never invent, guess, or roleplay a die result."

// standaloneRollNumber matches a bare 1..100 integer standing on its own word
// boundaries — the numeric shape a narrated die result takes ("a 19", "eine 19",
// "a perfect 100"). A number outside 1..100 (a price, a year) mostly falls
// outside the range, but the detector is recall-biased on purpose (see
// [claimsRollResult]).
//
// Residual: the 1..100 cap misses a 3-digit invented result ("Ergebnis: 120") —
// a false NEGATIVE. This is deliberate: extending to arbitrary integers would
// false-POSITIVE on years, quantities, and gold amounts on the far more common
// non-roll turns, and the cap covers every single die (d100 is the largest
// standard die). Revisit if live traffic shows the model narrating multi-die SUMS
// above 100 (e.g. "3d100") without calling the tool.
var standaloneRollNumber = regexp.MustCompile(`\b([1-9][0-9]?|100)\b`)

// claimsRollResult reports whether text reads as a narrated die result (#399,
// Option C): explicit die notation ("d20", "2w6", "d%") is stripped first — those
// are the request, not a result — and what remains is checked for a standalone
// 1..100 integer. Language-free and recall-biased: a false positive only costs one
// regeneration on a turn where the dice Tool was already armed, whereas a false
// negative would ship an invented number, so the detector leans toward firing.
func claimsRollResult(text string) bool {
	stripped := diceNotation.ReplaceAllString(text, " ")
	return standaloneRollNumber.MatchString(stripped)
}
