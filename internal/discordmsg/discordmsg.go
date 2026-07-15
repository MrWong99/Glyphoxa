// Package discordmsg holds the single Discord message-splitting implementation
// shared by every surface that posts free-form text to a channel: Discord
// rejects a message whose content exceeds [Limit] characters, so long answers
// (a recap's Followups, a Butler text reply) are split into ordered messages
// instead of being truncated or 400ing.
//
// It is a stdlib-only leaf (the textnorm precedent) so any package can depend
// on it without risking an import cycle — internal/presence imports
// internal/wirenpc, so before this package the splitter existed twice, copied
// verbatim to dodge that cycle.
package discordmsg

// Limit is Discord's per-message character cap: a message whose content
// exceeds it is rejected (a Followup over it 400s and the GM sees nothing).
const Limit = 2000

// Split breaks text into ordered chunks each at most limit RUNES (never bytes —
// German recaps and Butler answers), preferring to break on a newline, then a
// space, so a chunk ends at a natural boundary rather than mid-word; only a
// single unbroken run longer than limit is hard-cut. The boundary whitespace is
// dropped so it does not lead the next chunk. Concatenating the chunks
// (re-inserting a single break) reproduces the text in order. Never truncates:
// every rune is delivered (#271, #299). A limit <= 0 disables splitting.
func Split(text string, limit int) []string {
	if limit <= 0 {
		return []string{text}
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return []string{text}
	}
	var parts []string
	for len(runes) > limit {
		cut := limit
		if i := lastBreakRune(runes, limit, '\n'); i > 0 {
			cut = i
		} else if i := lastBreakRune(runes, limit, ' '); i > 0 {
			cut = i
		}
		parts = append(parts, string(runes[:cut]))
		rest := runes[cut:]
		// Drop the boundary whitespace we broke on (not a hard mid-run cut).
		if cut < limit && len(rest) > 0 && (rest[0] == '\n' || rest[0] == ' ') {
			rest = rest[1:]
		}
		runes = rest
	}
	if len(runes) > 0 {
		parts = append(parts, string(runes))
	}
	return parts
}

// lastBreakRune returns the last index in [1, limit) where runes[i]==ch, or -1.
// The lower bound of 1 avoids an empty leading chunk when a break sits at
// index 0.
func lastBreakRune(runes []rune, limit int, ch rune) int {
	for i := limit - 1; i > 0; i-- {
		if runes[i] == ch {
			return i
		}
	}
	return -1
}
