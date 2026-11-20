package agenttool

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// diceToolName is the [tool.Tool.Name] of the built-in dice Tool, gated per turn
// by [needsDice]. Kept here (not imported from pkg/tool) so the gate names what
// it drops without widening this package's coupling.
const diceToolName = "dice"

// diceNotation matches explicit die notation anywhere in the text, in any
// language: an optional count, a 'd' (NdM: 2d6, d20, d%) or a 'w' (the German
// wN form: 2w6, w20), then sides — a number or '%' (d100). The leading \b and
// the \b inside the digit branch keep the numeric form from firing inside
// unrelated words ("add", "model", "würfle"); the '%' branch carries no
// trailing \b because '%' is not a word char, so "d%"/"w%" match on their own
// (a \b after '%' would require a following word char and could never fire).
var diceNotation = regexp.MustCompile(`(?i)\b\d*[dw](\d+\b|%)`)

// diceKeywords maps a gate language (the normalized primary subtag) to the
// keyword patterns whose match in an utterance marks plausible dice intent; the
// German table is completed by [deArms] (the cues that need a denylist rather
// than a pattern).
//
// An armed turn is not free: it runs BUFFERED (no streaming, so first audio waits
// for the whole reply) and arms the invented-roll guard. But a missed arm
// withholds the dice Tool when it is genuinely needed, so the gate stays
// RECALL-biased: cues are matched broadly and only the observed everyday
// false-positive shapes are carved out (O5: "Vorwurf", "Wirf mir den Schlüssel
// rüber", "eine Probe von deinem Wein", "did the king die", "take advantage of",
// "rolling hills", a speaker named Dieter). Tune here; adding a language is a
// data change. Go's \b is ASCII-only, so no pattern puts \b next to an umlaut.
//
//   - en: roll inflections as whole words ("rolling" unless an everyday
//     collocation follows, [enRollingNot]), "dice", a determined "die" ("the
//     die", never "did he die"), advantage only when someone HAS/rolls WITH it,
//     and the named checks/saves (#438).
//   - de: see [deArms].
var diceKeywords = map[string][]*regexp.Regexp{
	"en": {
		regexp.MustCompile(`(?i)\b(roll|rolls|rolled|dice|saving throws?|initiative|to hit|attack rolls?|ability checks?|skill checks?|(perception|insight|investigation|stealth|athletics|acrobatics|arcana|history|nature|religion|medicine|survival|deception|intimidation|persuasion|performance|sleight of hand|animal handling|strength|dexterity|constitution|intelligence|wisdom|charisma) (check|save)s?)\b`),
		regexp.MustCompile(`(?i)\b(a|the|one|this|that|my|your|each|another)\s+die\b`),
		regexp.MustCompile(`(?i)\b(with|at|have|has|had|get|gets|got|gain|gains|grant|grants|give|gives)\s+(dis)?advantage\b`),
	},
	"de": {
		regexp.MustCompile(`(?i)initiative`),
	},
}

// enRolling finds "rolling" and the word after it; [enRollingNot] lists the
// everyday collocations that are no roll ("rolling hills", "rolling his eyes").
var enRolling = regexp.MustCompile(`(?i)\brolling\b\s*(\p{L}*)`)

var enRollingNot = map[string]bool{
	"hills": true, "hill": true, "his": true, "her": true, "their": true, "its": true,
	"eyes": true, "thunder": true, "stone": true, "stones": true, "waves": true,
	"fog": true, "mist": true, "green": true, "meadows": true, "plains": true,
	"countryside": true, "landscape": true, "pin": true, "mill": true, "news": true,
	"blackout": true, "basis": true, "back": true, "down": true, "over": true,
	"around": true, "along": true, "away": true, "out": true, "up": true, "in": true,
}

// rollingArms reports whether some "rolling" in text is a roll.
func rollingArms(text string) bool {
	for _, m := range enRolling.FindAllStringSubmatch(text, -1) {
		if !enRollingNot[strings.ToLower(m[1])] {
			return true
		}
	}
	return false
}

