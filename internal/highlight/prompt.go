package highlight

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

// classifyInstruction is the fixed classifier directive (the RoleSystem prompt).
// Fixed text: the cassette prompt hash (ADR-0021) depends on it byte-for-byte, so
// a wording change is a genuine prompt change that must miss its cassette.
const classifyInstruction = "You are watching a live tabletop RPG voice session and spotting highlight-worthy moments — " +
	"a critical hit, a dramatic reversal, a clutch save, a big emotional or comedic beat. " +
	"Rate how strong a highlight the following recent moment is on a scale from 0 to 10, " +
	"where 0 is mundane table chatter or rules bookkeeping and 10 is an unforgettable epic beat the table will retell. " +
	"Weigh both the transcript and the audio-energy summary: louder, more excited delivery suggests a bigger moment. " +
	"Respond with ONLY a JSON object and nothing else, in the form " +
	`{"score": <number 0-10>, "excerpt": "<short quote capturing the moment>", "reason": "<one short sentence>"}.`

// classification is a parsed classifier verdict for one window.
type classification struct {
	score   float64
	excerpt string
	reason  string
}

// buildRequest renders the classifier [llm.Request] for a window: the fixed system
// instruction plus a user message carrying the recent transcript lines and the
// per-lane audio-energy summary. It is deterministic in its inputs (no wall-clock,
// no map iteration) so the prompt hash is stable for cassette replay (ADR-0021).
func buildRequest(model string, lines []finalLine, featureSummary string) llm.Request {
	var b strings.Builder
	b.WriteString("Recent transcript:\n")
	if len(lines) == 0 {
		b.WriteString("(no transcript)\n")
	}
	for _, l := range lines {
		b.WriteString(l.render())
		b.WriteByte('\n')
	}
	b.WriteString("\n")
	b.WriteString(featureSummary)
	return llm.Request{
		Model:     model,
		MaxTokens: 256,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Text: classifyInstruction},
			{Role: llm.RoleUser, Text: b.String()},
		},
	}
}

// promptRunes counts the utf8 runes across a request's message texts, for the
// ceil(chars/4) input-token estimate when the provider reports no usage (ADR-0045).
func promptRunes(req llm.Request) int {
	n := 0
	for _, m := range req.Messages {
		n += utf8.RuneCountInString(m.Text)
	}
	return n
}

// parseClassification extracts the classifier's JSON verdict from the model's
// prose. It is deliberately tolerant (a classifier that wraps the object in prose
// or fences still parses): it slices from the first '{' to the last '}' and
// unmarshals. It reports parse success as the second return: a malformed or absent
// object yields a zero score AND ok=false, so the caller can distinguish a genuine
// low-score verdict from an unparseable stream (#428) — either way a classify never
// crashes the worker (the highlight is simply not confirmed). The slicing behaviour
// is identical to the pre-#428 tolerant parse; only the ok signal is new.
func parseClassification(text string) (classification, bool) {
	start := strings.IndexByte(text, '{')
	end := strings.LastIndexByte(text, '}')
	if start < 0 || end <= start {
		return classification{}, false
	}
	var raw struct {
		Score   float64 `json:"score"`
		Excerpt string  `json:"excerpt"`
		Reason  string  `json:"reason"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &raw); err != nil {
		return classification{}, false
	}
	return classification{score: raw.Score, excerpt: raw.Excerpt, reason: raw.Reason}, true
}

// estimateTokens is the ceil(chars/4) per-direction token estimate (ADR-0045),
// used when the classifier provider reports no usage.
func estimateTokens(runes int) int { return (runes + 3) / 4 }
