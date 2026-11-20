package agenttool

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// diceHardeningInstruction is appended to the system prompt on dice-armed turns
// (#399, Option B): cheap prompt hardening that tells the model to call the dice
// Tool and never narrate an invented result. Not airtight on its own — the
// post-hoc [claimsRollResult] guard is the enforcement backstop.
const diceHardeningInstruction = "When asked to roll dice, call the dice tool and report its actual result. Never invent, guess, or roleplay a die result."

// A digit in the reply is a claimed roll result only in a ROLL CONTEXT (O5) —
// otherwise every price, age or headcount on a dice-armed turn ("Das macht 5
// Kupferstücke", "vor 3 Tagen") forced a pointless regeneration. Two shapes count,
// both limited to 1..100:
//   - proximity: the integer sits within [rollCueWindow] words of a roll cue —
//     a roll/result word ([rollCueWord]) or die notation ("I roll the d20 … 12");
//   - number-as-noun: an article (+ at most one adjective) directly before it,
//     with nothing noun-like after — "a 19!", "a perfect 100 on the nose",
//     "eine 17 gewürfelt" — but not "a 5 gold coin" / "eine 3 Tage alte Karte".
//
// Residual: the 1..100 cap misses a 3-digit invented result ("Ergebnis: 120") —
// a false NEGATIVE, deliberate (d100 is the largest standard die). Revisit if
// live traffic shows the model narrating multi-die SUMS above 100 unrolled.
const rollCueWindow = 6

// rollToken splits text into words (letters/digits/%) and single punctuation
// marks, so the claim scan can see word distance and what follows a number.
var rollToken = regexp.MustCompile(`[\p{L}\p{N}%']+|[^\s\p{L}\p{N}]`)

// rollCueWord is a (lowercased) word that puts nearby digits in a roll context,
// EN and DE.
var rollCueWord = regexp.MustCompile(`^(roll|rolls|rolled|rolling|natural|nat|result|results|total|dice|w(ü|ue)rf\p{L}*|gew(ü|ue)rfelt|\p{L}*wurf|\p{L}*wurfs|geworfen|ergebnis\p{L}*|nat(ü|ue)rliche\p{L}*|augen)$`)

// dieNotationToken is a single token of die notation (d20, 2W6, d%), itself a
// roll cue and never a claimed value.
var dieNotationToken = regexp.MustCompile(`(?i)^\d*[dw](\d+|%)$`)

// numberArticles open the number-as-noun form; the German ones also allow any
// lowercase word after the number (German nouns are capitalized), the English
// ones only [enAfterNumber].
var numberArticles = map[string]bool{"a": true, "an": true, "ein": true, "eine": true, "einen": true, "einer": true, "einem": true}
var deNumberArticles = map[string]bool{"ein": true, "eine": true, "einen": true, "einer": true, "einem": true}
var enAfterNumber = map[string]bool{
	"on": true, "with": true, "plus": true, "and": true, "for": true, "to": true,
	"but": true, "so": true, "again": true, "total": true, "this": true, "that": true,
	"here": true, "there": true, "then": true, "now": true,
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

// claimsRollResult reports whether text reads as a narrated die result (#399,
// Option C): a 1..100 integer in a roll context (see [rollCueWindow]) or a
// spelled-out number word in a roll-claim context (#438). Explicit die notation
// ("d20", "2w6", "d%") is the request, never a result. Language-free (EN and DE
// cues together): a false positive costs a regeneration on an armed turn, a false
// negative ships an invented number, so the cues stay broad within a roll context.
func claimsRollResult(text string) bool {
	return len(claimedRollValues(text)) > 0
}

// claimedRollValues extracts the roll values text claims (#438): every 1..100
// integer in a roll context, then every spelled-out number word in a roll-claim
// context, mapped to its integer. The values feed the regen-consistency check
// ([rollClaimConsistent]); an empty result means text makes no roll claim.
func claimedRollValues(text string) []int {
	vals := digitRollClaims(text)
	stripped := diceNotation.ReplaceAllString(text, " ")
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

// digitRollClaims returns the 1..100 integers text claims as roll results, in
// order: those within [rollCueWindow] words of a roll cue, and those in the
// number-as-noun form (see [rollCueWindow] for both shapes).
func digitRollClaims(text string) []int {
	type tok struct {
		s     string // original
		low   string
		word  bool // letters/digits, not punctuation
		cue   bool
		value int // 1..100 for a claimable integer, else 0
		wi    int // index among word tokens
	}
	var toks []tok
	wi := 0
	for _, s := range rollToken.FindAllString(text, -1) {
		t := tok{s: s, low: strings.ToLower(s)}
		r := []rune(s)[0]
		t.word = unicode.IsLetter(r) || unicode.IsDigit(r) || r == '%' || r == '\''
		if t.word {
			t.wi = wi
			wi++
			switch {
			case dieNotationToken.MatchString(s):
				t.cue = true
			case rollCueWord.MatchString(t.low):
				t.cue = true
			default:
				if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= 100 {
					t.value = n
				}
			}
		}
		toks = append(toks, t)
	}
	var cues []int
	for _, t := range toks {
		if t.cue {
			cues = append(cues, t.wi)
		}
	}
	nearCue := func(w int) bool {
		for _, c := range cues {
			if d := c - w; d >= -rollCueWindow && d <= rollCueWindow {
				return true
			}
		}
		return false
	}
	// prevWord returns the index of the token before i when it is a word, or -1
	// (start of text, or punctuation, which breaks the noun phrase).
	prevWord := func(i int) int {
		if i > 0 && toks[i-1].word {
			return i - 1
		}
		return -1
	}
	nounForm := func(i int) bool {
		a := prevWord(i)
		if a >= 0 && !numberArticles[toks[a].low] {
			a = prevWord(a) // one adjective: "a perfect 100", "eine glatte 20"
		}
		if a < 0 || !numberArticles[toks[a].low] {
			return false
		}
		if i+1 >= len(toks) || !toks[i+1].word {
			return true // end or punctuation: "a 19!"
		}
		next := toks[i+1].s
		if deNumberArticles[toks[a].low] {
			return unicode.IsLower([]rune(next)[0]) // a capitalized next word is a noun
		}
		return enAfterNumber[toks[i+1].low]
	}
	var vals []int
	for i, t := range toks {
		if t.value == 0 {
			continue
		}
		if nearCue(t.wi) || nounForm(i) {
			vals = append(vals, t.value)
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
