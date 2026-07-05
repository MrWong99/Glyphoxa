// Package embedworker is the async embedding backfill worker (#116, ADR-0011).
//
// The Transcript Chunk writer (#104) inserts rows with embedding NULL; this
// worker is the second half of that eventually-consistent pipeline. A background
// loop in the web/all process periodically claims the oldest NULL-embedding
// chunks, embeds their text through the configured [embeddings.Provider], and
// UPDATEs each row with its 768-dim vector plus the model stamp — draining the
// backlog toward zero and making the chunks returnable by the embedding-filtered
// retrieval query (embedding IS NOT NULL).
//
// Retry is implicit and stateless: a pass that hits an error stops early and
// re-claims the leftover work next pass. A failure BEFORE the writes (list, the
// provider call, a wrong vector count or dimension) writes nothing; a failure
// DURING the per-chunk writes keeps the rows already written — each embedding is
// an independent, valid row — and leaves the rest NULL. Either way the still-NULL
// chunks are re-claimed on the next pass. There is no backoff bookkeeping and no
// dead-letter state — a chunk that cannot be embedded now is simply retried
// later, forever, while the worker keeps running. The loop stops cleanly when its
// context is cancelled (process shutdown): in-flight provider calls carry that
// context, so they abort rather than pinning the shutdown.
package embedworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings/ollama"
)

// Store is the narrow persistence surface the worker needs. *storage.Store
// satisfies it; tests fake it.
type Store interface {
	// ListUnembeddedChunks returns up to limit chunks still awaiting an embedding,
	// oldest first — the worker's per-pass work queue.
	ListUnembeddedChunks(ctx context.Context, limit int) ([]storage.TranscriptChunk, error)
	// SetChunkEmbedding fills one chunk's vector and stamps the model.
	SetChunkEmbedding(ctx context.Context, id uuid.UUID, vec []float32, model string) error
	// CountUnembeddedChunks reports the remaining NULL-embedding backlog (gauge).
	CountUnembeddedChunks(ctx context.Context) (int, error)
}

// BacklogGauge receives the current NULL-embedding backlog after each pass that
// wrote at least one embedding (Set-from-COUNT, never Inc/Dec — ADR-0032).
// *observe.PrometheusRecorder satisfies it; a nil gauge disables the update.
type BacklogGauge interface {
	SetEmbeddingBacklog(n int)
}

// ProviderConfigStore is the single read [ResolveProvider] needs; *storage.Store
// satisfies it. Kept narrow so the resolution is unit-testable with a fake.
type ProviderConfigStore interface {
	GetEmbeddingsProviderConfig(ctx context.Context) (storage.ProviderConfig, error)
}

const (
	defaultInterval    = 10 * time.Second
	defaultBatchSize   = 16
	defaultCallTimeout = 60 * time.Second // Ollama's cold model load can take tens of seconds
)

// Config tunes the worker. Zero values fall back to the defaults above.
type Config struct {
	// Interval is the sleep between passes (and between an empty backlog and the
	// next look). Default 10s.
	Interval time.Duration
	// BatchSize is the max chunks claimed and embedded per pass. Default 16.
	BatchSize int
	// CallTimeout bounds one provider Embed call (derived from the run context).
	// Default 60s to survive an Ollama cold-start.
	CallTimeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.Interval <= 0 {
		c.Interval = defaultInterval
	}
	if c.BatchSize <= 0 {
		c.BatchSize = defaultBatchSize
	}
	if c.CallTimeout <= 0 {
		c.CallTimeout = defaultCallTimeout
	}
	return c
}

// Worker drains the embedding backlog. Construct with [New]; run with [Run].
type Worker struct {
	store    Store
	provider embeddings.Provider
	model    string
	gauge    BacklogGauge
	log      *slog.Logger
	cfg      Config
}

// New builds a Worker. model is the embedding model stamped onto each row (the
// provider must produce vectors for it); gauge may be nil to disable the backlog
// metric. Config zero values take the package defaults.
func New(store Store, provider embeddings.Provider, model string, gauge BacklogGauge, log *slog.Logger, cfg Config) *Worker {
	return &Worker{
		store:    store,
		provider: provider,
		model:    model,
		gauge:    gauge,
		log:      log,
		cfg:      cfg.withDefaults(),
	}
}

