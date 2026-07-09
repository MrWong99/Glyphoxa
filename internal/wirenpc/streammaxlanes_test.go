package wirenpc

import "testing"

// TestStreamMaxLanes pins the GLYPHOXA_STT_STREAM_MAX_LANES parsing (finding 7):
// absent/empty/invalid → the default 4; an explicit "0" is honoured as disabled (0
// lanes stream); a negative value is invalid → default.
func TestStreamMaxLanes(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  bool
		val  string
		want int
	}{
		{name: "absent", set: false, want: defaultStreamMaxLanes},
		{name: "empty", set: true, val: "", want: defaultStreamMaxLanes},
		{name: "invalid", set: true, val: "abc", want: defaultStreamMaxLanes},
		{name: "negative", set: true, val: "-2", want: defaultStreamMaxLanes},
		{name: "zero disables", set: true, val: "0", want: 0},
		{name: "explicit", set: true, val: "8", want: 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv("GLYPHOXA_STT_STREAM_MAX_LANES", tc.val)
			} else {
				t.Setenv("GLYPHOXA_STT_STREAM_MAX_LANES", "")
				// t.Setenv can't unset; the empty case already covers "unset" semantics.
			}
			if got := streamMaxLanes(); got != tc.want {
				t.Errorf("streamMaxLanes() = %d, want %d", got, tc.want)
			}
		})
	}
}