// deWurfCompound captures the prefix before a Wurf/würf stem ("Stärke" in
// "Stärkewurf", "" in "Würfel"); [deWurfNot] are the everyday compounds.
var deWurfCompound = regexp.MustCompile(`(?i)(\p{L}*?)(wurf|würf|wuerf)`)

var deWurfNot = map[string]bool{
	"vor": true, "ent": true, "ein": true, "aus": true, "ab": true, "über": true,
	"ueber": true, "maul": true, "um": true, "zu": true, "an": true, "nach": true,
	"rück": true, "rueck": true, "auf": true, "raus": true, "unter": true,
}

// wurfPrefixEveryday reports whether a Wurf/würf compound prefix ends in an
// everyday-compound stem — so "Gesetzentwurf" is caught like "Entwurf".
func wurfPrefixEveryday(prefix string) bool {
	p := strings.ToLower(prefix)
	for d := range deWurfNot {
		if strings.HasSuffix(p, d) {
			return true
		}
	}
	return false
}

// deProbeIdiom is a bare Probe that is no check: the idiom "auf die Probe
// stellen", or a rehearsal ("Probe mit der Kapelle").
var deProbeIdiom = regexp.MustCompile(`(?i)\bauf\s+die\s+probe\b|kapelle|\bchor\b|\bband\b|orchester|theater|bühne|buehne|konzert|aufführung|auffuehrung`)

// deProbeCompound captures the prefix of a "-probe" compound
// (Wahrnehmungsprobe → "wahrnehmungs"); [everydayProbe] are the everyday ones.
var deProbeCompound = regexp.MustCompile(`(?i)(\p{L}{3,})probe`)

var everydayProbe = map[string]bool{
	"kost": true, "wein": true, "general": true, "stich": true, "bier": true,
	"hör": true, "hoer": true, "blut": true, "urin": true, "material": true,
	"gesteins": true, "wasser": true, "boden": true, "arbeits": true,
	"lese": true, "sprach": true, "tanz": true, "chor": true, "text": true,
	"schrift": true, "muster": true, "gratis": true, "waren": true,
	"speichel": true, "gewebe": true, "luft": true, "erd": true, "haut": true,
}

// deBareProbe finds a stand-alone "Probe" and the word after it; a sample OF
// something ("eine Probe von deinem Wein") is no check ([deProbeOf]).
var deBareProbe = regexp.MustCompile(`(?i)\bprobe\b\s*(\p{L}*)`)

var deProbeOf = map[string]bool{"von": true, "vom": true, "aus": true, "des": true, "deines": true, "deiner": true, "eures": true}

// deWerfen finds a werfen verb and the clause after it (to the next sentence
// punctuation).
var deWerfen = regexp.MustCompile(`(?i)\b(wirf|werft|werfe|werfen)\b([^.!?;]*)`)

// deThrowEvidence is a word in the clause after a werfen verb that says a THING
// is being thrown somewhere — a direction particle ("Wirf mir den Schlüssel
// RÜBER", "Wirf ihn INS Feuer") — or the idiom "einen Blick werfen".
var deThrowEvidence = regexp.MustCompile(`(?i)^(rüber|rueber|herüber|herueber|hinüber|hinueber|weg|raus|heraus|hinaus|rein|herein|hinein|zurück|zurueck|ins|runter|herunter|hinunter|hoch|blick|blicke)$`)

// werfenArms reports whether a werfen verb reads as (re)rolling. It ARMS by
// default — "Wirf Wahrnehmung für mich", "Wirf nochmal, Flo", "Werfen Sie bitte
// noch einmal" — and stays off only when the clause after the verb carries
// throw evidence ([deThrowEvidence]).
func werfenArms(text string) bool {
	for _, m := range deWerfen.FindAllStringSubmatch(text, -1) {
		thrown := false
		for _, w := range strings.Fields(m[2]) {
			if deThrowEvidence.MatchString(strings.Trim(w, `,"'()`)) {
				thrown = true
				break
			}
		}
		if !thrown {
			return true
		}
	}
	return false
}

