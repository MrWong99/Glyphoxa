package recall

import "github.com/MrWong99/Glyphoxa/internal/textnorm"

// normalize folds an utterance to its speculation-cache comparison form: lower
// case, punctuation stripped, whitespace collapsed to single spaces and trimmed.
// It is the self-heal key of ADR-0042 — the [STTFinal] text is normalized and
// matched against the normalized partial a speculation prefetched on, so casing,
// trailing punctuation, and interim-whitespace jitter between the partial and the
// final do not defeat the cache. It delegates to the single shared normaliser in
// internal/textnorm so the recall cache and the #411 proposal dedup never drift.
func normalize(s string) string { return textnorm.Normalize(s) }
