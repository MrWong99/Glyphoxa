package address

import (
	"strings"
	"unicode"

	"github.com/antzucaro/matchr"
)

// NameMatchConfig tunes the fuzzy name engine. Every field has a sensible
// zero-aware default applied by [NameMatchConfig.withDefaults], so a caller
// can set only the knobs they care about.
type NameMatchConfig struct {
	// MaxWindow is the largest number of adjacent utterance tokens joined into
	// one candidate before encoding, so a multi-token mishearing ("grim jaw")
	// can match a single-token name ("Grimjaw"). It is raised automatically to
	// fit the longest registered name, so this only sets the floor. Default 3.
	MaxWindow int

	// MinRunes is the rune-length floor below which a candidate (and the name
	// it is compared against) must match exactly. Short tokens are where
	// articles and fillers collide with names under fuzzy rules ("an" ≈ "Ann"),
	// so anything shorter is exact-only. Default 4.
	MinRunes int

	// MaxEditDistance bounds the Damerau-Levenshtein net that runs on a
	// phonetic miss: candidates farther than this many edits from a name never
	// match through the edit net. Default 2.
	MaxEditDistance int

	// PhoneticScore is the similarity awarded when a candidate and a name share
	// a phonetic code but are not byte-identical. It sits below the exact-match
	// score of 1.0 so a clean match always outranks a homophone. Default 0.9.
	PhoneticScore float64
}

func (c NameMatchConfig) withDefaults() NameMatchConfig {
	if c.MaxWindow <= 0 {
		c.MaxWindow = 3
	}
	if c.MinRunes <= 0 {
		c.MinRunes = 4
	}
	if c.MaxEditDistance <= 0 {
		c.MaxEditDistance = 2
	}
	if c.PhoneticScore <= 0 {
		c.PhoneticScore = 0.9
	}
	return c
}

// truncationAliasScore is the similarity a derived STT-truncation alias earns on
// an exact, utterance-initial hit (#197). It sits just below an exact name (1.0)
// so a genuinely named Agent always outranks a truncation collision, and above
// the phonetic tier (0.9) so a truncated name still clears the address
// threshold. See [fuzzyIndex.scoreAll] and ADR-0024.
const truncationAliasScore = 0.99

// nameEntry is one matchable name (an Agent's display Name or an alias),
// pre-tokenized and pre-joined at build time so matching does no per-call
// string work beyond tokenizing the utterance.
type nameEntry struct {
	agentIdx int    // index into the matcher's agents slice
	joined   string // tokens concatenated, lowercased: "Grim Jaw" → "grimjaw"
	tokens   int    // token count, used to size the window search
	// initialOnly marks a derived STT-truncation alias (#197): it is matched
	// EXACT-ONLY and only when the candidate window starts the utterance (token
	// 0), and it never reaches the phonetic/edit fuzzy tiers. Configured names
	// and aliases stay position-free with the full fuzzy chain.
	initialOnly bool
}

// fuzzyIndex is the pure, immutable name index the [Matcher] builds once from
// its Agents. Scoring a transcript against it allocates only the utterance's
// token slice and is otherwise read-only, so it is safe to share across
// goroutines and trivial to unit-test in isolation.
type fuzzyIndex struct {
	cfg     NameMatchConfig
	enc     Encoder // may be nil: edit-distance net only
	entries []nameEntry
	codes   []string // phonetic code per entry, parallel to entries ("" if no encoder)
	window  int      // effective max window: max(cfg.MaxWindow, longest name)
}

