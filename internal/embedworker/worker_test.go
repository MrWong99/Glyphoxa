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
// stored, and Count reports len(pending). listErr/countErr fail those reads;
// setFailID fails SetChunkEmbedding for exactly one chunk id, so a test can land
// a write error mid-batch (rows before it commit, rows from it on stay NULL).
type fakeStore struct {
	mu        sync.Mutex
	pending   []storage.TranscriptChunk
	embedded  map[uuid.UUID]embedRecord
	listErr   error
	setFailID uuid.UUID
	countErr  error

	// Node backlog (#300): mirrors the chunk fields so a test can drive the node
	// phase independently.
	pendingNodes  []storage.KGNode
	embeddedNodes map[uuid.UUID]embedRecord
	nodeListErr   error
	nodeSetFailID uuid.UUID
	nodeCountErr  error
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
	if f.setFailID != uuid.Nil && id == f.setFailID {
		return errors.New("set chunk embedding: injected write failure")
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

func (f *fakeStore) ListUnembeddedNodes(_ context.Context, limit int) ([]storage.KGNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nodeListErr != nil {
		return nil, f.nodeListErr
	}
	n := limit
	if n > len(f.pendingNodes) {
		n = len(f.pendingNodes)
	}
	out := make([]storage.KGNode, n)
	copy(out, f.pendingNodes[:n])
	return out, nil
}

func (f *fakeStore) SetNodeEmbedding(_ context.Context, id uuid.UUID, vec []float32, model string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nodeSetFailID != uuid.Nil && id == f.nodeSetFailID {
		return errors.New("set node embedding: injected write failure")
	}
	for i, n := range f.pendingNodes {
		if n.ID == id {
			f.pendingNodes = append(f.pendingNodes[:i], f.pendingNodes[i+1:]...)
			break
		}
	}
	if f.embeddedNodes == nil {
		f.embeddedNodes = map[uuid.UUID]embedRecord{}
	}
	f.embeddedNodes[id] = embedRecord{vec: append([]float32(nil), vec...), model: model}
	return nil
}

func (f *fakeStore) CountUnembeddedNodes(_ context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nodeCountErr != nil {
		return 0, f.nodeCountErr
	}
	return len(f.pendingNodes), nil
}

func (f *fakeStore) embeddedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.embedded)
}

func (f *fakeStore) embeddedNodeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.embeddedNodes)
}

func (f *fakeStore) pendingNodeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pendingNodes)
}

func (f *fakeStore) nodeEmbedRecord(id uuid.UUID) (embedRecord, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.embeddedNodes[id]
	return r, ok
}

func (f *fakeStore) pendingCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pending)
}

func (f *fakeStore) isEmbedded(id uuid.UUID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.embedded[id]
	return ok
}

// fakeGauge records every Set so a test can assert the backlog curve.
type fakeGauge struct {
	mu       sync.Mutex
	sets     []int
	nodeSets []int
}

func (g *fakeGauge) SetEmbeddingBacklog(n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sets = append(g.sets, n)
}

func (g *fakeGauge) SetKGEmbeddingBacklog(n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nodeSets = append(g.nodeSets, n)
}

func (g *fakeGauge) nodeSnapshot() []int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]int(nil), g.nodeSets...)
}

func (g *fakeGauge) snapshot() []int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]int(nil), g.sets...)
}

func chunk(content string) storage.TranscriptChunk {
	return storage.TranscriptChunk{ID: uuid.New(), CampaignID: uuid.New(), Content: content}
}

func node(name, body string) storage.KGNode {
	return storage.KGNode{ID: uuid.New(), CampaignID: uuid.New(), Name: name, Body: body}
}

// TestPassEmbedsNodeBacklog: the node phase mirrors the chunk phase — it drains
// the Node backlog, embeds name+body (trimmed, blank-line joined), stamps the
// model, and Sets the KG backlog gauge to the true remaining count.
func TestPassEmbedsNodeBacklog(t *testing.T) {
	n0, n1 := node("Bart", "An innkeeper."), node("Neverwinter", "")
	store := &fakeStore{pendingNodes: []storage.KGNode{n0, n1}}
	gauge := &fakeGauge{}
	w := New(store, embeddingstest.Deterministic{}, "nomic-embed-text", gauge, discardLog(), Config{})

	w.pass(context.Background())

	if store.pendingNodeCount() != 0 || store.embeddedNodeCount() != 2 {
		t.Fatalf("pendingNodes=%d embeddedNodes=%d, want 0/2", store.pendingNodeCount(), store.embeddedNodeCount())
	}
	rec, ok := store.nodeEmbedRecord(n0.ID)
	if !ok {
		t.Fatal("n0 not embedded")
	}
	if len(rec.vec) != embeddings.Dim {
		t.Errorf("node vector = %d dims, want %d", len(rec.vec), embeddings.Dim)
	}
	if rec.model != "nomic-embed-text" {
		t.Errorf("node model = %q, want nomic-embed-text", rec.model)
	}
	// A body-less node still embeds (name alone), never a panic or skip.
	if _, ok := store.nodeEmbedRecord(n1.ID); !ok {
		t.Error("body-less node n1 not embedded")
	}
	sets := gauge.nodeSnapshot()
	if len(sets) == 0 || sets[len(sets)-1] != 0 {
		t.Errorf("KG backlog gauge = %v, want final 0", sets)
	}
}

