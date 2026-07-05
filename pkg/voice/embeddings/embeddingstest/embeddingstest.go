// Package embeddingstest provides deterministic [embeddings.Provider] doubles
// for tests, so downstream chunk-writer, backfill-worker, and retrieval tests
// (ADR-0011) are reproducible without a live model.
//
// Two doubles, no cassette (ADR-0021's determinism goal, met by construction
// rather than by recording a network call):
//
//   - [Deterministic] hashes each text and expands it into a pseudo-random
//     unit vector. The same text yields the same [embeddings.Dim] vector on
//     every run and platform; different texts yield different vectors. It has
//     no semantic structure — it is for plumbing and reproducibility tests, not
//     for asserting that related texts are close (use a live Ollama for that).
//   - [Fixed] maps exact texts to hand-crafted vectors, so a test can lay out a
//     small semantic space by hand (e.g. two near-duplicate vectors and one
//     far) and assert retrieval ranking deterministically. An unmapped text is
//     a test-authoring error and returns a non-nil error.
package embeddingstest

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings"
)

// Deterministic is a stateless [embeddings.Provider] that maps each text to a
// stable pseudo-random unit vector. The zero value is ready to use.
type Deterministic struct{}

// Embed returns one [embeddings.Dim] vector per text, in input order. Each
// vector is a deterministic function of its text alone — identical across runs,
// platforms, and process restarts — so tests that record or compare vectors are
// reproducible. An empty batch returns (nil, nil).
func (Deterministic) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = vectorFor(t)
	}
	return out, nil
}

// vectorFor derives a stable unit vector from text: SHA-256 seeds a splitmix64
// stream that fills [embeddings.Dim] floats in [-1,1), then L2-normalizes so
// cosine similarity is a plain dot product. Hashing makes it independent of any
// float formatting, endianness at the API layer, or map-iteration order.
func vectorFor(text string) []float32 {
	sum := sha256.Sum256([]byte(text))
	state := binary.BigEndian.Uint64(sum[:8])

	v := make([]float32, embeddings.Dim)
	var norm float64
	for i := range v {
		u := splitmix64(&state)
		// Top 53 bits → float64 in [0,1), then scale to [-1,1).
		f := float64(u>>11)/(1<<53)*2 - 1
		v[i] = float32(f)
		// The explicit float64() conversion rounds the product to float64 before
		// the add, a Go-spec fusion barrier: without it a target (e.g. arm64) may
		// fuse mul+add into one FMA, giving low-bit-different norms per platform.
		// Downstream waves commit fixtures against these vectors, so cross-platform
		// bit-stability is load-bearing.
		norm += float64(f * f)
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		// Astronomically unlikely with splitmix64, but keep the contract total:
		// never emit a zero vector the ANN path can't normalize.
		v[0] = 1
		return v
	}
	inv := float32(1.0 / norm)
	for i := range v {
		v[i] *= inv
	}
	return v
}

// splitmix64 advances state and returns the next value of the SplitMix64
// generator — a fast, well-distributed PRNG whose only state is one uint64,
// making the derived vectors trivially reproducible.
func splitmix64(state *uint64) uint64 {
	*state += 0x9E3779B97F4A7C15
	z := *state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// Fixed is an [embeddings.Provider] backed by an exact text→vector map. It lets
// a test hand-place points in vector space (the map values need not be
// [embeddings.Dim] long — the test owns their shape). An unmapped text errors.
type Fixed map[string][]float32

// Embed returns the mapped vector for each text, in input order. A text absent
// from the map returns a non-nil error naming the missing text — an unmapped
// input is a test-authoring mistake, not a silent zero vector.
func (f Fixed) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v, ok := f[t]
		if !ok {
			return nil, fmt.Errorf("embeddingstest.Fixed: no vector mapped for %q", t)
		}
		// Copy so a consumer that mutates a returned vector can't corrupt the
		// shared fixture backing array (the map value).
		out[i] = append([]float32(nil), v...)
	}
	return out, nil
}
