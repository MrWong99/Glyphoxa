package recap

import (
	"strings"
	"unicode/utf8"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// renderLine renders one Transcript Line as `Who (Tag): text\n`, with the ` (Tag)`
// segment omitted when the Line has no Tag (ADR-0040 line grain). This is the exact
// input the LLM sees, so it is deterministic and cassette-hashable (ADR-0021).
func renderLine(l storage.TranscriptLine) string {
	var b strings.Builder
	b.WriteString(l.Who)
	if l.Tag != "" {
		b.WriteString(" (")
		b.WriteString(l.Tag)
		b.WriteString(")")
	}
	b.WriteString(": ")
	b.WriteString(l.Text)
	b.WriteString("\n")
	return b.String()
}

// renderLines concatenates the rendered Lines in the given (seq) order.
func renderLines(lines []storage.TranscriptLine) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(renderLine(l))
	}
	return b.String()
}

// lineChars is the rendered character (rune) length of one Line — the unit the
// budget/window thresholds are measured in.
func lineChars(l storage.TranscriptLine) int {
	return utf8.RuneCountInString(renderLine(l))
}

// splitWindows greedily packs consecutive WHOLE Lines into windows each no larger
// than windowChars rendered characters (ADR-0040 seq order preserved). A single
// Line longer than windowChars is never split — it forms its own oversized window,
// so the map step still sees complete Lines. Returns nil for no Lines.
func splitWindows(lines []storage.TranscriptLine, windowChars int) [][]storage.TranscriptLine {
	var windows [][]storage.TranscriptLine
	var cur []storage.TranscriptLine
	curChars := 0
	for _, l := range lines {
		lc := lineChars(l)
		// Start a new window when the current one is non-empty and adding this Line
		// would exceed the budget. A lone over-budget Line stays in its own window.
		if len(cur) > 0 && curChars+lc > windowChars {
			windows = append(windows, cur)
			cur = nil
			curChars = 0
		}
		cur = append(cur, l)
		curChars += lc
	}
	if len(cur) > 0 {
		windows = append(windows, cur)
	}
	return windows
}