// deArms is the German gate beyond the pattern table: any Wurf/Würfel compound
// except an everyday one (Vorwurf, Gesetzentwurf, Rauswurf, unterwürfig …), any
// -probe compound except an everyday one (Kostprobe), a bare "Probe" unless it
// is a sample OF something, the "auf die Probe" idiom or a rehearsal, and werfen
// unless something is thrown somewhere ([werfenArms]).
func deArms(text string) bool {
	for _, m := range deWurfCompound.FindAllStringSubmatch(text, -1) {
		if !wurfPrefixEveryday(m[1]) {
			return true
		}
	}
	for _, m := range deProbeCompound.FindAllStringSubmatch(text, -1) {
		if !everydayProbe[strings.ToLower(m[1])] {
			return true
		}
	}
	if !deProbeIdiom.MatchString(text) {
		for _, m := range deBareProbe.FindAllStringSubmatch(text, -1) {
			if !deProbeOf[strings.ToLower(m[1])] {
				return true
			}
		}
	}
	return werfenArms(text)
}

// speakerParticles are lowercase words a multi-word display name may contain.
var speakerParticles = map[string]bool{"von": true, "van": true, "de": true, "der": true, "den": true, "zu": true, "du": true, "of": true, "the": true, "/": true, "-": true, "&": true}

// stripSpeakerPrefix removes the "<Name>: " attribution Hot Context puts on every
// human line, so the speaker's NAME never arms the gate (a Dieter, a Wurf) — only
// what they said. It must never eat content ("Okay Bart, roll for me: stealth"),
// so a prefix is: a single token, or up to five name-shaped words (each
// capitalized or a name particle), with no punctuation. Anything else is returned
// unchanged — keeping a real name in the text only risks an extra arm.
func stripSpeakerPrefix(text string) string {
	i := strings.Index(text, ": ")
	if i <= 0 || i > 64 || strings.ContainsAny(text[:i], ".,!?:;\n") {
		return text
	}
	words := strings.Fields(text[:i])
	if len(words) == 0 || len(words) > 5 {
		return text
	}
	if len(words) > 1 {
		for _, w := range words {
			r := []rune(w)
			if !unicode.IsUpper(r[0]) && !speakerParticles[strings.ToLower(w)] {
				return text
			}
		}
	}
	return text[i+2:]
}

// gateLanguage normalizes a Campaign Language to a key in [diceKeywords]: it
// lowercases the primary subtag ("de-DE" → "de") and degrades any language with
// no registered keyword table to "en". This mirrors the address matcher's EN
// fallback (matcherLanguage, ADR-0024): an unknown Campaign Language degrades to
// English, never to nothing — a language with no table still gets the EN gate,
// not a gate that never arms dice.
func gateLanguage(lang string) string {
	primary := lang
	if i := strings.IndexAny(primary, "-_"); i >= 0 {
		primary = primary[:i]
	}
	primary = strings.ToLower(primary)
	if _, ok := diceKeywords[primary]; ok {
		return primary
	}
	return "en"
}

// needsDice reports whether the latest user utterance in the assembled Hot
// Context plausibly needs the dice Tool, so the [Engine] offers dice only then —
// a plain conversational turn is left a single LLM round (no empty tool-call
// round before first audio; latency investigation baseline finding #4).
//
// It inspects ONLY the most recent user message (the turn's actual request), with
// its "<Name>: " speaker prefix removed; older history must not keep dice armed
// for every subsequent turn. Matching is case-insensitive over explicit die
// notation (2d6, d20, d%, w20) and the keyword set selected by lang
// ([gateLanguage] normalizes it; an unknown language falls back to the EN set).
// An empty/absent user message yields false (nothing to roll for).
func needsDice(lang string, messages []llm.Message) bool {
	text := stripSpeakerPrefix(latestUserText(messages))
	if text == "" {
		return false
	}
	if diceNotation.MatchString(text) {
		return true
	}
	gl := gateLanguage(lang)
	for _, re := range diceKeywords[gl] {
		if re.MatchString(text) {
			return true
		}
	}
	if gl == "de" {
		return deArms(text)
	}
	return rollingArms(text)
}

// latestUserText returns the Text of the last user-role message, or "" if there
// is none. It is the turn's current request — the only thing the dice gate
// should consider (a dice roll three turns ago must not arm dice now).
func latestUserText(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == llm.RoleUser {
			return messages[i].Text
		}
	}
	return ""
}
