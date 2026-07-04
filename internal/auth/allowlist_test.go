package auth_test

import (
	"slices"
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

// Malformed surfaces entries that can never match a Discord snowflake (digits
// only), so the boot preflight (#112) can fail loud instead of letting a pasted
// username silently lock the operator out.
func TestOperatorAllowlistMalformed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{name: "empty", in: "", want: nil},
		{name: "all numeric", in: "77, 88 99", want: nil},
		{name: "pasted username", in: "MrWong99,770000000000000000", want: []string{"MrWong99"}},
		{name: "quoted value", in: `"123",456`, want: []string{`"123"`}},
		{name: "sorted output", in: "zz 123 abc", want: []string{"abc", "zz"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := auth.ParseOperatorAllowlist(tc.in).Malformed()
			if !slices.Equal(got, tc.want) {
				t.Errorf("Malformed() = %q, want %q", got, tc.want)
			}
		})
	}
}
