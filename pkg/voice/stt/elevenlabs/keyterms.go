package elevenlabs

import (
	"strings"
	"unicode"
)

// Keyterm limits (#701, ADR-0042 amendment 2026-11-22). The realtime endpoint
// enforces the list limits HARD: a 51st term, or more than 1000 characters in
// total, is answered with an invalid_request frame instead of session_started
// and the session never starts. The batch endpoint documents looser list limits
// (1000 terms) but bills a 20 s minimum per request past 100 terms, so one cap
// serves both paths.
//
// Measured at the boundary 2026-11-22 (ADR-0042): realtime counts the total in
// characters and accepts exactly 1000; batch enforces the per-term limits with an
// HTTP 400 (50 characters; 4 spaces, i.e. 5 whitespace-separated words) and has
// no total. Nothing that passes [Keyterms] was refused by either. Should the
// provider refuse a list all the same, the client stops sending keyterms rather
// than fail ([Client.refuseKeyterms]).
const (
	// MaxKeyterms is the most terms either path sends.
	MaxKeyterms = 50
	// MaxKeytermsChars bounds the summed length of the terms sent. It is counted
	// in bytes, which can only be stricter than the provider's character count.
	MaxKeytermsChars = 1000
	// MaxKeytermChars and MaxKeytermWords bound one term (the batch endpoint's
	// documented per-term limits).
	MaxKeytermChars = 50
	MaxKeytermWords = 5
)

// keytermForbidden are the characters the provider does not accept in a keyterm.
const keytermForbidden = `<>{}[]\`

// Keyterms returns the keyterm list this adapter sends for terms, on the batch
// and the realtime path alike. It is deterministic and order-preserving, so a
// caller that lists its terms by priority keeps the important ones:
//
//  1. each term loses the forbidden characters and surrounding or repeated
//     whitespace, and is dropped if no letter or digit is left (punctuation or
//     invisible characters alone are nothing a recognizer can hear);
//  2. each term is cut to [MaxKeytermWords] words and [MaxKeytermChars]
//     characters;
//  3. a term equal to an earlier one, ignoring case, is dropped;
//  4. the list ends before the first term that would exceed [MaxKeyterms] terms
//     or [MaxKeytermsChars] in total.
func Keyterms(terms []string) []string {
	var out []string
	seen := make(map[string]struct{}, len(terms))
	total := 0
	for _, raw := range terms {
		term := sanitizeKeyterm(raw)
		if term == "" {
			continue
		}
		key := strings.ToLower(term)
		if _, dup := seen[key]; dup {
			continue
		}
		if len(out) == MaxKeyterms || total+len(term) > MaxKeytermsChars {
			break
		}
		seen[key] = struct{}{}
		total += len(term)
		out = append(out, term)
	}
	return out
}

func sanitizeKeyterm(raw string) string {
	stripped := strings.Map(func(r rune) rune {
		if strings.ContainsRune(keytermForbidden, r) {
			return -1
		}
		return r
	}, raw)
	words := strings.Fields(stripped)
	if len(words) > MaxKeytermWords {
		words = words[:MaxKeytermWords]
	}
	term := strings.Join(words, " ")
	if runes := []rune(term); len(runes) > MaxKeytermChars {
		term = strings.TrimSpace(string(runes[:MaxKeytermChars]))
	}
	if !strings.ContainsFunc(term, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		return ""
	}
	return term
}
