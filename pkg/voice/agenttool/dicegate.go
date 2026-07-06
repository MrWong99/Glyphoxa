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
// wN form: 2w6, w20), then sides (a number or '%' for d100). Word boundaries
// keep it from firing inside unrelated words (e.g. "add", "model", "würfle").
var diceNotation = regexp.MustCompile(`(?i)\b\d*[dw](\d+|%)\b`)

// diceKeywords maps a gate language (the normalized primary subtag) to the
// keyword pattern whose match in an utterance marks plausible dice intent. The
// set is biased for HIGH RECALL: a false positive only costs the (rare) extra
// tool-call round the system already paid unconditionally before this gate,
// whereas a false negative would withhold the dice Tool when it is genuinely
// needed — breaking the tool path. So the keep-IN side is generous (anything
// ttrpg-roll-shaped) and the keep-OUT side is conservative. Tune here; adding a
// language is a data change (one entry), not a logic change.
//
//   - en: \b-anchored PREFIX so inflections stay armed ("rolls", "rolling") while
//     the "die" substring no longer trips inside unrelated words ("studied") —
//     the substring false-positive class #226 also flags in the other direction.
//   - de: bare substrings, because the roll cue lives inside compounds
//     ("Würfelwerkzeug", "Rettungswurf", "Fertigkeitsprobe") that word anchors
//     would miss. This deliberately accepts rare compound false positives
//     (Entwurf, Vorwurf) — the recall bias above makes that the cheap side.
var diceKeywords = map[string]*regexp.Regexp{
	"en": regexp.MustCompile(`(?i)\b(roll|dice|die|d20|d6|d100|saving throw|initiative|advantage|disadvantage|to hit|attack roll|ability check|skill check)`),
	"de": regexp.MustCompile(`(?i)(würf|wurf|probe|initiative)`),
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
// It inspects ONLY the most recent user message (the turn's actual request);
// older history must not keep dice armed for every subsequent turn. Matching is
// case-insensitive over explicit die notation (2d6, d20, d%, w20) and the
// recall-biased keyword set selected by lang ([gateLanguage] normalizes it; an
// unknown language falls back to the EN set). An empty/absent user message
// yields false (nothing to roll for).
func needsDice(lang string, messages []llm.Message) bool {
	text := latestUserText(messages)
	if text == "" {
		return false
	}
	if diceNotation.MatchString(text) {
		return true
	}
	return diceKeywords[gateLanguage(lang)].MatchString(text)
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
