package storage_test

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestBuildTSQuery is #131's keyless sanitizer contract (ADR-0008 tsvector
// search): a raw GM query string is turned into a safe 'simple' to_tsquery input.
// tsquery operator characters are neutralized (they become term separators, never
// injected operators), whitespace splits terms, terms AND-join, and only the LAST
// term gets the ":*" prefix marker so typeahead matches a word the GM is still
// typing. An input with nothing left after stripping yields "".
func TestBuildTSQuery(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"single term gets prefix", "bridge", "bridge:*"},
		{"multi term ANDs, prefix on last only", "old stone bridge", "old & stone & bridge:*"},
		{"tsquery specials are stripped, not injected", "bridge & castle | keep!", "bridge & castle & keep:*"},
		{"colon and star specials become separators", "foo:*bar", "foo & bar:*"},
		{"parens and bang stripped", "(dragon) !ogre", "dragon & ogre:*"},
		{"unicode letters survive (German campaigns)", "Köln brüc", "Köln & brüc:*"},
		{"digits survive", "room 12b", "room & 12b:*"},
		{"all-punctuation input yields empty", "!@#$%^&*()", ""},
		{"whitespace-only yields empty", "   \t\n ", ""},
		{"empty yields empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := storage.BuildTSQuery(tc.in); got != tc.want {
				t.Errorf("BuildTSQuery(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
