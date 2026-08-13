package portraitgen

import (
	"strings"
	"unicode/utf8"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/kgvocab"
)

// Prompt composition for a generated portrait (#590).
//
// The shape follows mapgen: a fixed instruction, the campaign framing, the
// entry's own material, then the GM's optional extra words. Every
// variable-length piece is cut by RUNE count — a GM with a 40 000-character NPC
// biography should get a portrait, not a rejected request.

const (
	// maxSeedBodyRunes bounds the entry's prose. A portrait is a picture of a
	// subject, not an illustrated biography: past a few hundred runes the model
	// is being told about history, not appearance.
	maxSeedBodyRunes = 600
	// maxSeedAspects bounds how many public facts are folded in. Facts are
	// where GMs keep exactly the material a portrait wants ("appearance: red
	// beard, one eye"), so they rank above the body — but a hundred facts shape
	// nothing.
	maxSeedAspects = 12
	// MaxPromptRunes bounds the GM's own words. Exported so the RPC boundary
	// can REFUSE an over-long prompt rather than accept it and silently drop
	// the tail (the mapgen contract).
	MaxPromptRunes = 2000
)

// portraitInstruction is the fixed directive.
//
// "No text or lettering" is the same trick mapgen and highlight enrichment use,
// and it earns its place for the same reason: image models render names and
// captions as convincing gibberish, and a portrait with a nonsense nameplate is
// worse than one with none.
const portraitInstruction = "Draw a single portrait illustration for a tabletop RPG, " +
	"depicting only the subject described below against a simple evocative background. " +
	"No text, no labels, no lettering, no watermark, and no border anywhere in the image. " +
	"Painterly character-art style."

// subjectLabels names each entry type for the prompt in plain words — "an NPC"
// would leak jargon into the image model's brief, and a Location or Item is not
// a person to portray but a subject to depict.
var subjectLabels = map[storage.KGNodeType]string{
	storage.KGNodeCharacter:  "a player character",
	storage.KGNodeNPC:        "a character",
	storage.KGNodeLocation:   "a place",
	storage.KGNodeFaction:    "a group or organization, shown through its emblem, members or seat",
	storage.KGNodeItem:       "an object",
	storage.KGNodePlotThread: "a scene evoking an unfolding story",
	storage.KGNodeNote:       "a scene",
}

// buildPrompt composes the full image prompt from the entry's PUBLIC material
// (the seed read already filtered gm_private) plus the GM's optional words.
func buildPrompt(c storage.Campaign, node storage.KGNode, gmPrompt string) string {
	var b strings.Builder
	b.WriteString(portraitInstruction)

	if c.Name != "" {
		b.WriteString("\n\nThe campaign is called \"" + c.Name + "\".")
	}
	if c.System != "" {
		b.WriteString("\nThe TTRPG system is " + c.System + ".")
	}

	subject := subjectLabels[node.Type]
	if subject == "" {
		subject = "a subject"
	}
	b.WriteString("\n\nThe portrait depicts " + subject + " named \"" + node.Name + "\".")
	if body := strings.TrimSpace(node.Body); body != "" {
		b.WriteString(" What the world knows: " + truncateRunes(body, maxSeedBodyRunes))
	}
	if facts := seedFacts(node.Aspects); facts != "" {
		b.WriteString("\nKnown facts: " + facts)
	}

	if gmPrompt != "" {
		b.WriteString("\n\nThe GM asks for: " + truncateRunes(gmPrompt, MaxPromptRunes))
	}

	// The Campaign Language is deliberately NOT pinned, for mapgen's reason: the
	// instruction forbids lettering, so there is no prose in the artefact to
	// localize.
	return b.String()
}

// seedFacts renders the entry's public Aspects as "key: value" clauses. The
// seed read returns public rows only, so no privacy check belongs here — a
// second filter would suggest the first one is optional (#542's seam rule).
func seedFacts(aspects []storage.KGNodeAspect) string {
	if len(aspects) == 0 {
		return ""
	}
	n := len(aspects)
	if n > maxSeedAspects {
		n = maxSeedAspects
	}
	parts := make([]string, 0, n)
	for _, a := range aspects[:n] {
		// A fact's two halves are already bounded by the editor and Tool caps
		// (kgvocab); the truncation here is belt-and-braces for imported data.
		clause := truncateRunes(a.Key, kgvocab.MaxAspectKeyRunes) + ": " +
			truncateRunes(a.Value, kgvocab.MaxAspectValueRunes)
		parts = append(parts, clause)
	}
	return strings.Join(parts, "; ") + "."
}

// truncateRunes returns s cut to at most n runes, on a rune boundary.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n])) + "…"
}
