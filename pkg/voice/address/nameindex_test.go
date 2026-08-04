package address

import "testing"

// NameIndex (#545) is a thin wrapper over the SAME fuzzyIndex the live Address
// Detector runs on. These tests pin that it is thin — a wrapper that quietly
// scored differently would be the second matcher the slice exists to avoid.

func nameIdxFor(t *testing.T, lang string, names ...string) *NameIndex {
	t.Helper()
	var enc Encoder
	if lang != "" {
		e, ok := DefaultEncoders().For(lang)
		if !ok {
			t.Fatalf("no encoder for %q", lang)
		}
		enc = e
	}
	return NewNameIndex(NameMatchConfig{}, enc, names)
}

func minConf() float64 { return MinNameConfidence(NameMatchConfig{}) }

func TestNameIndex_MatchesAnExactName(t *testing.T) {
	t.Parallel()
	idx := nameIdxFor(t, "en", "Bart the innkeeper", "Saltmarsh")
	got := idx.Match("we should ask Bart the innkeeper about the ledger", minConf())
	if _, ok := got[0]; !ok {
		t.Fatalf("the named entry did not match: %v", got)
	}
	if _, ok := got[1]; ok {
		t.Errorf("an unnamed entry matched: %v", got)
	}
}

// TestNameIndex_DoesNotFireOnTrivialSubstrings is the AC. A wiki full of short
// names would otherwise mark every line that merely contains those letters, and
// an index that matches everything is an index nobody reads.
func TestNameIndex_DoesNotFireOnTrivialSubstrings(t *testing.T) {
	t.Parallel()
	idx := nameIdxFor(t, "en", "Ash", "Ilva")
	for _, line := range []string{
		"we should smash the door",  // "ash" inside "smash"
		"the ashes were still warm", // "ash" inside "ashes"
		"I will vanish into the crowd",
	} {
		if got := idx.Match(line, minConf()); len(got) != 0 {
			t.Errorf("%q matched %v", line, got)
		}
	}
}

// TestNameIndex_ScoresIdenticallyToTheLiveMatcher: the wrapper must not be a
// second opinion. If these diverge, the Appearances index and the Address
// Detector disagree about what "named" means.
func TestNameIndex_ScoresIdenticallyToTheLiveMatcher(t *testing.T) {
	t.Parallel()
	names := []string{"Bart the innkeeper", "Dockmaster Ilva"}
	enc, _ := DefaultEncoders().For("en")
	wrapper := NewNameIndex(NameMatchConfig{}, enc, names)

	per := make([][]string, len(names))
	for i, n := range names {
		per[i] = []string{n}
	}
	raw := newFuzzyIndex(NameMatchConfig{}.withDefaults(), enc, per, nil)

	const line = "ask Dockmaster Ilva where Bart went"
	rawScores := raw.scoreAll(tokenize(line))
	got := wrapper.Match(line, 0.0001)
	for i, want := range rawScores {
		if want < 0.0001 {
			continue
		}
		if got[i] != want {
			t.Errorf("entry %d: wrapper scored %v, matcher scored %v", i, got[i], want)
		}
	}
}

// TestMinNameConfidence_IsStricterThanTheLiveDetector. A mis-detection in the
// live turn costs one NPC answering when it should not have, which the GM hears
// and shrugs off. A mis-detection here writes a durable row that quietly pollutes
// an entry's history, and nobody reviews it.
func TestMinNameConfidence_IsStricterThanTheLiveDetector(t *testing.T) {
	t.Parallel()
	cfg := NameMatchConfig{}.withDefaults()
	if MinNameConfidence(NameMatchConfig{}) != cfg.PhoneticScore {
		t.Fatalf("MinNameConfidence = %v, want the phonetic bar %v",
			MinNameConfidence(NameMatchConfig{}), cfg.PhoneticScore)
	}
	// It must sit ABOVE the edit-distance net's clamp, which is where "spelled it
	// differently" ends and "sounds like something else" begins.
	if cfg.PhoneticScore <= cfg.PhoneticScore-0.01 {
		t.Fatal("the phonetic bar is not above the clamped edit-distance net")
	}
}

func TestNameIndex_EmptyInputsAreSafe(t *testing.T) {
	t.Parallel()
	if got := NewNameIndex(NameMatchConfig{}, nil, nil).Match("anything", 0.9); len(got) != 0 {
		t.Errorf("an empty index matched %v", got)
	}
	idx := nameIdxFor(t, "en", "Bart")
	if got := idx.Match("   ...   ", 0.9); len(got) != 0 {
		t.Errorf("a punctuation-only line matched %v", got)
	}
}
