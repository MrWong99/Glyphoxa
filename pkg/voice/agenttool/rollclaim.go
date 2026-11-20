package agenttool

import (
	"regexp"
	"strconv"
	"strings"
)

// diceHardeningInstructions hold the instruction appended to the system prompt
// on dice-armed turns (#399, Option B), keyed by gate language: cheap prompt
// hardening that tells the model to call the dice Tool and never narrate an
// invented result. Not airtight on its own — the post-hoc [claimsRollResult]
// guard is the enforcement backstop. It is written in the Campaign Language so
// a German campaign's system prompt does not switch to English mid-prompt
// (the language the model then tends to answer in); the text is fixed per
// session, so an armed turn's prompt prefix stays cacheable. "dice" is the
// Tool's name and stays untranslated.
var diceHardeningInstructions = map[string]string{
	"en": "When asked to roll dice, call the dice tool and report its actual result. Never invent, guess, or roleplay a die result.",
	"de": "Wenn du gebeten wirst zu würfeln, rufe das Tool dice auf und nenne sein tatsächliches Ergebnis. Erfinde, rate oder spiele niemals ein Würfelergebnis vor.",
}

// diceHardeningInstruction returns the hardening instruction for a gate
// language ([gateLanguage]), falling back to English like the keyword gate.
func diceHardeningInstruction(lang string) string {
	if s, ok := diceHardeningInstructions[lang]; ok {
		return s
	}
	return diceHardeningInstructions["en"]
}

// enNumberWord / deNumberWord are the spelled-out number vocabularies the #438
// spelled-claim patterns capture: 1–20 plus the tens up to 100, in the two
// campaign languages currently supported. Longer alternatives come first so the
// capture prefers "fourteen" over "four" and "one hundred" over "one".
const (
	enNumberWord = `one\s+hundred|hundred|seventeen|thirteen|fourteen|eighteen|nineteen|sixteen|fifteen|seventy|twenty|thirty|eleven|twelve|eighty|ninety|forty|fifty|sixty|three|seven|eight|four|five|nine|one|two|six|ten`
	deNumberWord = `einhundert|hundert|siebzehn|dreizehn|vierzehn|fünfzehn|sechzehn|achtzehn|neunzehn|dreißig|dreissig|siebzig|zwanzig|vierzig|fünfzig|sechzig|achtzig|neunzig|sieben|zwölf|zwei|drei|vier|fünf|sechs|acht|neun|zehn|eins|elf`
)

// spelledRollClaims match a spelled-out number word IN A ROLL-CLAIM CONTEXT
// (#438): "you rolled a natural twenty", "a roll of ninety", "eine Zwanzig",
// "gewürfelt: siebzehn". Unlike the digit form, a bare number word is everyday
// prose ("twenty paces", "zwanzig Schritte"), so the spelled detector requires a
// claim cue — a roll verb, "natural"/"nat", a German article (number-as-noun), or
// a würfeln/Wurf cue — as its precision guard. Both languages are checked
// unconditionally, keeping [claimsRollResult] language-free like the digit path.
// Capture group 1 is the number word [claimedRollValues] maps to its value.
var spelledRollClaims = []*regexp.Regexp{
	// EN: a roll verb (with optional article/of) or "natural"/"nat", then the word.
	regexp.MustCompile(`(?i)\b(?:roll(?:ed|s|ing)?(?:\s+(?:a|an|of))?|natural|nat)\s+(?:an?\s+)?(?:natural\s+|nat\s+)?(` + enNumberWord + `)\b`),
	// DE: an article (optionally "natürliche") — the number-as-noun form „eine
	// Zwanzig" — or a würfeln/Wurf cue, then the word.
	regexp.MustCompile(`(?i)\b(?:ein(?:e|en|er)?\s+(?:natürliche[nrs]?\s+)?|gewürfelt[:,]?\s+|würfel(?:st|t)?\s+|wurf(?:s|es)?\s+(?:von\s+)?)(` + deNumberWord + `)\b`),
}

// numberWordValues maps a normalized (lowercased, whitespace-collapsed) captured
// number word to its integer value — EN and DE, 1–20 plus the tens up to 100.
var numberWordValues = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7,
	"eight": 8, "nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "thirteen": 13,
	"fourteen": 14, "fifteen": 15, "sixteen": 16, "seventeen": 17, "eighteen": 18,
	"nineteen": 19, "twenty": 20, "thirty": 30, "forty": 40, "fifty": 50,
	"sixty": 60, "seventy": 70, "eighty": 80, "ninety": 90,
	"hundred": 100, "one hundred": 100,

	"eins": 1, "zwei": 2, "drei": 3, "vier": 4, "fünf": 5, "sechs": 6, "sieben": 7,
	"acht": 8, "neun": 9, "zehn": 10, "elf": 11, "zwölf": 12, "dreizehn": 13,
	"vierzehn": 14, "fünfzehn": 15, "sechzehn": 16, "siebzehn": 17, "achtzehn": 18,
	"neunzehn": 19, "zwanzig": 20, "dreißig": 30, "dreissig": 30, "vierzig": 40,
	"fünfzig": 50, "sechzig": 60, "siebzig": 70, "achtzig": 80, "neunzig": 90,
	"hundert": 100, "einhundert": 100,
}

