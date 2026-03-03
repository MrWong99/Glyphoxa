package llmcorrect

import (
	"strings"

	"github.com/antzucaro/matchr"
)

// plausibilityThreshold is the minimum Jaro-Winkler score required for a
// correction span to be accepted. Set at 0.70 to cleanly separate legitimate
// phonetic corrections (typically ≥0.84) from hallucinated replacements where
// the LLM substitutes unrelated words with entity names (typically ≤0.65).
const plausibilityThreshold = 0.70

// plausible returns true when the original span bears sufficient phonetic
// resemblance to the corrected span to be a plausible entity-name correction.
// This rejects hallucinated corrections where the LLM replaced unrelated
// words with entity names.
func plausible(original, corrected string) bool {
	origLower := strings.ToLower(original)
	corrLower := strings.ToLower(corrected)

	// Strategy 1: full-string comparison.
	best := matchr.JaroWinkler(origLower, corrLower, false)

	// Strategy 2: space-stripped comparison (catches multi-word → single-word).
	origConcat := strings.ReplaceAll(origLower, " ", "")
	corrConcat := strings.ReplaceAll(corrLower, " ", "")
	if s := matchr.JaroWinkler(origConcat, corrConcat, false); s > best {
		best = s
	}

	// Strategy 3: best pairwise token comparison. Skip very short tokens
	// (≤3 chars) because articles, pronouns, and prepositions like "die",
	// "der", "the", "ich" produce false-positive matches across unrelated
	// spans.
	origTokens := strings.Fields(origLower)
	corrTokens := strings.Fields(corrLower)
	for _, ot := range origTokens {
		if len(ot) <= 3 {
			continue
		}
		for _, ct := range corrTokens {
			if len(ct) <= 3 {
				continue
			}
			if s := matchr.JaroWinkler(ot, ct, false); s > best {
				best = s
			}
		}
	}

	return best >= plausibilityThreshold
}

// indexPair maps a token index in the original sequence to the corresponding
// index in the corrected sequence.
type indexPair struct {
	origIdx int
	corrIdx int
}

// changeSpan represents a contiguous region that differs between the original
// and corrected token sequences.
type changeSpan struct {
	origTokens []string
	corrTokens []string
}

// tokenLCS computes the longest common subsequence of two token slices and
// returns anchor pairs (indices into a and b) representing common tokens in
// order. Standard O(m×n) DP — token counts are small (transcript sentences).
func tokenLCS(a, b []string) []indexPair {
	m, n := len(a), len(b)
	if m == 0 || n == 0 {
		return nil
	}

	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	lcsLen := dp[m][n]
	if lcsLen == 0 {
		return nil
	}

	anchors := make([]indexPair, lcsLen)
	i, j, k := m, n, lcsLen-1
	for i > 0 && j > 0 {
		if a[i-1] == b[j-1] {
			anchors[k] = indexPair{origIdx: i - 1, corrIdx: j - 1}
			i--
			j--
			k--
		} else if dp[i-1][j] >= dp[i][j-1] {
			i--
		} else {
			j--
		}
	}
	return anchors
}

// extractChangeSpans walks the anchor list and collects gaps between anchored
// (unchanged) tokens. Each gap is a changeSpan representing a region that
// differs between the two token sequences.
func extractChangeSpans(orig, corr []string, anchors []indexPair) []changeSpan {
	var spans []changeSpan
	oi, ci := 0, 0
	for _, a := range anchors {
		if oi < a.origIdx || ci < a.corrIdx {
			spans = append(spans, changeSpan{
				origTokens: orig[oi:a.origIdx],
				corrTokens: corr[ci:a.corrIdx],
			})
		}
		oi = a.origIdx + 1
		ci = a.corrIdx + 1
	}
	if oi < len(orig) || ci < len(corr) {
		spans = append(spans, changeSpan{
			origTokens: orig[oi:],
			corrTokens: corr[ci:],
		})
	}
	return spans
}

// normalizeForLookup lowercases s and strips common trailing punctuation so
// that token spans like "Wispers." match corrections declared as "Wispers".
func normalizeForLookup(s string) string {
	return strings.ToLower(strings.TrimRight(s, ".,;:!?\"')"))
}

// verifyCorrectedText cross-references actual token-level changes between
// original and corrected against the reported corrections list. Any change
// span that does not correspond to a declared correction is reverted to the
// original tokens. Returns the verified text and only the confirmed
// corrections.
func verifyCorrectedText(original, corrected string, corrections []Correction) (string, []Correction) {
	if original == corrected {
		return original, corrections
	}

	origTokens := strings.Fields(original)
	corrTokens := strings.Fields(corrected)

	anchors := tokenLCS(origTokens, corrTokens)
	spans := extractChangeSpans(origTokens, corrTokens, anchors)

	type corrKey struct{ orig, corr string }
	lookup := make(map[corrKey]Correction, len(corrections))
	for _, c := range corrections {
		lookup[corrKey{normalizeForLookup(c.Original), normalizeForLookup(c.Corrected)}] = c
	}

	var result []string
	var verified []Correction
	oi, ci, spanIdx := 0, 0, 0

	for _, a := range anchors {
		if oi < a.origIdx || ci < a.corrIdx {
			span := spans[spanIdx]
			spanIdx++
			key := corrKey{
				normalizeForLookup(strings.Join(span.origTokens, " ")),
				normalizeForLookup(strings.Join(span.corrTokens, " ")),
			}
			if c, ok := lookup[key]; ok && plausible(strings.Join(span.origTokens, " "), strings.Join(span.corrTokens, " ")) {
				result = append(result, span.corrTokens...)
				verified = append(verified, c)
			} else {
				result = append(result, span.origTokens...)
			}
		}
		result = append(result, origTokens[a.origIdx])
		oi = a.origIdx + 1
		ci = a.corrIdx + 1
	}

	if oi < len(origTokens) || ci < len(corrTokens) {
		span := spans[spanIdx]
		key := corrKey{
			normalizeForLookup(strings.Join(span.origTokens, " ")),
			normalizeForLookup(strings.Join(span.corrTokens, " ")),
		}
		if c, ok := lookup[key]; ok && plausible(strings.Join(span.origTokens, " "), strings.Join(span.corrTokens, " ")) {
			result = append(result, span.corrTokens...)
			verified = append(verified, c)
		} else {
			result = append(result, span.origTokens...)
		}
	}

	return strings.Join(result, " "), verified
}
