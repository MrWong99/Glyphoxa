// No build tag, like vector_test.go next door: encodeVector is pure string
// rendering, so its benchmark runs in the default keyless gate alongside the
// package's other untagged unit tests. Internal package on purpose —
// encodeVector is unexported.

package storage

import (
	"math"
	"testing"
)

// BenchmarkEncodeVector768 renders one embedding the way every SetChunkEmbedding
// / KG-node embedding write does: 768 float32s (the vector(768) columns,
// ADR-0011) to pgvector's text form via per-element strconv.FormatFloat with
// shortest-round-trip formatting — the expensive mode. The embedworker drains
// whole backlogs through this, so ns/op and allocs/op here bound how fast the
// backlog can drain off-peak.
func BenchmarkEncodeVector768(b *testing.B) {
	vec := make([]float32, 768)
	for i := range vec {
		// Full-precision mantissas, like real embedding output — integer-ish
		// values would flatter FormatFloat.
		vec[i] = float32(math.Sin(float64(i) * 0.7300271))
	}
	b.ReportAllocs()
	for b.Loop() {
		encodeVector(vec)
	}
}
