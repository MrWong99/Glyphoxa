package mapgen

import (
	"strings"
	"unicode/utf8"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Prompt composition for a generated map (#541).
//
// The shape follows internal/assist: a fixed instruction, then the campaign
// framing, then the GM's own words, then the wiki seed. The bounds are the
// interesting part — every variable-length piece is cut by RUNE count, because a
// GM with a 40 000-character Location entry should get a map, not a rejected
// request.

const (
	// maxSeedBodyRunes bounds the anchor entry's prose. A map is a picture of a
	// place, not an illustrated encyclopedia entry: past a few hundred runes the
	// model is being told about politics, not geography.
	maxSeedBodyRunes = 600
	// maxResidents bounds how many of a Location's inhabitants are named. The list
	// is there to shape the picture ("a harbour, a tavern, a chapel"), and a
	// hundred names shape nothing.
	maxResidents = 12
	// maxPromptRunes bounds the GM's own words.
	maxPromptRunes = 2000
)

// mapInstruction is the fixed directive.
//
// "No text or lettering" is the same trick internal/highlight/enrich.go uses, and
// it earns its place: image models render captions and labels as convincing
// gibberish, and a map covered in nonsense words is worse than a map with none —
// the GM cannot label over it and the table reads it as canon.
const mapInstruction = "Draw a top-down fantasy map illustration for a tabletop RPG. " +
	"No text, no labels, no lettering, no legend, and no compass rose anywhere in the image. " +
	"Render terrain, buildings and landmarks only, in a hand-drawn cartographic style."

// seedContext is the wiki material a location-seeded prompt folds in.
type seedContext struct {
	Name      string
	Body      string
	Residents []string
}

func (s seedContext) empty() bool { return s.Name == "" }

// buildPrompt composes the full image prompt.
func buildPrompt(c storage.Campaign, gmPrompt string, seed seedContext) string {
	var b strings.Builder
	b.WriteString(mapInstruction)

	if c.Name != "" {
		b.WriteString("\n\nThe campaign is called \"" + c.Name + "\".")
	}
	if c.System != "" {
		b.WriteString("\nThe TTRPG system is " + c.System + ".")
	}

	if !seed.empty() {
		b.WriteString("\n\nThe map depicts \"" + seed.Name + "\".")
		if body := strings.TrimSpace(seed.Body); body != "" {
			b.WriteString(" What the world knows about it: " + truncateRunes(body, maxSeedBodyRunes))
		}
		if len(seed.Residents) > 0 {
			named := seed.Residents
			if len(named) > maxResidents {
				named = named[:maxResidents]
			}
			b.WriteString("\nPlaces and people found there: " + strings.Join(named, ", ") + ".")
		}
	}

	b.WriteString("\n\nThe GM asks for: " + truncateRunes(strings.TrimSpace(gmPrompt), maxPromptRunes))

	// The Campaign Language is deliberately NOT pinned here. Every other generated
	// artefact is prose the table reads, but this instruction says to put no
	// lettering in the image at all — asking for it in German would be asking for
	// the one thing the prompt forbids.
	return b.String()
}

// truncateRunes returns s cut to at most n runes, on a rune boundary.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n])) + "…"
}
