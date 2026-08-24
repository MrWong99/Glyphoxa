package textnorm_test

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/textnorm"
)

// benchUtterances mirror the two live call sites: ADR-0042's speculation-cache
// key (players' spoken questions, DE and EN, punctuation from the STT) and the
// #411 proposal dedup (LLM-drafted fact sentences). Sized like real turns, not
// microstrings, so per-rune dispatch dominates the way it does in production.
var benchUtterances = []string{
	"Wer ist eigentlich dieser Händler am Nordtor, und was verkauft er?",
	"Okay, so... we head back to the Rusty Flagon — wait, does Grimnir still owe us money?!",
	"Die Gruppe folgt dem unterirdischen Fluss bis zur alten Zwergenbrücke; dort lagern sie.",
	"The lich's phylactery is hidden beneath the chapel (the one we burned down in session twelve).",
}

// BenchmarkNormalize is the baseline for the repo's shared text-equivalence
// fold — a pure per-rune loop whose cost every recall speculation hit and every
// remember_knowledge dedup pays. One iteration normalizes one realistic
// utterance, round-robin, so the number reads as "per normalize call".
func BenchmarkNormalize(b *testing.B) {
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		textnorm.Normalize(benchUtterances[i%len(benchUtterances)])
		i++
	}
}
