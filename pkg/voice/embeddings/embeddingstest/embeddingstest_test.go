package embeddingstest_test

import (
	"context"
	"math"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings/embeddingstest"
)

// Compile-time assertions: both doubles satisfy [embeddings.Provider], so they
// are drop-in substitutes for the Ollama adapter in worker and retrieval tests.
var (
	_ embeddings.Provider = embeddingstest.Deterministic{}
	_ embeddings.Provider = embeddingstest.Fixed{}
)

func l2Norm(v []float32) float64 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return math.Sqrt(sum)
}

// TestDeterministic_SameInput_IdenticalVector is the reproducibility guarantee
// AC4 rests on: the same text yields byte-identical vectors every call, so
// downstream worker and retrieval tests are stable without a live model.
func TestDeterministic_SameInput_IdenticalVector(t *testing.T) {
	var d embeddingstest.Deterministic
	a, err := d.Embed(context.Background(), []string{"the dragon guards the northern pass"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	b, err := d.Embed(context.Background(), []string{"the dragon guards the northern pass"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("len(a)=%d len(b)=%d, want 1 each", len(a), len(b))
	}
	if len(a[0]) != embeddings.Dim {
		t.Fatalf("vector len = %d, want %d", len(a[0]), embeddings.Dim)
	}
	for i := range a[0] {
		if a[0][i] != b[0][i] {
			t.Fatalf("vectors differ at index %d: %v vs %v", i, a[0][i], b[0][i])
		}
	}
}

// TestDeterministic_DifferentInput_DifferentVector guards against a degenerate
// double that returns a constant vector — different texts must map to different
// vectors or a retrieval test could never distinguish them.
func TestDeterministic_DifferentInput_DifferentVector(t *testing.T) {
	var d embeddingstest.Deterministic
	out, err := d.Embed(context.Background(), []string{"alpha", "beta"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	same := true
	for i := range out[0] {
		if out[0][i] != out[1][i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("distinct inputs produced identical vectors")
	}
}

// TestDeterministic_L2NormApproxOne pins the L2-normalization: unit-length
// vectors make cosine similarity a plain dot product, matching how the ANN
// retrieval path (ADR-0011) compares them.
func TestDeterministic_L2NormApproxOne(t *testing.T) {
	var d embeddingstest.Deterministic
	out, err := d.Embed(context.Background(), []string{"tavern", "quest", "", "a much longer sentence here"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	for i, v := range out {
		if len(v) != embeddings.Dim {
			t.Fatalf("vector %d len = %d, want %d", i, len(v), embeddings.Dim)
		}
		if n := l2Norm(v); math.Abs(n-1.0) > 1e-5 {
			t.Errorf("vector %d L2 norm = %v, want ~1", i, n)
		}
	}
}

// TestDeterministic_BatchOrderAndCount mirrors the real provider's totality:
// N texts yield N vectors in input order.
func TestDeterministic_BatchOrderAndCount(t *testing.T) {
	var d embeddingstest.Deterministic
	texts := []string{"one", "two", "three"}
	out, err := d.Embed(context.Background(), texts)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(out) != len(texts) {
		t.Fatalf("len(out) = %d, want %d", len(out), len(texts))
	}
	// The i-th output must equal a standalone embedding of texts[i] (order).
	for i, txt := range texts {
		single, err := d.Embed(context.Background(), []string{txt})
		if err != nil {
			t.Fatalf("Embed single: %v", err)
		}
		for j := range out[i] {
			if out[i][j] != single[0][j] {
				t.Fatalf("out[%d] != standalone embedding of %q at index %d", i, txt, j)
			}
		}
	}
}

// TestDeterministic_Golden locks the exact algorithm output for one input, so a
// future refactor of the hash/PRNG/normalization silently changing the vector
// space (which would invalidate any committed fixtures downstream) fails loudly.
// The values were captured from the reference implementation; they encode the
// cross-run/platform stability AC4 requires, not any semantic meaning.
func TestDeterministic_Golden(t *testing.T) {
	var d embeddingstest.Deterministic
	out, err := d.Embed(context.Background(), []string{"glyphoxa"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	want := []float32{-0.0302517, -0.019189103, 0.058886487, 0.0416833, -0.05829539}
	for i, w := range want {
		if out[0][i] != w {
			t.Errorf("out[0][%d] = %v, want %v (algorithm changed?)", i, out[0][i], w)
		}
	}
}

// TestFixed_MappedText_ReturnsVector: a text present in the map returns exactly
// its hand-crafted vector, in input order.
func TestFixed_MappedText_ReturnsVector(t *testing.T) {
	f := embeddingstest.Fixed{
		"hello": {1, 0, 0},
		"world": {0, 1, 0},
	}
	out, err := f.Embed(context.Background(), []string{"world", "hello"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	if out[0][1] != 1 || out[1][0] != 1 {
		t.Errorf("Fixed returned wrong/ordered vectors: %v", out)
	}
}

// TestFixed_UnknownText_Errors: an unmapped text is a test-authoring mistake and
// must error loudly rather than silently returning a zero vector.
func TestFixed_UnknownText_Errors(t *testing.T) {
	f := embeddingstest.Fixed{"known": {1}}
	_, err := f.Embed(context.Background(), []string{"known", "surprise"})
	if err == nil {
		t.Fatal("Fixed.Embed with unmapped text returned nil error")
	}
}
