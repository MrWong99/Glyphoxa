package recall

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// --- fakes ---

type fakeEmbedder struct {
	mu    sync.Mutex
	calls int
	vec   []float32
	block bool  // block until ctx is done, then return ctx.Err()
	err   error // forced error
}

func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = f.vec
	}
	return out, nil
}

func (f *fakeEmbedder) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeRetriever struct {
	mu           sync.Mutex
	byAgentCalls int
	byCampCalls  int
	agentChunks  []storage.ChunkMatch
	campChunks   []storage.ChunkMatch
	agentErr     error
	campErr      error
}

func (f *fakeRetriever) SearchChunksByAgent(_ context.Context, _, _ uuid.UUID, _ []float32, _ int) ([]storage.ChunkMatch, error) {
	f.mu.Lock()
	f.byAgentCalls++
	f.mu.Unlock()
	if f.agentErr != nil {
		return nil, f.agentErr
	}
	return f.agentChunks, nil
}

func (f *fakeRetriever) SearchChunksByCampaign(_ context.Context, _ uuid.UUID, _ []float32, _ int) ([]storage.ChunkMatch, error) {
	f.mu.Lock()
	f.byCampCalls++
	f.mu.Unlock()
	if f.campErr != nil {
		return nil, f.campErr
	}
	return f.campChunks, nil
}

func (f *fakeRetriever) agentN() int { f.mu.Lock(); defer f.mu.Unlock(); return f.byAgentCalls }
func (f *fakeRetriever) campN() int  { f.mu.Lock(); defer f.mu.Unlock(); return f.byCampCalls }

type fakeSessions struct {
	campaignID uuid.UUID
	active     bool
}

func (f fakeSessions) Snapshot() (storage.VoiceSession, bool) {
	if !f.active {
		return storage.VoiceSession{}, false
	}
	return storage.VoiceSession{ID: uuid.New(), CampaignID: f.campaignID}, true
}

type fakeMetrics struct {
	mu     sync.Mutex
	counts map[observe.RecallOutcome]int
}

func newFakeMetrics() *fakeMetrics { return &fakeMetrics{counts: map[observe.RecallOutcome]int{}} }

func (f *fakeMetrics) MemoryRecall(o observe.RecallOutcome) {
	f.mu.Lock()
	f.counts[o]++
	f.mu.Unlock()
}

func (f *fakeMetrics) count(o observe.RecallOutcome) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[o]
}

func (f *fakeMetrics) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, v := range f.counts {
		n += v
	}
	return n
}

// --- helpers ---

func fixedVec() []float32 { return []float32{0.1, 0.2, 0.3} }

func chunkMatch(content string) storage.ChunkMatch {
	return storage.ChunkMatch{Chunk: storage.TranscriptChunk{Content: content}}
}

func newTestRecaller(t *testing.T, emb embeddings.Provider, ret Retriever, sess Sessions, m Metrics, bus *voiceevent.Bus, cfg Config) *Recaller {
	t.Helper()
	r := New(emb, ret, sess, bus, m, slog.New(slog.DiscardHandler), cfg)
	t.Cleanup(r.Close)
	return r
}

func waitSpeculated(t *testing.T, r *Recaller) {
	t.Helper()
	select {
	case <-r.speculated:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a speculation pass")
	}
}

// --- tests ---

// TestRecall_NoPartials_InlineQueriesBothModes_CountsMiss pins the inline
// bounded-sync path (ADR-0042): with no speculation, Recall embeds the utterance
// and runs BOTH ANN modes, returns the chunks split by mode, and counts a miss.
func TestRecall_NoPartials_InlineQueriesBothModes_CountsMiss(t *testing.T) {
	emb := &fakeEmbedder{vec: fixedVec()}
	ret := &fakeRetriever{
		agentChunks: []storage.ChunkMatch{chunkMatch("I poured his ale.")},
		campChunks:  []storage.ChunkMatch{chunkMatch("A dragon flew over the pass.")},
	}
	m := newFakeMetrics()
	r := newTestRecaller(t, emb, ret, fakeSessions{campaignID: uuid.New(), active: true}, m, voiceevent.NewBus(), Config{})

	mem := r.Recall(context.Background(), uuid.NewString(), "do you remember the ale")

	if emb.callCount() != 1 {
		t.Errorf("embed calls = %d, want 1 (inline embed)", emb.callCount())
	}
	if ret.campN() != 1 || ret.agentN() != 1 {
		t.Errorf("want both ANN modes queried once: camp=%d agent=%d", ret.campN(), ret.agentN())
	}
	if len(mem.Personal) != 1 || mem.Personal[0] != "I poured his ale." {
		t.Errorf("personal = %v, want the NPC-knowledge chunk", mem.Personal)
	}
	if len(mem.World) != 1 || mem.World[0] != "A dragon flew over the pass." {
		t.Errorf("world = %v, want the world-context chunk", mem.World)
	}
	if m.count(observe.RecallMiss) != 1 || m.count(observe.RecallHit) != 0 {
		t.Errorf("outcomes: miss=%d hit=%d, want miss=1 hit=0", m.count(observe.RecallMiss), m.count(observe.RecallHit))
	}
}

