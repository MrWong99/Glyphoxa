package address

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

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
	compared := 0
	for i, want := range rawScores {
		if want < 0.0001 {
			continue
		}
		compared++
		if got[i] != want {
			t.Errorf("entry %d: wrapper scored %v, matcher scored %v", i, got[i], want)
		}
	}
	// Every comparison above is skipped when the live matcher scores nothing, so
	// without this the test would report agreement it never actually checked. And
	// the loop only walks what the live matcher scored, so a wrapper that invented
	// an entry of its own would slip past it — divergence has two directions.
	if compared == 0 {
		t.Fatalf("the live matcher scored nothing on %q; this compared no entries", line)
	}
	if len(got) != compared {
		t.Errorf("wrapper returned %d entries, the live matcher scored %d: %v", len(got), compared, got)
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
	// And it must sit ABOVE the similarity the live Address Detector accepts as a
	// name hit — the bar below which a turn may still route to an NPC but a
	// durable row must not be written. Read from the shipped stack rather than
	// hard-coded, so retuning the live signal up to this bar fails HERE: the
	// durable index going no stricter than the disposable turn is the whole point.
	live, ok := liveNameThreshold()
	if !ok {
		t.Fatal("the default heuristic stack has no NameMatch to be stricter than")
	}
	if got := MinNameConfidence(NameMatchConfig{}); got <= live {
		t.Fatalf("MinNameConfidence = %v, want strictly above the live detector's name bar %v", got, live)
	}
	// The shipped stack is what a default Matcher gates on, but only because
	// NewMatcher fills a nil stack from it. Pin that too, or the bar compared
	// against above stops being the bar the live turn actually applies.
	npc := Agent{Target: voiceevent.AddressTarget{AgentID: "npc-bart", AgentRole: "character", Name: "Bart"}}
	if got := NewMatcher(Config{Language: "en"}, npc).nameThreshold(); got != live {
		t.Errorf("a default Matcher gates name hits at %v, the shipped stack says %v", got, live)
	}
}

// liveNameThreshold is the similarity the live Address Detector demands of a name
// hit: the Threshold of the [NameMatch] heuristic in the shipped stack, which is
// the value [Matcher.nameThreshold] gates on. Reported absent (false) rather than
// defaulted, so a stack without a NameMatch cannot pass the comparison by
// accident.
func liveNameThreshold() (float64, bool) {
	for _, h := range DefaultHeuristics() {
		if nm, ok := h.(NameMatch); ok {
			return nm.Threshold, true
		}
	}
	return 0, false
}

// TestMinNameConfidence_ExcludesTheEditNet pins the OTHER half of the bar, and
// the one [MinNameConfidence]'s doc comment actually promises: "an exact match
// and a phonetic one, and nothing softer". That promise rests entirely on the
// clamp in [fuzzyIndex.similarity] holding the edit-distance net just below
// PhoneticScore whenever an encoder is present — with the clamp gone, a name
// reached by pure edit distance scores exactly the bar and lands in the durable
// index. "Rockmaster" is one edit from "Dockmaster" across ten runes, which is
// that worst case: a different word, not a different spelling of the same one.
func TestMinNameConfidence_ExcludesTheEditNet(t *testing.T) {
	t.Parallel()
	idx := nameIdxFor(t, "en", "Dockmaster")
	const line = "ask Rockmaster about the ledger"
	if got := idx.Match(line, minConf()); len(got) != 0 {
		t.Errorf("an edit-net-only match cleared the durable bar: %q matched %v", line, got)
	}
	// It must be a near miss rather than a non-match, or this passes for the wrong
	// reason — an index that stopped matching anything would satisfy the check
	// above while testing nothing about the clamp.
	if got := idx.Match(line, 0.0001); len(got) == 0 {
		t.Fatalf("%q did not reach the entry at all; this no longer probes the clamp", line)
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
