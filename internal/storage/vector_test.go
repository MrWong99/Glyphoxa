package storage

import "testing"

// TestEncodeVector locks pgvector's text input format: a bracketed,
// comma-separated list with shortest round-trippable float32 decimals and no
// spaces. This is the exact string handed to a server-side ::vector cast, so its
// shape is load-bearing (a malformed literal is a runtime write failure, not a
// compile error). The integration test proves the same string round-trips
// through a real ::vector column.
func TestEncodeVector(t *testing.T) {
	cases := []struct {
		name string
		in   []float32
		want string
	}{
		{"empty", []float32{}, "[]"},
		{"single", []float32{0.5}, "[0.5]"},
		{"signs and zero", []float32{0.5, -0.25, 0}, "[0.5,-0.25,0]"},
		{"needs precision", []float32{0.1, 0.2, 0.3}, "[0.1,0.2,0.3]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := encodeVector(tc.in); got != tc.want {
				t.Errorf("encodeVector(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