// TestRecall_SpeculationHit_ReusesPrefetch_CountsHit pins the speculative path:
// a partial is embedded + world-prefetched off the turn; a matching final reuses
// the vector and prefetched world chunks, runs ONLY the deferred NPC-knowledge
// query, and counts a hit — no second embed.
func TestRecall_SpeculationHit_ReusesPrefetch_CountsHit(t *testing.T) {
	emb := &fakeEmbedder{vec: fixedVec()}
	ret := &fakeRetriever{
		agentChunks: []storage.ChunkMatch{chunkMatch("I served the knight last night.")},
		campChunks:  []storage.ChunkMatch{chunkMatch("Bandits on the north road.")},
	}
	m := newFakeMetrics()
	bus := voiceevent.NewBus()
	r := newTestRecaller(t, emb, ret, fakeSessions{campaignID: uuid.New(), active: true}, m, bus, Config{})

	bus.Publish(voiceevent.STTPartial{Text: "Do you remember the knight?", UtteranceID: "u1"})
	waitSpeculated(t, r)

	if emb.callCount() != 1 {
		t.Fatalf("speculator embed calls = %d, want 1", emb.callCount())
	}
	if ret.campN() != 1 {
		t.Fatalf("world prefetch calls = %d, want 1", ret.campN())
	}
	if ret.agentN() != 0 {
		t.Fatalf("NPC-knowledge must be deferred during speech; byAgent = %d, want 0", ret.agentN())
	}

	mem := r.Recall(context.Background(), uuid.NewString(), "do you remember the knight")

	if emb.callCount() != 1 {
		t.Errorf("embed called again on a hit: calls = %d, want 1", emb.callCount())
	}
	if ret.agentN() != 1 {
		t.Errorf("byAgent = %d, want exactly 1 (deferred NPC-knowledge at recall)", ret.agentN())
	}
	if ret.campN() != 1 {
		t.Errorf("world re-queried on a hit: byCamp = %d, want 1 (prefetch only)", ret.campN())
	}
	if len(mem.World) != 1 || mem.World[0] != "Bandits on the north road." {
		t.Errorf("world not reused from prefetch: %v", mem.World)
	}
	if len(mem.Personal) != 1 || mem.Personal[0] != "I served the knight last night." {
		t.Errorf("personal = %v", mem.Personal)
	}
	if m.count(observe.RecallHit) != 1 || m.count(observe.RecallMiss) != 0 {
		t.Errorf("outcomes: hit=%d miss=%d, want hit=1 miss=0", m.count(observe.RecallHit), m.count(observe.RecallMiss))
	}
}

// TestRecall_SpeculationMiss_FallsBackInline_CountsMiss pins that a final NOT
// matching the speculated partial re-embeds inline and counts a miss.
func TestRecall_SpeculationMiss_FallsBackInline_CountsMiss(t *testing.T) {
	emb := &fakeEmbedder{vec: fixedVec()}
	ret := &fakeRetriever{
		agentChunks: []storage.ChunkMatch{chunkMatch("personal")},
		campChunks:  []storage.ChunkMatch{chunkMatch("world")},
	}
	m := newFakeMetrics()
	bus := voiceevent.NewBus()
	r := newTestRecaller(t, emb, ret, fakeSessions{campaignID: uuid.New(), active: true}, m, bus, Config{})

	bus.Publish(voiceevent.STTPartial{Text: "Do you remember the knight?", UtteranceID: "u1"})
	waitSpeculated(t, r)
	if emb.callCount() != 1 {
		t.Fatalf("speculator embed = %d, want 1", emb.callCount())
	}

	mem := r.Recall(context.Background(), uuid.NewString(), "what about the golden crown")

	if emb.callCount() != 2 {
		t.Errorf("embed calls = %d, want 2 (speculation + inline miss)", emb.callCount())
	}
	if ret.agentN() != 1 {
		t.Errorf("byAgent = %d, want 1", ret.agentN())
	}
	if ret.campN() != 2 {
		t.Errorf("byCamp = %d, want 2 (prefetch + inline)", ret.campN())
	}
	if m.count(observe.RecallMiss) != 1 || m.count(observe.RecallHit) != 0 {
		t.Errorf("outcomes: miss=%d hit=%d, want miss=1 hit=0", m.count(observe.RecallMiss), m.count(observe.RecallHit))
	}
	_ = mem
}