// newFuzzyIndex builds the index for names, where names[i] is the list of
// matchable strings (primary Name + aliases) for agent i, and truncations[i] is
// the derived STT-truncation aliases for the same agent (#197), matched
// exact-only at the utterance start. truncations is parallel to names and may be
// nil (no derived aliases). enc may be nil, in which case matching relies on the
// edit-distance net alone (ADR-0024: a language with no registered phonetic
// encoder).
func newFuzzyIndex(cfg NameMatchConfig, enc Encoder, names [][]string, truncations [][]string) *fuzzyIndex {
	cfg = cfg.withDefaults()
	idx := &fuzzyIndex{cfg: cfg, enc: enc, window: cfg.MaxWindow}
	add := func(agentIdx int, name string, initialOnly bool) {
		toks := tokenize(name)
		if len(toks) == 0 {
			return
		}
		joined := strings.Join(toks, "")
		idx.entries = append(idx.entries, nameEntry{
			agentIdx:    agentIdx,
			joined:      joined,
			tokens:      len(toks),
			initialOnly: initialOnly,
		})
		// Derived aliases never reach the phonetic tier, so they carry no code;
		// codes stays parallel to entries either way.
		if enc != nil && !initialOnly {
			idx.codes = append(idx.codes, enc.Encode(joined))
		} else {
			idx.codes = append(idx.codes, "")
		}
		if len(toks) > idx.window {
			idx.window = len(toks)
		}
	}
	for agentIdx, agentNames := range names {
		for _, name := range agentNames {
			add(agentIdx, name, false)
		}
	}
	for agentIdx, aliases := range truncations {
		for _, alias := range aliases {
			add(agentIdx, alias, true)
		}
	}
	return idx
}

// scoreAll returns the best name-match similarity in [0,1] for every agent,
// keyed by the agent's index. An agent absent from the map (or present with 0)
// was not named. words is the tokenized utterance.
func (idx *fuzzyIndex) scoreAll(words []string) map[int]float64 {
	best, _ := idx.score(words)
	return best
}

// score returns, for every agent, its best name-match similarity in [0,1] (best,
// keyed by agent index) AND the earliest utterance token position at which that
// best similarity was reached (positions). positions encodes the ADDRESSEE
// convention the matcher's tie-break uses: among equal-similarity hits the one
// spoken FIRST is the likelier addressee, so a name at the vocative head of the
// utterance ("Gott, was hat Gesa gesagt?") outranks a same-tier name mentioned
// later as the topic. positions is only meaningful for agents with best > 0. words
// is the tokenized utterance.
func (idx *fuzzyIndex) score(words []string) (best map[int]float64, positions map[int]int) {
	best = map[int]float64{}
	positions = map[int]int{}
	if len(idx.entries) == 0 || len(words) == 0 {
		return best, positions
	}

	// Pre-encode every candidate window once, then compare each against every
	// name entry. Windows of 1..window adjacent tokens are joined so a name
	// heard as several tokens still lines up with a single-token name.
	type candidate struct {
		start  int // index of the first token in this window, 0 == utterance start
		joined string
		code   string
	}
	var cands []candidate
	for start := range words {
		var sb strings.Builder
		for n := 1; n <= idx.window && start+n <= len(words); n++ {
			sb.WriteString(words[start+n-1])
			joined := sb.String()
			c := candidate{start: start, joined: joined}
			if idx.enc != nil {
				c.code = idx.enc.Encode(joined)
			}
			cands = append(cands, c)
		}
	}

	for ei, entry := range idx.entries {
		code := idx.codes[ei]
		for _, c := range cands {
			var s float64
			if entry.initialOnly {
				// Derived STT-truncation alias (#197): exact byte-match only, and
				// only when the window opens the utterance. It never reaches the
				// phonetic/edit tiers, so a near-miss earns nothing.
				if c.start == 0 && c.joined == entry.joined {
					s = truncationAliasScore
				}
			} else {
				s = idx.similarity(c.joined, c.code, entry.joined, code)
			}
			if s <= 0 {
				continue
			}
			if s > best[entry.agentIdx] {
				best[entry.agentIdx] = s
				positions[entry.agentIdx] = c.start
			} else if s == best[entry.agentIdx] && c.start < positions[entry.agentIdx] {
				// Same best similarity reached earlier in the utterance: keep the
				// earliest position so the addressee-position tie-break sees the
				// vocative-head occurrence, not a later topic mention.
				positions[entry.agentIdx] = c.start
			}
		}
	}
	return best, positions
}

