// Package embeddings defines the v2 Embeddings provider surface — the
// "embeddings" Component per CONTEXT.md and ADR-0004.
//
// The Component turns Transcript chunk text into dense vectors for the async,
// eventually-consistent embedding pipeline (ADR-0011): the chunk writer inserts
// a row with embedding=NULL, a background worker calls [Provider.Embed] and
// UPDATEs the row, and NPC retrieval runs ANN similarity over the non-null
// vectors. The provider matrix is Ollama + OpenAI (ADR-0004); v1.0 ships the
// keyless local Ollama adapter as the default, with the OpenAI slot a future
// sibling behind this same interface.
//
// Determinism in tests follows ADR-0021's spirit without a cassette: the
// [github.com/MrWong99/Glyphoxa/pkg/voice/embeddings/embeddingstest] doubles
// yield stable vectors for the same input on every run and platform, so the
// downstream worker and retrieval tests are reproducible without a live model.
package embeddings

import "context"

// Dim is the embedding vector dimension every v1.0 Provider MUST return.
//
// Fixed at 768 because ADR-0011 pins the default model to Ollama
// `nomic-embed-text` and the storage schema to `vector(768)`; switching models
// or Matryoshka dimensions requires a schema+backfill migration, not a runtime
// change. Adapters guard against any other returned dimension so a mis-pulled
// model fails loudly instead of writing rows the ANN index cannot use.
const Dim = 768

// Provider embeds a batch of texts into dense vectors.
//
// The contract is total and order-preserving: Embed returns exactly len(texts)
// vectors in input order, each exactly [Dim] elements long, or a non-nil error.
// Implementations MUST error (never truncate or pad) when the underlying model
// returns any other dimension — a wrong dimension signals a mis-configured
// model, and silently reshaping it would corrupt the vector store.
type Provider interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}