// TestRecall_BudgetExceeded_DegradesToSkip pins the hard budget (ADR-0042): an
// embed that outlasts the budget yields zero Memory within ~budget and counts a
// skip.
func TestRecall_BudgetExceeded_DegradesToSkip(t *testing.T) {
	emb := &fakeEmbedder{block: true}
	m := newFakeMetrics()
	r := newTestRecaller(t, emb, &fakeRetriever{}, fakeSessions{campaignID: uuid.New(), active: true}, m,
		voiceevent.NewBus(), Config{Budget: 50 * time.Millisecond})

	start := time.Now()
	mem := r.Recall(context.Background(), uuid.NewString(), "do you remember the ale")
	elapsed := time.Since(start)

	if !mem.IsZero() {
		t.Errorf("want zero Memory on budget exceed, got %+v", mem)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("recall took %v, want ~budget (50ms)", elapsed)
	}
	if m.count(observe.RecallSkip) != 1 {
		t.Errorf("skip = %d, want 1", m.count(observe.RecallSkip))
	}
}

// TestRecall_RetrieverError_DegradesToSkip pins the DB-down degradation: a
// retrieval error yields zero Memory and counts a skip.
func TestRecall_RetrieverError_DegradesToSkip(t *testing.T) {
	emb := &fakeEmbedder{vec: fixedVec()}
	ret := &fakeRetriever{campErr: errors.New("db down")}
	m := newFakeMetrics()
	r := newTestRecaller(t, emb, ret, fakeSessions{campaignID: uuid.New(), active: true}, m, voiceevent.NewBus(), Config{})

	mem := r.Recall(context.Background(), uuid.NewString(), "do you remember the ale")
	if !mem.IsZero() {
		t.Errorf("want zero Memory on retriever error, got %+v", mem)
	}
	if m.count(observe.RecallSkip) != 1 {
		t.Errorf("skip = %d, want 1", m.count(observe.RecallSkip))
	}
}

// TestRecall_BargeCancel_ZeroMemoryNoCounter pins that a barge (a cancelled turn
// ctx) yields zero Memory and counts NOTHING — not even a skip (ADR-0042): the
// turn is gone, so the recall is not a degradation to record.
func TestRecall_BargeCancel_ZeroMemoryNoCounter(t *testing.T) {
	emb := &fakeEmbedder{vec: fixedVec()}
	ret := &fakeRetriever{agentChunks: []storage.ChunkMatch{chunkMatch("x")}}
	m := newFakeMetrics()
	r := newTestRecaller(t, emb, ret, fakeSessions{campaignID: uuid.New(), active: true}, m, voiceevent.NewBus(), Config{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // barge cancelled the turn before recall started

	mem := r.Recall(ctx, uuid.NewString(), "do you remember the ale")
	if !mem.IsZero() {
		t.Errorf("want zero Memory on barge, got %+v", mem)
	}
	if m.total() != 0 {
		t.Errorf("a barge must count nothing; total outcomes = %d", m.total())
	}
	if emb.callCount() != 0 {
		t.Errorf("a barge must not embed; calls = %d", emb.callCount())
	}
}

// TestRecall_UnparseableAgentID_Skips pins the defensive guard: a non-uuid agent
// id yields zero Memory and counts a skip, never a panic.
func TestRecall_UnparseableAgentID_Skips(t *testing.T) {
	emb := &fakeEmbedder{vec: fixedVec()}
	m := newFakeMetrics()
	r := newTestRecaller(t, emb, &fakeRetriever{}, fakeSessions{campaignID: uuid.New(), active: true}, m, voiceevent.NewBus(), Config{})

	mem := r.Recall(context.Background(), "not-a-uuid", "do you remember the ale")
	if !mem.IsZero() {
		t.Errorf("want zero Memory for a bad agent id, got %+v", mem)
	}
	if m.count(observe.RecallSkip) != 1 {
		t.Errorf("skip = %d, want 1", m.count(observe.RecallSkip))
	}
	if emb.callCount() != 0 {
		t.Errorf("must not embed with a bad agent id; calls = %d", emb.callCount())
	}
}