// similarity scores one candidate window against one name entry. The tiers,
// in order: exact byte-equality (1.0); for inputs long enough to fuzz, a shared
// phonetic code ([NameMatchConfig.PhoneticScore]); otherwise the edit-distance
// net, scoring 1 − d/maxLen for distances within the bound. Inputs shorter than
// the rune floor are exact-only and never reach the fuzzy tiers.
func (idx *fuzzyIndex) similarity(candJoined, candCode, nameJoined, nameCode string) float64 {
	if candJoined == nameJoined {
		return 1.0
	}
	// The rune floor guards the shorter of the two strings: a 3-rune name must
	// be matched exactly even by a long candidate, and vice versa.
	if runeLen(candJoined) < idx.cfg.MinRunes || runeLen(nameJoined) < idx.cfg.MinRunes {
		return 0
	}
	if idx.enc != nil && candCode != "" && candCode == nameCode {
		return idx.cfg.PhoneticScore
	}
	d := matchr.DamerauLevenshtein(candJoined, nameJoined)
	if d > idx.cfg.MaxEditDistance {
		return 0
	}
	maxLen := runeLen(candJoined)
	if l := runeLen(nameJoined); l > maxLen {
		maxLen = l
	}
	if maxLen == 0 {
		return 0
	}
	score := 1 - float64(d)/float64(maxLen)
	// Keep the edit net strictly below a phonetic hit so the tiers stay ordered.
	if idx.enc != nil && score >= idx.cfg.PhoneticScore {
		score = idx.cfg.PhoneticScore - 0.01
	}
	return score
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// isVowel reports whether r is a vocalic onset for truncation-alias derivation.
// German umlauts count because the Campaign Language may be de (#199); y is a
// consonant. A name beginning with a vowel derives no alias, since dropping a
// leading vowel is not the STT-truncation failure mode this guards.
func isVowel(r rune) bool {
	switch unicode.ToLower(r) {
	case 'a', 'e', 'i', 'o', 'u', 'ä', 'ö', 'ü':
		return true
	}
	return false
}

// DeriveTruncationAliases returns, per name, the STT-truncation form STT tends to
// produce by dropping the leading consonant — "Bart" heard as "art" (#197, live
// turn 47aecba4be320d54). A form is derived only when the name begins with a
// consonant letter and the remainder is itself usable: its first rune is a
// letter (so "D20"→"20" is rejected) and it is at least two runes once tokenized
// (so "Bo"→"o" is rejected). Vowel-initial names ("Anna", the configured alias
// "innkeeper") derive nothing. Results are deduped. The caller feeds these to
// [Agent.TruncationAliases], where they are matched EXACT-ONLY at the utterance
// start and never reach the phonetic/edit tiers (see [fuzzyIndex.scoreAll]).
func DeriveTruncationAliases(names ...string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, name := range names {
		runes := []rune(strings.TrimSpace(name))
		if len(runes) == 0 {
			continue
		}
		if first := runes[0]; !unicode.IsLetter(first) || isVowel(first) {
			continue
		}
		candidate := string(runes[1:])
		rem := []rune(strings.TrimSpace(candidate))
		if len(rem) == 0 || !unicode.IsLetter(rem[0]) {
			continue // remainder does not start with a letter (guards "D20"→"20")
		}
		if runeLen(strings.Join(tokenize(candidate), "")) < 2 {
			continue // remainder too short to be a name (guards "Bo"→"o")
		}
		if _, dup := seen[candidate]; dup {
			continue
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}
	return out
}

// tokenize lowercases s and splits it into maximal runs of letters and digits,
// dropping all other runes. It is the shared normalizer for both names (at
// build time) and utterances (at match time), so "Bart, what's up?" and the
// name "Bart" tokenize compatibly.
func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}
