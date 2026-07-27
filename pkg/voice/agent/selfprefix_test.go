package agent

import "testing"

// TestStripSelfPrefix covers the label forms a model actually produces when it
// imitates Hot Context's "Name: text" grammar in its OWN reply — and, just as
// importantly, the lines that must survive untouched.
func TestStripSelfPrefix(t *testing.T) {
	for _, tc := range []struct {
		name string
		self string
		in   string
		want string
	}{
		{name: "plain", self: "Mira", in: "Mira: Good evening.", want: "Good evening."},
		{name: "no space after colon", self: "Mira", in: "Mira:Good evening.", want: "Good evening."},
		{name: "leading space", self: "Mira", in: "  Mira: Good evening.", want: "Good evening."},
		{name: "case insensitive", self: "Mira", in: "MIRA: Good evening.", want: "Good evening."},
		{name: "bold markdown", self: "Mira", in: "**Mira**: Good evening.", want: "Good evening."},
		{name: "italic markdown", self: "Mira", in: "*Mira*: Good evening.", want: "Good evening."},
		{name: "emphasis around whole label", self: "Mira", in: "*Mira:* Good evening.", want: "Good evening."},
		{name: "bracketed", self: "Mira", in: "[Mira]: Good evening.", want: "Good evening."},
		{name: "quoted", self: "Mira", in: `"Mira": Good evening.`, want: "Good evening."},
		{name: "repeated", self: "Mira", in: "Mira: Mira: Good evening.", want: "Good evening."},
		{name: "multi-word name", self: "Jule Brandt", in: "Jule Brandt: Guten Abend.", want: "Guten Abend."},
		{name: "fullwidth colon", self: "Mira", in: "Mira： Good evening.", want: "Good evening."},
		{name: "behind an audio tag", self: "Mira", in: "[laughs] Mira: Good evening.", want: "[laughs] Good evening."},
		{name: "behind a two-word audio tag", self: "Mira", in: "[warmly, softly] Mira: Good evening.", want: "[warmly, softly] Good evening."},

		// Must NOT strip.
		{name: "no colon", self: "Mira", in: "Mira would never do that.", want: "Mira would never do that."},
		{name: "another speaker", self: "Mira", in: "Bart: Good evening.", want: "Bart: Good evening."},
		{name: "name later in line", self: "Mira", in: "As Mira: I refuse.", want: "As Mira: I refuse."},
		{name: "audio tag", self: "Mira", in: "[laughs] Good evening.", want: "[laughs] Good evening."},
		{name: "colon mid-sentence", self: "Mira", in: "Listen: the bridge is out.", want: "Listen: the bridge is out."},
		{name: "no configured name", self: "", in: "Mira: Good evening.", want: "Mira: Good evening."},
		{name: "empty reply", self: "Mira", in: "", want: ""},
		{name: "label only", self: "Mira", in: "Mira:", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripSelfPrefix(tc.self, tc.in); got != tc.want {
				t.Errorf("stripSelfPrefix(%q, %q) = %q, want %q", tc.self, tc.in, got, tc.want)
			}
		})
	}
}
