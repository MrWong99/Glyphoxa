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

// dicePattern matches an explicit NdM / dM die notation anywhere in the text:
// an optional count, a 'd', then sides (a number or '%' for d100). Word
// boundaries keep it from firing inside unrelated words (e.g. "add", "model").
var dicePattern = regexp.MustCompile(`(?i)\b\d*d(\d+|%)\b`)

// diceKeywords are the lowercase substrings whose presence in an utterance marks
// plausible dice intent. The set is biased for HIGH RECALL: a false positive
// only costs the (rare) extra tool-call round the system already paid
// unconditionally before this gate, whereas a false negative would withhold the
// dice Tool when it is genuinely needed — breaking the tool path. So the keep-IN
// side is generous (anything ttrpg-roll-shaped) and the keep-OUT side is
// conservative. Tune here.
var diceKeywords = []string{
	"roll", "dice", "die", "d20", "d6", "d100",
	// ttrpg cues that imply a roll even without the word "roll":
	"saving throw", "initiative", "advantage", "disadvantage",
	"to hit", "attack roll", "ability check", "skill check",
}

// needsDice reports whether the latest user utterance in the assembled Hot
// Context plausibly needs the dice Tool, so the [Engine] offers dice only then —
// a plain conversational turn is left a single LLM round (no empty tool-call
// round before first audio; latency investigation baseline finding #4).
//
// It inspects ONLY the most recent user message (the turn's actual request);
// older history must not keep dice armed for every subsequent turn. Matching is
// case-insensitive over explicit die notation (2d6, d20, d%) and a small,
// recall-biased keyword set. An empty/absent user message yields false (nothing
// to roll for).
func needsDice(messages []llm.Message) bool {
	text := latestUserText(messages)
	if text == "" {
		return false
	}
	if dicePattern.MatchString(text) {
		return true
	}
	lower := strings.ToLower(text)
	for _, kw := range diceKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
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
