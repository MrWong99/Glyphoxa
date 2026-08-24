package storage

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

// encodeVectorReference is the original strings.Builder + FormatFloat renderer
// (#613). It is the byte-for-byte contract every already-stored pgvector value
// was written with, so it stays here as the oracle the fast renderer is held
// against: same bytes out, fewer allocations in.
func encodeVectorReference(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// encodeVectorSink keeps the renderer's result observable so the compiler
// cannot optimise the call away inside the allocation measurement.
var encodeVectorSink string

// TestEncodeVectorAllocationBudget holds the renderer to two allocations — the
// buffer and the final string conversion — regardless of vector length or
// content (#613). Every embedding write (SetChunkEmbedding, SetNodeEmbedding,
// the embedworker draining a whole backlog) pays this per row, so a per-element
// allocation is per-row GC pressure.
//
// The worst case is the one that matters: a buffer sized for typical elements
// regrows — a third allocation — on a vector where every element renders at the
// float32 maximum. Shortest-round-trip 'g' formatting tops out at 15 bytes
// ("-1.35849605e-05": sign, 9 significant digits, point, 4-byte exponent), 16
// with the separator, so the "typical" case alone would not catch an undersized
// cap.
func TestEncodeVectorAllocationBudget(t *testing.T) {
	const budget = 2

	typical := make([]float32, 768)
	for i := range typical {
		typical[i] = float32(math.Sin(float64(i) * 0.7300271))
	}

	// Every element renders at the 15-byte maximum; asserted below rather than
	// assumed, so a formatting change cannot quietly defang this case.
	widest := make([]float32, 768)
	for i := range widest {
		widest[i] = -1.35849605e-05
	}
	for i, f := range widest {
		if got := len(strconv.FormatFloat(float64(f), 'g', -1, 32)); got != 15 {
			t.Fatalf("widest[%d] renders in %d bytes, want the 15-byte float32 maximum", i, got)
		}
	}

	for _, tc := range []struct {
		name string
		vec  []float32
	}{
		{"typical embedding", typical},
		{"widest elements", widest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := testing.AllocsPerRun(50, func() {
				encodeVectorSink = encodeVector(tc.vec)
			})
			if got > budget {
				t.Errorf("encodeVector allocated %.0f times per call, want at most %d", got, budget)
			}
		})
	}
}

// TestEncodeVectorMatchesReference proves the renderer emits exactly the bytes
// the original implementation did, across the float32 shapes a real embedding
// (or a corrupt one) can carry: nothing, one element, signs, signed zero,
// denormals, extremes and the non-finite values. Byte equality is what keeps
// values written before and after #613 identical in the vector(768) columns.
func TestEncodeVectorMatchesReference(t *testing.T) {
	embedding := make([]float32, 768)
	for i := range embedding {
		embedding[i] = float32(math.Sin(float64(i) * 0.7300271))
	}
	cases := []struct {
		name string
		in   []float32
	}{
		{"nil", nil},
		{"empty", []float32{}},
		{"single", []float32{0.5}},
		{"negative", []float32{-0.25}},
		{"signed zero", []float32{0, float32(math.Copysign(0, -1))}},
		{"denormal", []float32{math.SmallestNonzeroFloat32, -math.SmallestNonzeroFloat32}},
		{"extremes", []float32{math.MaxFloat32, -math.MaxFloat32, 1e-40, 1e20}},
		{"non-finite", []float32{
			float32(math.NaN()),
			float32(math.Inf(1)),
			float32(math.Inf(-1)),
		}},
		{"full precision mantissas", []float32{0.1, 0.2, 0.3, 1.0 / 3.0}},
		{"768-dim embedding", embedding},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := encodeVectorReference(tc.in)
			if got := encodeVector(tc.in); got != want {
				t.Errorf("encodeVector = %q, want %q", got, want)
			}
		})
	}
}

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
