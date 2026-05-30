package address

import (
	"reflect"
	"testing"
)

func TestTokenize(t *testing.T) {
	cases := map[string][]string{
		"Bart, what's the special tonight?": {"bart", "what", "s", "the", "special", "tonight"},
		"Grim-Jaw":                          {"grim", "jaw"},
		"D20 roll":                          {"d20", "roll"},
	}
	for in, want := range cases {
		if got := tokenize(in); !reflect.DeepEqual(got, want) {
			t.Errorf("tokenize(%q) = %v, want %v", in, got, want)
		}
	}
	if got := tokenize("   "); len(got) != 0 {
		t.Errorf("tokenize(whitespace) = %v, want empty", got)
	}
}

// idxFor builds an English fuzzy index over the given per-agent names with the
// default config, the common setup for the scoring tests below.
func idxFor(names ...[]string) *fuzzyIndex {
	return newFuzzyIndex(NameMatchConfig{}, DoubleMetaphone, names)
}

// TestFuzzyIndex_ExactMatch is the baseline: a clean name scores a perfect 1.0.
func TestFuzzyIndex_ExactMatch(t *testing.T) {
	idx := idxFor([]string{"Bart"})
	got := idx.scoreAll(tokenize("Bart, what's the special tonight?"))
	if got[0] != 1.0 {
		t.Fatalf("exact name score = %v, want 1.0", got[0])
	}
}

// TestFuzzyIndex_PhoneticMishearing covers ADR-0024's core case: STT mishears
// the proper noun but preserves its sound, so the homophone still matches —
// above the edit-distance tier but below an exact hit.
func TestFuzzyIndex_PhoneticMishearing(t *testing.T) {
	idx := idxFor([]string{"Bart"})
	got := idx.scoreAll(tokenize("bard, what's the special?"))
	if got[0] != idx.cfg.withDefaults().PhoneticScore {
		t.Fatalf("phonetic score = %v, want %v", got[0], idx.cfg.PhoneticScore)
	}
	if got[0] >= 1.0 {
		t.Error("a homophone must score below an exact match")
	}
}

// TestFuzzyIndex_WindowJoin proves the sliding-window join: a single-token name
// ("Grimjaw") is matched by two adjacent spoken tokens ("grim jaw").
func TestFuzzyIndex_WindowJoin(t *testing.T) {
	idx := idxFor([]string{"Grimjaw"})
	got := idx.scoreAll(tokenize("I attack grim jaw with my axe"))
	if got[0] < idx.cfg.withDefaults().PhoneticScore {
		t.Fatalf("windowed score = %v, want >= phonetic %v", got[0], idx.cfg.PhoneticScore)
	}
}

// TestFuzzyIndex_MultiTokenName proves a multi-token name ("Glyphoxa Butler")
// is matched when its words are spoken adjacently, exercising the window
// auto-grown to fit the longest name.
func TestFuzzyIndex_MultiTokenName(t *testing.T) {
	idx := idxFor([]string{"Glyphoxa Butler"})
	if idx.window < 2 {
		t.Fatalf("window = %d, want >= 2 to fit a 2-token name", idx.window)
	}
	got := idx.scoreAll(tokenize("hey Glyphoxa Butler, summarize last session"))
	if got[0] != 1.0 {
		t.Fatalf("multi-token exact score = %v, want 1.0", got[0])
	}
}

// TestFuzzyIndex_RuneFloor pins the short-token guard: a 3-rune name is
// exact-only, so a near-miss filler word does not collide with it.
func TestFuzzyIndex_RuneFloor(t *testing.T) {
	idx := newFuzzyIndex(NameMatchConfig{MinRunes: 4}, DoubleMetaphone, [][]string{{"Ann"}})
	// "an" is one edit from "ann" but both are under the 4-rune floor → no match.
	if got := idx.scoreAll(tokenize("give me an apple")); got[0] != 0 {
		t.Errorf("short-name fuzzy score = %v, want 0 (exact-only under floor)", got[0])
	}
	// The exact spoken name still matches even under the floor.
	if got := idx.scoreAll(tokenize("Ann, hello")); got[0] != 1.0 {
		t.Errorf("short-name exact score = %v, want 1.0", got[0])
	}
}

// TestFuzzyIndex_EditDistanceNet covers a non-homophone typo within the bound:
// a one-edit miss that the phonetic codes happen not to share still matches via
// the Damerau-Levenshtein net, scoring strictly below the phonetic tier.
func TestFuzzyIndex_EditDistanceNet(t *testing.T) {
	// No encoder → the edit net is the entire fuzzy layer (ADR-0024).
	idx := newFuzzyIndex(NameMatchConfig{MaxEditDistance: 2}, nil, [][]string{{"Glyphoxa"}})
	got := idx.scoreAll(tokenize("glyphoxer rolls a check")) // 1 insertion
	if got[0] <= 0 || got[0] >= 1.0 {
		t.Fatalf("edit-net score = %v, want in (0,1)", got[0])
	}

	// Beyond the bound, nothing matches.
	far := newFuzzyIndex(NameMatchConfig{MaxEditDistance: 1}, nil, [][]string{{"Glyphoxa"}})
	if s := far.scoreAll(tokenize("grindstone")); s[0] != 0 {
		t.Errorf("out-of-bound score = %v, want 0", s[0])
	}
}

// TestFuzzyIndex_Aliases proves an alias is matchable alongside the primary
// name and that scoreAll attributes the hit to the right agent index.
func TestFuzzyIndex_Aliases(t *testing.T) {
	idx := idxFor(
		[]string{"Bart", "the innkeeper"},
		[]string{"Glyphoxa"},
	)
	got := idx.scoreAll(tokenize("ask the innkeeper about rooms"))
	if got[0] != 1.0 {
		t.Errorf("alias score for agent 0 = %v, want 1.0", got[0])
	}
	if got[1] != 0 {
		t.Errorf("agent 1 should not match, got %v", got[1])
	}
}

// TestFuzzyIndex_NoMatch confirms an unrelated utterance scores nobody.
func TestFuzzyIndex_NoMatch(t *testing.T) {
	idx := idxFor([]string{"Bart"})
	if got := idx.scoreAll(tokenize("let us roll initiative please")); len(got) != 0 {
		t.Errorf("unrelated utterance scored %v, want empty", got)
	}
}