// Run drives the backfill loop until ctx is cancelled, then returns. Each
// iteration runs one pass and sleeps Interval; a cancel during either the pass
// (carried into the provider call) or the sleep unwinds promptly. Run blocks, so
// callers launch it as a goroutine.
func (w *Worker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		w.pass(ctx)

		timer := time.NewTimer(w.cfg.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// pass claims one batch, embeds it, and writes each vector. An error stops the
// pass early: a pre-write error (list, provider, wrong count/dimension) writes
// nothing, while a mid-batch write error keeps the rows already written and
// leaves the rest NULL. The still-NULL chunks are re-claimed next pass (the
// retry). The gauge is re-read only after the whole batch of writes succeeds; a
// short or aborted pass leaves it as the last pass set it.
func (w *Worker) pass(ctx context.Context) {
	chunks, err := w.store.ListUnembeddedChunks(ctx, w.cfg.BatchSize)
	if err != nil {
		w.log.Warn("embed backfill: list unembedded chunks failed; retrying next pass", "err", err)
		return
	}
	if len(chunks) == 0 {
		return // backlog drained; the loop sleeps
	}

	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Content
	}

	callCtx, cancel := context.WithTimeout(ctx, w.cfg.CallTimeout)
	defer cancel()
	vecs, err := w.provider.Embed(callCtx, texts)
	if err != nil {
		w.log.Warn("embed backfill: provider embed failed; chunks stay NULL, retrying next pass",
			"batch", len(chunks), "err", err)
		return
	}
	if len(vecs) != len(chunks) {
		w.log.Warn("embed backfill: provider returned wrong vector count; abandoning pass",
			"want", len(chunks), "got", len(vecs))
		return
	}
	// Validate every dimension BEFORE writing any row: a wrong dimension signals a
	// mis-configured model, so the whole batch is suspect — write none and retry
	// rather than corrupt the vector store with a partial, wrong-shape write.
	for i, v := range vecs {
		if len(v) != embeddings.Dim {
			w.log.Warn("embed backfill: provider returned a wrong-dimension vector; abandoning pass",
				"index", i, "want", embeddings.Dim, "got", len(v))
			return
		}
	}

	// Write each row; a failure stops the loop but keeps the rows already written
	// (each is an independent, committed embedding). This chunk and the untried
	// remainder stay NULL and are re-claimed next pass.
	for i, c := range chunks {
		if err := w.store.SetChunkEmbedding(ctx, c.ID, vecs[i], w.model); err != nil {
			w.log.Warn("embed backfill: set chunk embedding failed; this and later chunks retry next pass",
				"chunk_id", c.ID, "err", err)
			return
		}
	}

	n, err := w.store.CountUnembeddedChunks(ctx)
	if err != nil {
		w.log.Warn("embed backfill: recount backlog failed; gauge left stale", "err", err)
		return
	}
	if w.gauge != nil {
		w.gauge.SetEmbeddingBacklog(n)
	}
}

// ResolveProvider resolves the process's embeddings Provider and its model from
// the saved Provider Config (#116, reused by #122). No bound config falls back to
// the local Ollama default (ADR-0004/0011); a saved 'ollama' config honours its
// model (or the default when blank) and the GLYPHOXA_OLLAMA_URL endpoint
// override; any other provider is unsupported in v1.0 and returns an error (the
// caller logs it and skips the worker — a loud, non-fatal stall the gauge shows).
func ResolveProvider(ctx context.Context, store ProviderConfigStore) (embeddings.Provider, string, error) {
	cfg, err := store.GetEmbeddingsProviderConfig(ctx)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return newOllama(ollama.DefaultModel), ollama.DefaultModel, nil
	case err != nil:
		return nil, "", fmt.Errorf("embedworker: resolve embeddings provider: %w", err)
	}

	switch cfg.Provider {
	case "", ollama.ProviderID:
		model := cfg.Model
		if model == "" {
			model = ollama.DefaultModel
		}
		return newOllama(model), model, nil
	default:
		return nil, "", fmt.Errorf("embedworker: unsupported embeddings provider %q (v1.0 supports only %q)",
			cfg.Provider, ollama.ProviderID)
	}
}

// newOllama builds an Ollama embeddings client for model, pointed at the
// GLYPHOXA_OLLAMA_URL endpoint when set (else the loopback default).
func newOllama(model string) *ollama.Client {
	opts := []ollama.Option{ollama.WithModel(model)}
	if u := os.Getenv("GLYPHOXA_OLLAMA_URL"); u != "" {
		opts = append(opts, ollama.WithBaseURL(u))
	}
	return ollama.New(opts...)
}
