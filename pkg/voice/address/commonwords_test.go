package address_test

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
)

// TestIsCommonWord: the everyday-word list that keeps the fuzzy tiers off
// ordinary speech is readable from outside the package, so the STT keyterm list
// can leave out a name that is also an ordinary word (#701) without a second
// copy of the vocabulary.
func TestIsCommonWord(t *testing.T) {
	cases := []struct {
		lang, term string
		want       bool
	}{
		{"de", "Wolf", true},
		{"de", "  wolf! ", true},
		{"de-DE", "MEHR", true},
		{"en", "great", true},
		{"de", "Flo", false},
		{"de", "great", false},    // English word, German campaign
		{"de", "Der Wolf", false}, // a multi-word name is not an everyday word
		{"fr", "wolf", false},     // no list for the language
		{"", "wolf", false},       // no language
		{"de", "", false},
	}
	for _, c := range cases {
		if got := address.IsCommonWord(c.lang, c.term); got != c.want {
			t.Errorf("IsCommonWord(%q, %q) = %v, want %v", c.lang, c.term, got, c.want)
		}
	}
}
