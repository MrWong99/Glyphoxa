package agenttool

import (
	"regexp"
	"strings"

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
// keyword patterns whose match in an utterance marks plausible dice intent.
//
// An armed turn is not free: it runs BUFFERED (no streaming, so first audio waits
// for the whole reply) and arms the invented-roll guard, which can force a
// regeneration. So the patterns are anchored to roll-shaped PHRASES, not bare
// substrings (O5: "Vorwurf", "Wirf mir den Schlüssel rüber", "eine Probe von
// deinem Wein", "did the king die", "take advantage of" and a speaker named
// Dieter all used to arm). Every pattern still covers the real ttrpg cues — a
// missed arm withholds the dice Tool when it is genuinely needed. Tune here;
// adding a language is a data change (one entry), not a logic change. Go's \b is
// ASCII-only, so no pattern puts \b next to an umlaut.
//
//   - en: roll inflections as whole words ("rolling" only when it reads as a
//     roll, not "rolling his eyes" / "rolling hills"), "dice", a determined "die"
//     ("the die", never "did he die"), advantage only when someone HAS/rolls WITH
//     it, and the named checks/saves (#438).
//   - de: würf/wuerf (würfeln, Würfel — also inside compounds like
//     "Würfelwerkzeug"), "Wurf" and the save/attack -wurf compounds (never
//     Vorwurf/Entwurf), werfen only with no object other than dice ("Wirf noch
//     einmal!", not "Wirf mir den Schlüssel"), and Probe as a check: a -probe
//     compound that is not an everyday one (Kostprobe, Weinprobe …), or a bare
//     "Probe" that is made/laid down or taken ON a skill.
var diceKeywords = map[string][]*regexp.Regexp{
	"en": {
		regexp.MustCompile(`(?i)\b(roll|rolls|rolled|dice|saving throws?|initiative|to hit|attack rolls?|ability checks?|skill checks?|(perception|insight|investigation|stealth|athletics|acrobatics|arcana|history|nature|religion|medicine|survival|deception|intimidation|persuasion|performance|sleight of hand|animal handling|strength|dexterity|constitution|intelligence|wisdom|charisma) (check|save)s?)\b`),
		regexp.MustCompile(`(?i)\brolling\s*([.!?,;]|$|(for|a|an|the|my|your|some|dice|initiative|to|again|now)\b)`),
		regexp.MustCompile(`(?i)\b(a|the|one|this|that|my|your|each|another)\s+die\b`),
		regexp.MustCompile(`(?i)\b(with|at|have|has|had|get|gets|got|gain|gains|grant|grants|give|gives)\s+(dis)?advantage\b`),
	},
	"de": {
		regexp.MustCompile(`(?i)(würf|wuerf|initiative|(rettungs|angriffs|schadens|initiativ|todes|treffer|fertigkeits)wurf)`),
		regexp.MustCompile(`(?i)\bwurf\b`),
		regexp.MustCompile(`(?i)\bprobe\s+(auf|für|fuer|gegen|ab)\b`),
		regexp.MustCompile(`(?i)\b(mach|leg|würfl|wuerfl)\p{L}*\s+(\p{L}+\s+){0,2}probe\b`),
	},
}

// deWerfen is the German throw verb family as a whole word; it arms only when
// no object other than the dice follows it ([werfenArms]).
var deWerfen = regexp.MustCompile(`(?i)\b(wirf|werft|werfe|werfen)\b`)

// deThrowObject is an object pronoun/determiner right after a werfen verb
// ("Wirf MIR den Schlüssel", "wirf DAS Seil") — something other than a die is
// being thrown, possibly after a filler particle ("Wirf MAL den Schlüssel"). "es"
// is deliberately absent ("Wirf es nochmal" is a re-roll).
var deThrowObject = regexp.MustCompile(`(?i)^\s*((mal|doch|bitte|jetzt|schnell|kurz|halt|eben|einfach)\s+){0,3}(mir|dir|ihm|ihr|ihn|uns|euch|den|die|das|dem|der|des|ein|eine|einen|einem|einer|mein\p{L}*|dein\p{L}*|sein\p{L}*|unser\p{L}*|eur\p{L}*)\b`)

// deProbeCompound captures the prefix of a "-probe" compound
// (Wahrnehmungsprobe → "wahrnehmungs").
var deProbeCompound = regexp.MustCompile(`(?i)(\p{L}{3,})probe`)

// everydayProbe are the "-probe" compounds that are no skill check.
var everydayProbe = map[string]bool{
	"kost": true, "wein": true, "general": true, "stich": true, "bier": true,
	"hör": true, "hoer": true, "blut": true, "urin": true, "material": true,
	"gesteins": true, "wasser": true, "boden": true, "arbeits": true,
	"lese": true, "sprach": true, "tanz": true, "chor": true, "text": true,
	"schrift": true, "muster": true, "gratis": true, "waren": true,
	"speichel": true, "gewebe": true, "luft": true, "erd": true, "haut": true,
}

// werfenArms reports whether a werfen verb in text reads as (re)rolling: at least
// one occurrence not followed by a non-dice object.
func werfenArms(text string) bool {
	for _, loc := range deWerfen.FindAllStringIndex(text, -1) {
		if !deThrowObject.MatchString(text[loc[1]:]) {
			return true
		}
	}
	return false
}

// probeCompoundArms reports whether text contains a "-probe" compound that is a
// skill check (Wahrnehmungsprobe), not an everyday one (Kostprobe).
func probeCompoundArms(text string) bool {
	for _, m := range deProbeCompound.FindAllStringSubmatch(text, -1) {
		if !everydayProbe[strings.ToLower(m[1])] {
			return true
		}
	}
	return false
}

// stripSpeakerPrefix removes the "<Name>: " attribution Hot Context puts on every
// human line, so the speaker's NAME never arms the gate (a Dieter, a Wurf) — only
// what they said. A prefix is a short run before the first ": " with no sentence
// punctuation in it; anything else is returned unchanged.
func stripSpeakerPrefix(text string) string {
	i := strings.Index(text, ": ")
	if i <= 0 || i > 64 || strings.ContainsAny(text[:i], ".!?:\n") {
		return text
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
	return gl == "de" && (werfenArms(text) || probeCompoundArms(text))
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