// claimRollNumber matches a bare 1..100 integer standing on its own word
// boundaries — the numeric shape a narrated die result takes ("a 19", "17! You
// hit.", "Meine Initiative ist 15"). The guard only runs on dice-ARMED turns where
// no dice was executed, so recall matters more than precision: every such integer
// counts, except one directly followed by a unit noun ([unitAfterNumber]).
//
// Residual: the 1..100 cap misses a 3-digit invented result ("Ergebnis: 120") —
// a false NEGATIVE, deliberate (d100 is the largest standard die; years and big
// amounts sit above it). Revisit if live traffic shows the model narrating
// multi-die SUMS above 100 without calling the tool.
var claimRollNumber = regexp.MustCompile(`\b([1-9][0-9]?|100)\b`)

// unitAfterNumber is a currency, time or distance noun (EN/DE, lowercased word
// prefix) right after a number: "5 Kupferstücke", "20 Goldstücke", "vor 3 Tagen",
// "40 years", "30 Fuß". Such a number is a quantity, not a die result (O5). It is
// a prefix match so German compounds and plurals are covered.
var unitAfterNumber = regexp.MustCompile(`^\s*(gold|silber|silver|kupfer|copper|platin|münz|muenz|taler|krone|dukat|heller|groschen|coin|gp\b|sp\b|cp\b|pp\b|gm\b|piece|tag\b|tage\b|tagen|stunde|minute|sekunde|woche|monat|jahr|nacht|nächte|naechte|uhr\b|day|hour|second|week|month|year|night|o'clock|meile|mile|meter|fuß|fuss|feet|foot|schritt|pace|yard)`)

// claimsRollResult reports whether text reads as a narrated die result (#399,
// Option C): explicit die notation ("d20", "2w6", "d%") is stripped first — those
// are the request, not a result — and what remains is checked for a 1..100
// integer that is not a quantity ([unitAfterNumber]) or a spelled-out number word
// in a roll-claim context (#438). Language-free and recall-biased: a false
// positive only costs one regeneration on a turn where the dice Tool was already
// armed, whereas a false negative would ship an invented number.
func claimsRollResult(text string) bool {
	return len(claimedRollValues(text)) > 0
}

// claimedRollValues extracts the roll values text claims (#438): every 1..100
// integer not followed by a unit noun (after die notation is stripped), then
// every spelled-out number word in a roll-claim context, mapped to its integer.
// The values feed the regen-consistency check ([rollClaimConsistent]); an empty
// result means text makes no roll claim.
func claimedRollValues(text string) []int {
	stripped := diceNotation.ReplaceAllString(text, " ")
	var vals []int
	for _, loc := range claimRollNumber.FindAllStringIndex(stripped, -1) {
		if unitAfterNumber.MatchString(strings.ToLower(stripped[loc[1]:])) {
			continue
		}
		if n, err := strconv.Atoi(stripped[loc[0]:loc[1]]); err == nil {
			vals = append(vals, n)
		}
	}
	for _, re := range spelledRollClaims {
		for _, m := range re.FindAllStringSubmatch(stripped, -1) {
			word := strings.Join(strings.Fields(strings.ToLower(m[1])), " ")
			if v, ok := numberWordValues[word]; ok {
				vals = append(vals, v)
			}
		}
	}
	return vals
}

// integerToken matches any run of digits — the loose extraction for the dice
// Tool's own result line, where every number (individual rolls AND totals, which
// can exceed 100) is a legitimate value for the narration to repeat.
var integerToken = regexp.MustCompile(`\d+`)

// rollClaimConsistent reports whether a regenerated reply's narrated roll claim is
// consistent with the dice Tool's actual results (#438): a reply claiming no value
// is trivially consistent; a claimed value is consistent when it matches ANY
// number the Tool reported (individual rolls or totals — die notation in the
// result line is stripped first so "1d20" never legitimizes a 20). The any-match
// rule is deliberate: narration legitimately mixes the roll with derived numbers
// (modifiers, totals), and a false contradiction would burn the one bounded retry
// — while the flagship failure (Tool rolled 7, reply claims 20 and never mentions
// the 7) can never pass. A claim with NO Tool result at all always contradicts.
func rollClaimConsistent(text string, results []string) bool {
	claims := claimedRollValues(text)
	if len(claims) == 0 {
		return true
	}
	actual := map[int]bool{}
	for _, r := range results {
		stripped := diceNotation.ReplaceAllString(r, " ")
		for _, m := range integerToken.FindAllString(stripped, -1) {
			if n, err := strconv.Atoi(m); err == nil {
				actual[n] = true
			}
		}
	}
	for _, c := range claims {
		if actual[c] {
			return true
		}
	}
	return false
}
