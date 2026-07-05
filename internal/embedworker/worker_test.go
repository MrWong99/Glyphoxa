package embedworker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings/embeddingstest"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings/ollama"
)

// discardLog is a logger that swallows output — most tests assert on the store
// and gauge, not on log lines.
func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeStore is an in-memory embed backlog: pending holds the still-NULL chunks
// (the ListUnembeddedChunks queue), embedded records what SetChunkEmbedding
// stored, and Count reports len(pending). The *Err fields inject failures.
type fakeStore struct {
	mu       sync.Mutex
	pending  []storage.TranscriptChunk
	embedded map[uuid.UUID]embedRecord
	listErr  error
	setErr   error
	countErr error
}

type embedRecord struct {
	vec   []float32
	model string
}

func (f *fakeStore) ListUnembeddedChunks(_ context.Context, limit int) ([]storage.TranscriptChunk, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	n := limit
	if n > len(f.pending) {
		n = len(f.pending)
	}
	out := make([]storage.TranscriptChunk, n)
	copy(out, f.pending[:n])
	return out, nil
}

func (f *fakeStore) SetChunkEmbedding(_ context.Context, id uuid.UUID, vec []float32, model string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	for i, c := range f.pending {
		if c.ID == id {
			f.pending = append(f.pending[:i], f.pending[i+1:]...)
			break
		}
	}
	if f.embedded == nil {
		f.embedded = map[uuid.UUID]embedRecord{}
	}
	f.embedded[id] = embedRecord{vec: append([]float32(nil), vec...), model: model}
	return nil
}

func (f *fakeStore) CountUnembeddedChunks(_ context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.countErr != nil {
		return 0, f.countErr
	}
	return len(f.pending), nil
}

func (f *fakeStore) embeddedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.embedded)
}

func (f *fakeStore) pendingCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pending)
}

// fakeGauge records every Set so a test can assert the backlog curve.
type fakeGauge struct {
	mu   sync.Mutex
	sets []int
}

func (g *fakeGauge) SetEmbeddingBacklog(n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sets = append(g.sets, n)
}

func (g *fakeGauge) snapshot() []int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]int(nil), g.sets...)
}

func chunk(content string) storage.TranscriptChunk {
	return storage.TranscriptChunk{ID: uuid.New(), CampaignID: uuid.New(), Content: content}
}

// Test 1: three NULL chunks are all embedded with 768-dim vectors and the model
// stamp in one pass.
func TestPassEmbedsWholeBatch(t *testing.T) {
	store := &fakeStore{pending: []storage.TranscriptChunk{
		chunk("alpha"), chunk("beta"), chunk("gamma"),
	}}
	w := New(store, embeddingstest.Deterministic{}, "nomic-embed-text", nil, discardLog(), Config{})

	w.pass(context.Background())

	if store.pendingCount() != 0 {
		t.Fatalf("pending after pass = %d, want 0 (all embedded)", store.pendingCount())
	}
	if store.embeddedCount() != 3 {
		t.Fatalf("embedded = %d, want 3", store.embeddedCount())
	}
	for id, rec := range store.embedded {
		if len(rec.vec) != embeddings.Dim {
			t.Errorf("chunk %s vector = %d dims, want %d", id, len(rec.vec), embeddings.Dim)
		}
		if rec.model != "nomic-embed-text" {
			t.Errorf("chunk %s model = %q, want nomic-embed-text", id, rec.model)
		}
	}
}

// flakyProvider errors on the first Embed call and delegates to inner after.
type flakyProvider struct {
	calls int
	inner embeddings.Provider
}

func (p *flakyProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	p.calls++
	if p.calls == 1 {
		return nil, errors.New("provider unavailable")
	}
	return p.inner.Embed(ctx, texts)
}

// Test 2: a provider error on pass 1 leaves every chunk NULL; pass 2 succeeds and
// embeds them. The worker itself never exits — pass returns and is re-driven.
func TestPassRetriesAfterProviderError(t *testing.T) {
	store := &fakeStore{pending: []storage.TranscriptChunk{chunk("alpha"), chunk("beta")}}
	prov := &flakyProvider{inner: embeddingstest.Deterministic{}}
	w := New(store, prov, "m", nil, discardLog(), Config{})

	w.pass(context.Background())
	if store.pendingCount() != 2 || store.embeddedCount() != 0 {
		t.Fatalf("after failing pass: pending=%d embedded=%d, want 2/0 (all stay NULL)",
			store.pendingCount(), store.embeddedCount())
	}

	w.pass(context.Background())
	if store.pendingCount() != 0 || store.embeddedCount() != 2 {
		t.Fatalf("after recovery pass: pending=%d embedded=%d, want 0/2",
			store.pendingCount(), store.embeddedCount())
	}
	if prov.calls != 2 {
		t.Fatalf("provider calls = %d, want 2 (failed then retried)", prov.calls)
	}
}

