package auth_test

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/auth"
)

func TestParseOperatorAllowlist(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want []string // snowflakes that MUST be Contains()==true
		deny []string // snowflakes that MUST be Contains()==false
		len  int
	}{
		{name: "empty", in: "", deny: []string{"", "77"}, len: 0},
		{name: "single", in: "77", want: []string{"77"}, deny: []string{"78"}, len: 1},
		{name: "comma", in: "77,88,99", want: []string{"77", "88", "99"}, len: 3},
		{name: "whitespace", in: "77 88\t99", want: []string{"77", "88", "99"}, len: 3},
		{name: "mixed comma+whitespace", in: "77, 88 ,99", want: []string{"77", "88", "99"}, len: 3},
		{name: "surrounding whitespace", in: "  77  ", want: []string{"77"}, len: 1},
		{name: "empty entries dropped", in: "77,,  ,88,", want: []string{"77", "88"}, deny: []string{""}, len: 2},
		{name: "newlines", in: "77\n88\r\n99", want: []string{"77", "88", "99"}, len: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := auth.ParseOperatorAllowlist(tc.in)
			if a.Len() != tc.len {
				t.Errorf("Len() = %d, want %d", a.Len(), tc.len)
			}
			for _, id := range tc.want {
				if !a.Contains(id) {
					t.Errorf("Contains(%q) = false, want true", id)
				}
			}
			for _, id := range tc.deny {
				if a.Contains(id) {
					t.Errorf("Contains(%q) = true, want false", id)
				}
			}
		})
	}
}
