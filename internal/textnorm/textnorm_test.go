package textnorm_test

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/textnorm"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"lowercase", "Hello World", "hello world"},
		{"trailing punctuation dropped", "Gesa ist die Schwester.", "gesa ist die schwester"},
		{"collapse whitespace", "well   now\tthen", "well now then"},
		{"trim edges", "  hi  ", "hi"},
		{"punctuation is a soft boundary", "well,now", "well now"},
		{"casefold plus punctuation equal", "Gesa ist die Schwester!", "gesa ist die schwester"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := textnorm.Normalize(tc.in); got != tc.want {
				t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