// Test 3: the gauge is Set from the true remaining count after each pass and the
// curve is monotone non-increasing, reaching zero when the backlog drains.
func TestGaugeDecreasesToZero(t *testing.T) {
	var pending []storage.TranscriptChunk
	for i := 0; i < 5; i++ {
		pending = append(pending, chunk("c"))
	}
	store := &fakeStore{pending: pending}
	gauge := &fakeGauge{}
	w := New(store, embeddingstest.Deterministic{}, "m", gauge, discardLog(), Config{BatchSize: 2})

	for store.pendingCount() > 0 {
		w.pass(context.Background())
	}

	sets := gauge.snapshot()
	if len(sets) == 0 {
		t.Fatal("gauge was never Set")
	}
	prev := 1 << 30
	for i, n := range sets {
		if n > prev {
			t.Errorf("gauge Set %d = %d rose above previous %d (must be monotone non-increasing)", i, n, prev)
		}
		prev = n
	}
	if sets[len(sets)-1] != 0 {
		t.Errorf("final gauge Set = %d, want 0 (backlog drained)", sets[len(sets)-1])
	}
}

// blockingProvider blocks inside Embed until the context is cancelled, so a test
// can cancel mid-call and assert the loop unwinds.
type blockingProvider struct {
	once    sync.Once
	entered chan struct{}
}

func (p *blockingProvider) Embed(ctx context.Context, _ []string) ([][]float32, error) {
	p.once.Do(func() { close(p.entered) })
	<-ctx.Done()
	return nil, ctx.Err()
}

// Test 4a: cancelling the context while the worker sleeps between passes returns
// Run promptly.
func TestRunReturnsOnCancelMidSleep(t *testing.T) {
	store := &fakeStore{} // empty backlog: pass does nothing, Run sleeps
	w := New(store, embeddingstest.Deterministic{}, "m", nil, discardLog(), Config{Interval: time.Hour})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	time.Sleep(20 * time.Millisecond) // let Run reach the inter-pass sleep
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return promptly after cancel during sleep")
	}
}

// Test 4b: cancelling the context while a provider call is in flight aborts the
// call (ctx is carried into Embed) and Run returns promptly.
func TestRunReturnsOnCancelMidCall(t *testing.T) {
	store := &fakeStore{pending: []storage.TranscriptChunk{chunk("alpha")}}
	prov := &blockingProvider{entered: make(chan struct{})}
	w := New(store, prov, "m", nil, discardLog(), Config{Interval: time.Hour})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()

	<-prov.entered // block until the worker is inside Embed
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return promptly after cancel during a provider call")
	}
	if store.embeddedCount() != 0 {
		t.Errorf("embedded = %d, want 0 (call aborted, nothing written)", store.embeddedCount())
	}
}

// Test 5: a wrong-dimension vector from the provider leaves every chunk NULL,
// logs, and the loop keeps running (a later pass can retry).
func TestPassRejectsWrongDimension(t *testing.T) {
	c := chunk("hello")
	store := &fakeStore{pending: []storage.TranscriptChunk{c}}
	prov := embeddingstest.Fixed{"hello": {1, 2, 3, 4}} // 4 dims, not 768
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	w := New(store, prov, "m", nil, log, Config{})

	w.pass(context.Background())

	if store.pendingCount() != 1 || store.embeddedCount() != 0 {
		t.Fatalf("pending=%d embedded=%d, want 1/0 (wrong-dim vector must not be written)",
			store.pendingCount(), store.embeddedCount())
	}
	if !strings.Contains(strings.ToLower(buf.String()), "dimension") {
		t.Errorf("expected a logged dimension warning, got: %q", buf.String())
	}

	// The loop keeps running: a second pass is a no-op, not a panic.
	w.pass(context.Background())
	if store.embeddedCount() != 0 {
		t.Errorf("embedded = %d after retry, want 0 (still wrong dimension)", store.embeddedCount())
	}
}

// fakeCfgStore is a ProviderConfigStore double for ResolveProvider.
type fakeCfgStore struct {
	cfg storage.ProviderConfig
	err error
}

func (f fakeCfgStore) GetEmbeddingsProviderConfig(context.Context) (storage.ProviderConfig, error) {
	return f.cfg, f.err
}

// Test 7: ResolveProvider defaults to Ollama+DefaultModel when no config is
// bound, honours a saved Ollama model, and rejects an unsupported provider.
func TestResolveProvider(t *testing.T) {
	ctx := context.Background()

	t.Run("not found defaults to ollama", func(t *testing.T) {
		prov, model, err := ResolveProvider(ctx, fakeCfgStore{err: storage.ErrNotFound})
		if err != nil {
			t.Fatalf("ResolveProvider: %v", err)
		}
		if model != ollama.DefaultModel {
			t.Errorf("model = %q, want %q (the ollama default)", model, ollama.DefaultModel)
		}
		if _, ok := prov.(*ollama.Client); !ok {
			t.Errorf("provider %T, want *ollama.Client", prov)
		}
	})

	t.Run("saved ollama model honoured", func(t *testing.T) {
		_, model, err := ResolveProvider(ctx, fakeCfgStore{cfg: storage.ProviderConfig{
			Provider: "ollama", Model: "mxbai-embed-large",
		}})
		if err != nil {
			t.Fatalf("ResolveProvider: %v", err)
		}
		if model != "mxbai-embed-large" {
			t.Errorf("model = %q, want mxbai-embed-large", model)
		}
	})

	t.Run("unsupported provider errors", func(t *testing.T) {
		_, _, err := ResolveProvider(ctx, fakeCfgStore{cfg: storage.ProviderConfig{Provider: "openai"}})
		if err == nil {
			t.Fatal("ResolveProvider(openai) = nil error, want an unsupported-provider error")
		}
	})
}
