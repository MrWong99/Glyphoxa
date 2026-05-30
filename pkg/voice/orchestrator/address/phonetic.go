// Package address is the scoring Address Detection matcher (ADR-0024): a
// deterministic, fuzzy, heuristic-scored alternative to the orchestrator's
// minimal whole-word [nameMatcher].
//
// Where the default matcher does a case-insensitive whole-word match on each
// Character NPC's display Name and falls back to the Butler unconditionally,
// this package matches every Agent — Butler included — fuzzily (phonetic +
// edit-distance), then scores each candidate with a stack of pluggable
// [Heuristic]s. An Agent is addressed when its total score crosses a
// configurable threshold; several Agents crossing at once yields the
// multi-target set that drives an Ensemble Turn (ADR-0025).
//
// The whole pipeline is a pure function over a per-utterance [DecisionContext]
// plus the matcher's own conversational state (last addressee, recent words,
// recent interruptions). It uses no LLM and no vendor in the hot path, so it
// stays sub-millisecond and fully unit-testable (ADR-0024 "Why deterministic").
//
// A [Matcher] satisfies the orchestrator's TargetMatcher seam and is wired in
// via orchestrator.WithMatcher; see [Matcher].
package address

import (
	"strings"

	"github.com/antzucaro/matchr"
	gophonetics "gopkg.in/Regis24GmbH/go-phonetics.v3"
)

// Encoder maps a single token to a phonetic code. Two tokens whose codes are
// equal (and non-empty) are treated as phonetically identical by the fuzzy
// matcher, so an STT mishearing that preserves the sound of a name — "bard"
// for "Bart" — still matches. The mapping is the language-specific half of
// fuzzy matching; the universal edit-distance net (ADR-0024) sits behind it
// and needs no Encoder.
type Encoder interface {
	// Encode returns the phonetic code of token. An empty return value means
	// "no code" and never matches another token, even another empty code.
	Encode(token string) string
}

// EncoderFunc adapts a plain func to [Encoder].
type EncoderFunc func(string) string

// Encode implements [Encoder].
func (f EncoderFunc) Encode(token string) string { return f(token) }

// DoubleMetaphone is the English (`en`) phonetic encoder: the primary Double
// Metaphone code (ADR-0024). It is exported so a custom [EncoderRegistry] can
// reuse it for other Latin-script languages that Double Metaphone approximates
// acceptably.
var DoubleMetaphone Encoder = EncoderFunc(func(token string) string {
	primary, _ := matchr.DoubleMetaphone(token)
	return primary
})

// KoelnerPhonetik is the German (`de`) phonetic encoder: the Kölner Phonetik
// code (ADR-0024).
var KoelnerPhonetik Encoder = EncoderFunc(gophonetics.NewPhoneticCode)

// EncoderRegistry resolves a per-language [Encoder]. The Campaign Language
// (CONTEXT.md) selects the scheme: `en` → Double Metaphone, `de` → Kölner
// Phonetik, and further languages register their own. A language with no
// registered Encoder still matches via the edit-distance net alone, so an
// unregistered language degrades rather than failing.
type EncoderRegistry struct {
	byLang map[string]Encoder
}

// NewEncoderRegistry returns an empty registry. Use [DefaultEncoders] for the
// `en`/`de` pair the platform ships with.
func NewEncoderRegistry() *EncoderRegistry {
	return &EncoderRegistry{byLang: map[string]Encoder{}}
}

// DefaultEncoders returns a registry pre-populated with the v1.0 language
// matrix: `en` → [DoubleMetaphone], `de` → [KoelnerPhonetik] (ADR-0024).
func DefaultEncoders() *EncoderRegistry {
	r := NewEncoderRegistry()
	r.Register("en", DoubleMetaphone)
	r.Register("de", KoelnerPhonetik)
	return r
}

// Register binds enc to lang, replacing any previous Encoder for that
// language. lang is normalized the same way [EncoderRegistry.For] normalizes
// its lookup ("en-US" and "EN" both register under "en"), so a BCP-47 tag and
// its bare language subtag are interchangeable. A nil enc unregisters lang.
func (r *EncoderRegistry) Register(lang string, enc Encoder) {
	key := normalizeLang(lang)
	if enc == nil {
		delete(r.byLang, key)
		return
	}
	r.byLang[key] = enc
}

// For returns the [Encoder] registered for lang and whether one was found.
// lang is matched on its primary subtag, so "en", "en-US", and "EN_us" all
// resolve to the same Encoder. A miss is not an error: the caller falls back
// to the edit-distance net.
func (r *EncoderRegistry) For(lang string) (Encoder, bool) {
	enc, ok := r.byLang[normalizeLang(lang)]
	return enc, ok
}

// normalizeLang reduces a BCP-47-ish tag to its lowercase primary subtag:
// "en-US" → "en", "DE" → "de". Region, script, and variant subtags are
// dropped because the phonetic scheme is chosen per language, not per locale.
func normalizeLang(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	for i, r := range lang {
		if r == '-' || r == '_' {
			return lang[:i]
		}
	}
	return lang
}
