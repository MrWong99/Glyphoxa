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
	return newFuzzyIndex(NameMatchConfig{}, DoubleMetaphone, names, nil)
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
	idx := newFuzzyIndex(NameMatchConfig{MinRunes: 4}, DoubleMetaphone, [][]string{{"Ann"}}, nil)
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
	idx := newFuzzyIndex(NameMatchConfig{MaxEditDistance: 2}, nil, [][]string{{"Glyphoxa"}}, nil)
	got := idx.scoreAll(tokenize("glyphoxer rolls a check")) // 1 insertion
	if got[0] <= 0 || got[0] >= 1.0 {
		t.Fatalf("edit-net score = %v, want in (0,1)", got[0])
	}

	// Beyond the bound, nothing matches.
	far := newFuzzyIndex(NameMatchConfig{MaxEditDistance: 1}, nil, [][]string{{"Glyphoxa"}}, nil)
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

// TestDeriveTruncationAliases pins the derivation rules (#197): a leading
// consonant is dropped ("Bart"→"art"), a vowel-initial name derives nothing
// ("Anna"), a non-letter remainder is guarded ("D20"→"20" rejected), a two-rune
// name is too short once truncated ("Bo"→"o" rejected), multi-token names keep
// their remaining tokens, and collisions dedupe.
func TestDeriveTruncationAliases(t *testing.T) {
	cases := []struct {
		desc string
		in   []string
		want []string
	}{
		{"consonant drop", []string{"Bart"}, []string{"art"}},
		{"marek", []string{"Marek"}, []string{"arek"}},
		{"multi-token keeps remaining tokens", []string{"Grim Jaw"}, []string{"rim Jaw"}},
		{"vowel-initial derives none", []string{"Anna"}, nil},
		{"configured-alias vowel-initial none", []string{"innkeeper"}, nil},
		{"non-letter remainder none", []string{"D20"}, nil},
		{"two-rune name none", []string{"Bo"}, nil},
		{"barkeep", []string{"barkeep"}, []string{"arkeep"}},
		{"dedupe collision", []string{"Bart", "Cart"}, []string{"art"}},
		{"name plus aliases", []string{"Bart", "innkeeper", "barkeep"}, []string{"art", "arkeep"}},
	}
	for _, tc := range cases {
		if got := DeriveTruncationAliases(tc.in...); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: DeriveTruncationAliases(%q) = %v, want %v", tc.desc, tc.in, got, tc.want)
		}
	}
}

// TestFuzzyIndex_TruncationAlias_InitialExactHit pins the derived-alias tier
// (#197): with Bart carrying the derived alias "art", an utterance that opens
// with "Art" (the STT truncation, live turn 47aecba4be320d54) scores the
// truncation-alias score — below an exact name, above phonetics.
func TestFuzzyIndex_TruncationAlias_InitialExactHit(t *testing.T) {
	idx := newFuzzyIndex(NameMatchConfig{}, DoubleMetaphone, [][]string{{"Bart"}}, [][]string{{"art"}})
	got := idx.scoreAll(tokenize("Art, wie läuft das Geschäft heute Abend?"))
	if got[0] != truncationAliasScore {
		t.Fatalf("initial truncation hit = %v, want %v", got[0], truncationAliasScore)
	}
}

// TestFuzzyIndex_TruncationAlias_NonInitialNoHit is the position gate: the same
// derived alias "art" appearing mid-utterance (the noun "Art") must NOT route to
// Bart. It is a differential test — the derived alias must add nothing over a
// name-only index for this utterance — so it isolates the alias's contribution
// from any incidental fuzzy hit the name "Bart" earns elsewhere in the sentence.
func TestFuzzyIndex_TruncationAlias_NonInitialNoHit(t *testing.T) {
	const utter = "was für eine Art von Bier hast du?" // AC: "Art" mid-sentence
	withTrunc := newFuzzyIndex(NameMatchConfig{}, DoubleMetaphone, [][]string{{"Bart"}}, [][]string{{"art"}})
	nameOnly := newFuzzyIndex(NameMatchConfig{}, DoubleMetaphone, [][]string{{"Bart"}}, nil)
	if a, b := withTrunc.scoreAll(tokenize(utter))[0], nameOnly.scoreAll(tokenize(utter))[0]; a != b {
		t.Fatalf("mid-utterance derived alias changed the score (%v with, %v without); it must only match at the start", a, b)
	}
}

// TestFuzzyIndex_TruncationAlias_ExactOnlyNoFuzzyTiers pins that a derived alias
// is exact-only: a near-miss to the derived form contributes nothing (no
// phonetic/edit tier for a derived entry), while the exact derived form scores
// the truncation-alias score — below a genuine exact name (1.0). The near-miss
// arm is differential so it is not confused by the name "Marek" incidentally
// reaching "areck" through its own edit net.
func TestFuzzyIndex_TruncationAlias_ExactOnlyNoFuzzyTiers(t *testing.T) {
	withTrunc := newFuzzyIndex(NameMatchConfig{}, DoubleMetaphone, [][]string{{"Marek"}}, [][]string{{"arek"}})
	nameOnly := newFuzzyIndex(NameMatchConfig{}, DoubleMetaphone, [][]string{{"Marek"}}, nil)

	const near = "areck, was liegt auf deinem Amboss?" // "areck" ≠ derived "arek"
	if a, b := withTrunc.scoreAll(tokenize(near))[0], nameOnly.scoreAll(tokenize(near))[0]; a != b {
		t.Fatalf("near-miss earned %v from the derived alias over name-only %v; a derived alias never fuzzes", a, b)
	}

	if got := withTrunc.scoreAll(tokenize("arek, was liegt auf deinem Amboss?"))[0]; got != truncationAliasScore {
		t.Fatalf("exact derived hit = %v, want %v", got, truncationAliasScore)
	}
	if truncationAliasScore >= 1.0 {
		t.Fatal("a derived alias must score below a genuine exact name (1.0)")
	}
}