// TestPassNodePhaseIndependentOfChunkError: a failing chunk phase must not starve
// the node phase — the two backlogs drain independently.
func TestPassNodePhaseIndependentOfChunkError(t *testing.T) {
	store := &fakeStore{
		pending:      []storage.TranscriptChunk{chunk("c")},
		listErr:      errors.New("chunk db down"),
		pendingNodes: []storage.KGNode{node("N", "b")},
	}
	w := New(store, embeddingstest.Deterministic{}, "m", nil, discardLog(), Config{})

	w.pass(context.Background())

	if store.embeddedCount() != 0 {
		t.Errorf("chunks embedded = %d, want 0 (chunk phase failed)", store.embeddedCount())
	}
	if store.embeddedNodeCount() != 1 {
		t.Errorf("nodes embedded = %d, want 1 (node phase must still run)", store.embeddedNodeCount())
	}
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

// Test: a List read error abandons the pass without embedding anything or
// touching the gauge, and the worker keeps running — a later pass recovers.
func TestPassAbandonedOnListError(t *testing.T) {
	store := &fakeStore{
		pending: []storage.TranscriptChunk{chunk("alpha")},
		listErr: errors.New("db unavailable"),
	}
	gauge := &fakeGauge{}
	w := New(store, embeddingstest.Deterministic{}, "m", gauge, discardLog(), Config{})

	w.pass(context.Background())
	if store.embeddedCount() != 0 {
		t.Errorf("embedded = %d, want 0 (list failed, nothing embedded)", store.embeddedCount())
	}
	if got := gauge.snapshot(); len(got) != 0 {
		t.Errorf("gauge Set %d times, want 0 (no successful pass)", len(got))
	}

	store.mu.Lock()
	store.listErr = nil
	store.mu.Unlock()
	w.pass(context.Background())
	if store.embeddedCount() != 1 {
		t.Errorf("embedded = %d after recovery pass, want 1 (worker kept running)", store.embeddedCount())
	}
}

// Test: a write error mid-batch commits the rows written before it (each
// embedding is an independent, valid row) and leaves the failing chunk plus the
// untried remainder NULL; a later pass drains them (the retry).
func TestPassKeepsWrittenRowsOnMidBatchSetError(t *testing.T) {
	c0, c1, c2 := chunk("alpha"), chunk("beta"), chunk("gamma")
	store := &fakeStore{
		pending:   []storage.TranscriptChunk{c0, c1, c2},
		setFailID: c1.ID, // the second write fails
	}
	w := New(store, embeddingstest.Deterministic{}, "m", nil, discardLog(), Config{})

	w.pass(context.Background())
	if !store.isEmbedded(c0.ID) {
		t.Error("c0 not embedded; the pre-error write must commit")
	}
	if store.isEmbedded(c1.ID) || store.isEmbedded(c2.ID) {
		t.Error("c1/c2 embedded; the failing chunk and the untried remainder must stay NULL")
	}
	if store.pendingCount() != 2 {
		t.Fatalf("pending = %d, want 2 (c1 failed + c2 untried)", store.pendingCount())
	}

	// Clear the injected failure: the next pass drains the leftover backlog.
	store.mu.Lock()
	store.setFailID = uuid.Nil
	store.mu.Unlock()
	w.pass(context.Background())
	if store.pendingCount() != 0 || store.embeddedCount() != 3 {
		t.Fatalf("after retry pass: pending=%d embedded=%d, want 0/3", store.pendingCount(), store.embeddedCount())
	}
}

// Test: a recount error after the writes leaves the gauge unset (stale, not
// zeroed) while the embeddings still commit, and the worker keeps running.
func TestPassLeavesGaugeStaleOnCountError(t *testing.T) {
	store := &fakeStore{
		pending:  []storage.TranscriptChunk{chunk("alpha"), chunk("beta")},
		countErr: errors.New("count failed"),
	}
	gauge := &fakeGauge{}
	w := New(store, embeddingstest.Deterministic{}, "m", gauge, discardLog(), Config{})

	w.pass(context.Background())
	if store.embeddedCount() != 2 {
		t.Fatalf("embedded = %d, want 2 (writes precede the recount)", store.embeddedCount())
	}
	if got := gauge.snapshot(); len(got) != 0 {
		t.Errorf("gauge Set %d times, want 0 (recount failed → gauge stays stale)", len(got))
	}

	// Worker keeps running: with the backlog drained, the next pass is a no-op.
	store.mu.Lock()
	store.countErr = nil
	store.mu.Unlock()
	w.pass(context.Background())
	if store.embeddedCount() != 2 {
		t.Errorf("embedded = %d, want 2 (no work left)", store.embeddedCount())
	}
}

// shortProvider returns one fewer vector than requested — a broken provider that
// violates the order-preserving, total contract.
type shortProvider struct{}

func (shortProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	return embeddingstest.Deterministic{}.Embed(ctx, texts[:len(texts)-1])
}

// Test: a vector-count mismatch (len(vecs) != len(chunks)) abandons the pass
// without writing any row — a provider that can't return one vector per input is
// mis-behaving, and a positional write would embed rows with the wrong vector.
func TestPassRejectsVectorCountMismatch(t *testing.T) {
	store := &fakeStore{pending: []storage.TranscriptChunk{chunk("alpha"), chunk("beta")}}
	w := New(store, shortProvider{}, "m", nil, discardLog(), Config{})

	w.pass(context.Background())
	if store.embeddedCount() != 0 || store.pendingCount() != 2 {
		t.Fatalf("embedded=%d pending=%d, want 0/2 (count mismatch abandons the pass)",
			store.embeddedCount(), store.pendingCount())
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
