package address

// NameIndex exports the address matcher's name-matching core for callers outside
// the voice turn (#545).
//
// The Appearances index needs exactly one thing this package already does well:
// "does this sentence name any of these entries", with Campaign Language phonetic
// tolerance and without firing on trivial substrings. Building a second matcher
// for it would be the classic mistake — two definitions of "was this named",
// drifting apart, one of them untested against the real STT output that shaped
// ADR-0024's thresholds.
//
// So this is a thin wrapper, not a copy. Everything below delegates to the same
// unexported fuzzyIndex the live Address Detector runs on, which means an
// improvement to the matcher improves both, and a regression breaks both tests.

// NameIndex matches names in free text. Build it with [NewNameIndex]; it is
// immutable and safe for concurrent use.
type NameIndex struct {
	idx   *fuzzyIndex
	names []string
}

// NewNameIndex builds an index over `names`, one entry per name, matched with the
// given Campaign Language's phonetic encoder.
//
// A language with no registered encoder gets `enc == nil`, which the matcher
// documents as "edit-distance net only" — and that changes what a threshold
// MEANS, so callers that care should pick their threshold from
// [NameMatchConfig.PhoneticScore] rather than hard-coding a number. See
// [MinNameConfidence].
func NewNameIndex(cfg NameMatchConfig, enc Encoder, names []string) *NameIndex {
	per := make([][]string, len(names))
	for i, n := range names {
		per[i] = []string{n}
	}
	return &NameIndex{idx: newFuzzyIndex(cfg, enc, per, nil), names: names}
}

// Match returns the index of every name the text names at or above `min`,
// together with its similarity. An empty result means the text named nothing,
// which is the common case and is not an error.
//
// The text is tokenised by the SAME normalizer the names were built with, so
// "Bart, what's up?" and the entry "Bart" compare compatibly.
func (n *NameIndex) Match(text string, min float64) map[int]float64 {
	if n == nil || len(n.names) == 0 {
		return nil
	}
	words := tokenize(text)
	if len(words) == 0 {
		return nil
	}
	out := map[int]float64{}
	for i, score := range n.idx.scoreAll(words) {
		if score >= min && i >= 0 && i < len(n.names) {
			out[i] = score
		}
	}
	return out
}

// MinNameConfidence is the similarity an out-of-turn name match must reach.
//
// It is deliberately STRICTER than the live Address Detector's threshold. A
// mis-detection there costs one NPC answering when it should not have, which the
// GM hears immediately and can shrug off; a mis-detection here writes a durable
// row that quietly pollutes an entry's history, and nobody reviews it. So this
// admits an exact match and a phonetic one, and nothing softer:
// [NameMatchConfig.PhoneticScore] is where the matcher clamps the edit-distance
// net to just BELOW when an encoder is present, which makes it exactly the
// "spelled it differently, said the same word" line.
//
// With no encoder registered for the Campaign Language, the edit-distance net is
// unclamped and this same number is a stricter, near-exact bar. That asymmetry is
// intended: less phonetic knowledge should buy less tolerance, not more.
func MinNameConfidence(cfg NameMatchConfig) float64 {
	return cfg.withDefaults().PhoneticScore
}
