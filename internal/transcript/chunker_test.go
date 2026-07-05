package transcript

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// fakeChunkStore is an in-memory ChunkStore: it records the chunks it was asked
// to insert and models the NULL-embedding backlog as "one more per insert" — the
// real CountUnembeddedChunks over rows that all land embedding=NULL (#104).
type fakeChunkStore struct {
	mu         sync.Mutex
	chunks     []storage.TranscriptChunk
	countCalls int
}

func (f *fakeChunkStore) InsertTranscriptChunk(_ context.Context, c storage.TranscriptChunk) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chunks = append(f.chunks, c)
	return uuid.New(), nil
}

func (f *fakeChunkStore) CountUnembeddedChunks(_ context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.countCalls++
	return len(f.chunks), nil // every insert is a NULL-embedding row
}

func (f *fakeChunkStore) all() []storage.TranscriptChunk {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]storage.TranscriptChunk(nil), f.chunks...)
}

func (f *fakeChunkStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.chunks)
}

// fakeGauge records every SetEmbeddingBacklog call so the "Set-from-COUNT" path
// is observable.
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

// capHandler records log messages so a drop-on-overflow is assertable.
type capHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (c *capHandler) Enabled(context.Context, slog.Level) bool { return true }
func (c *capHandler) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, r.Message)
	return nil
}
func (c *capHandler) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *capHandler) WithGroup(string) slog.Handler      { return c }
func (c *capHandler) has(sub string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range c.msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

func newChunker(t *testing.T, store ChunkStore, gauge BacklogGauge, cfg ChunkerConfig) (*voiceevent.Bus, *fakeSessions, *Chunker) {
	t.Helper()
	bus := voiceevent.NewBus()
	fs := &fakeSessions{id: uuid.New(), active: true}
	c := NewChunker(bus, fs, store, gauge, slog.New(slog.DiscardHandler), cfg)
	return bus, fs, c
}

// eventually polls fn until it returns true or the deadline passes.
func eventually(t *testing.T, within time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return fn()
}

// TestChunker_FiveUtterancesOneInsert is #104 rule CLOSE: a chunk closes at the
// fifth utterance into ONE insert whose content is the five lines in order — not
// one insert per utterance — and the embedding is never set on the write path.
func TestChunker_FiveUtterancesOneInsert(t *testing.T) {
	store := &fakeChunkStore{}
	bus, fs, c := newChunker(t, store, nil, ChunkerConfig{})

	for i := 1; i <= 5; i++ {
		bus.Publish(voiceevent.STTFinal{At: at(i), Text: "line" + string(rune('0'+i)), TurnID: "t"})
	}

	// Drain the async writer via the flush barrier (the auto-close at 5 already
	// enqueued the insert; FlushSession only adds the barrier here).
	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}

	got := store.all()
	if len(got) != 1 {
		t.Fatalf("inserts = %d, want 1 (five utterances = one chunk)", len(got))
	}
	want := "Player / DM: line1\nPlayer / DM: line2\nPlayer / DM: line3\nPlayer / DM: line4\nPlayer / DM: line5"
	if got[0].Content != want {
		t.Errorf("content = %q,\nwant %q", got[0].Content, want)
	}
	if len(got[0].ParticipatedAgentIDs) != 0 {
		t.Errorf("participated = %v, want empty (human-only chunk)", got[0].ParticipatedAgentIDs)
	}
	if len(got[0].SpeakerDiscordUserIDs) != 0 {
		t.Errorf("speakers = %v, want empty (anonymous STT lane)", got[0].SpeakerDiscordUserIDs)
	}
}

// TestChunker_WindowClosesWithTwo is #104 rule CLOSE: with two utterances the
// window timer closes the chunk on its own (no fifth utterance, no session end).
func TestChunker_WindowClosesWithTwo(t *testing.T) {
	store := &fakeChunkStore{}
	bus, _, _ := newChunker(t, store, nil, ChunkerConfig{Window: 40 * time.Millisecond})

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "one", TurnID: "t"})
	bus.Publish(voiceevent.STTFinal{At: at(2), Text: "two", TurnID: "t"})

	if !eventually(t, 2*time.Second, func() bool { return store.count() == 1 }) {
		t.Fatalf("window did not close the 2-utterance chunk: inserts = %d", store.count())
	}
	if lines := strings.Count(store.all()[0].Content, "\n") + 1; lines != 2 {
		t.Errorf("closed chunk has %d lines, want 2", lines)
	}
}

// TestChunker_WindowKeepsLoneUtterance is #104 rule CLOSE + ADR-0011: a single
// utterance is NOT closed by the window (timer fire with count==1 keeps it open);
// it is flushed only at session end (FlushSession).
func TestChunker_WindowKeepsLoneUtterance(t *testing.T) {
	store := &fakeChunkStore{}
	bus, fs, c := newChunker(t, store, nil, ChunkerConfig{Window: 40 * time.Millisecond})

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "alone", TurnID: "t"})

	// Well past the window: the lone utterance must still be open (0 inserts).
	time.Sleep(150 * time.Millisecond)
	if n := store.count(); n != 0 {
		t.Fatalf("lone utterance closed by window: inserts = %d, want 0", n)
	}

	// Session end is the ONLY way a lone utterance flushes.
	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	if n := store.count(); n != 1 {
		t.Fatalf("session end did not flush the lone utterance: inserts = %d, want 1", n)
	}
	if store.all()[0].Content != "Player / DM: alone" {
		t.Errorf("content = %q", store.all()[0].Content)
	}
}

// TestChunker_SecondUtteranceAfterWindowClosesImmediately is #104 rule CLOSE: a
// timer fire with count==1 leaves the chunk open; the next utterance re-checks
// elapsed and closes it immediately.
func TestChunker_SecondUtteranceAfterWindowClosesImmediately(t *testing.T) {
	store := &fakeChunkStore{}
	bus, _, _ := newChunker(t, store, nil, ChunkerConfig{Window: 40 * time.Millisecond})

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "one", TurnID: "t"})
	time.Sleep(120 * time.Millisecond) // timer fired with count==1, chunk stayed open
	if n := store.count(); n != 0 {
		t.Fatalf("chunk closed with a lone utterance: inserts = %d", n)
	}
	bus.Publish(voiceevent.STTFinal{At: at(2), Text: "two", TurnID: "t"})

	if !eventually(t, 2*time.Second, func() bool { return store.count() == 1 }) {
		t.Fatalf("second utterance past the window did not close immediately: inserts = %d", store.count())
	}
}

// TestChunker_AgentReplyCoalesces is #104 rule UTTERANCES: an Agent turn is ONE
// utterance no matter how many TTS sentences it spans (they append to one line),
// and participated_agent_ids carries exactly that Agent's DB UUID.
func TestChunker_AgentReplyCoalesces(t *testing.T) {
	store := &fakeChunkStore{}
	bus, fs, c := newChunker(t, store, nil, ChunkerConfig{})
	agentID := uuid.New()

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "Hello Bart", TurnID: "t1"})
	bus.Publish(voiceevent.AddressRouted{
		At: at(2), TurnID: "t1",
		Target: voiceevent.AddressTarget{AgentID: agentID.String(), AgentRole: "character", Name: "Bart"},
	})
	bus.Publish(voiceevent.TTSInvoked{At: at(3), Sentence: "Well met.", Index: 0, TurnID: "t1"})
	bus.Publish(voiceevent.TTSInvoked{At: at(4), Sentence: "Sit down.", Index: 1, TurnID: "t1"})
	bus.Publish(voiceevent.TTSInvoked{At: at(5), Sentence: "What'll it be?", Index: 2, TurnID: "t1"})
	bus.Publish(voiceevent.STTFinal{At: at(6), Text: "An ale", TurnID: "t2"})

	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}

	got := store.all()
	if len(got) != 1 {
		t.Fatalf("inserts = %d, want 1", len(got))
	}
	want := "Player / DM: Hello Bart\nBart: Well met. Sit down. What'll it be?\nPlayer / DM: An ale"
	if got[0].Content != want {
		t.Errorf("content = %q,\nwant %q", got[0].Content, want)
	}
	if len(got[0].ParticipatedAgentIDs) != 1 || got[0].ParticipatedAgentIDs[0] != agentID {
		t.Errorf("participated = %v, want exactly [%s]", got[0].ParticipatedAgentIDs, agentID)
	}
}

// TestChunker_HumanOnlyChunkEmptyParticipants is #104 AC: a chunk with no Agent
// reply has empty participated_agent_ids.
func TestChunker_HumanOnlyChunkEmptyParticipants(t *testing.T) {
	store := &fakeChunkStore{}
	bus, fs, c := newChunker(t, store, nil, ChunkerConfig{})

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "just me", TurnID: "t1"})
	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	got := store.all()
	if len(got) != 1 || len(got[0].ParticipatedAgentIDs) != 0 {
		t.Fatalf("participated = %v, want empty", got)
	}
}

// TestChunker_GaugeSetFromCount is #104 AC: after each written chunk the gauge is
// Set to the store's live NULL-embedding count, rising by one per new chunk.
func TestChunker_GaugeSetFromCount(t *testing.T) {
	store := &fakeChunkStore{}
	gauge := &fakeGauge{}
	// MaxUtterances=1 so each STTFinal closes its own chunk.
	bus, fs, c := newChunker(t, store, gauge, ChunkerConfig{MaxUtterances: 1})

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "a", TurnID: "t1"})
	bus.Publish(voiceevent.STTFinal{At: at(2), Text: "b", TurnID: "t2"})
	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}

	sets := gauge.snapshot()
	if len(sets) != 2 || sets[0] != 1 || sets[1] != 2 {
		t.Fatalf("gauge sets = %v, want [1 2] (rises by one per written chunk)", sets)
	}
}

// blockingChunkStore blocks every InsertTranscriptChunk on release, signalling
// the first entry on entered — so the writer goroutine can be pinned mid-insert
// and the bounded queue driven to overflow.
type blockingChunkStore struct {
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	inserts int
}

func (b *blockingChunkStore) InsertTranscriptChunk(_ context.Context, _ storage.TranscriptChunk) (uuid.UUID, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	b.mu.Lock()
	b.inserts++
	b.mu.Unlock()
	return uuid.New(), nil
}
func (b *blockingChunkStore) CountUnembeddedChunks(_ context.Context) (int, error) { return 0, nil }
func (b *blockingChunkStore) done() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inserts
}

// TestChunker_BusCallbackNeverBlocks is #104 WRITE PATH (voiceevent.Bus contract):
// the bus callback must never block on the DB. With the single writer pinned
// mid-insert and the bounded queue full, further chunk closes are dropped + logged
// rather than blocking Publish — so only the in-flight + queued chunks (1 + cap)
// are ever written.
func TestChunker_BusCallbackNeverBlocks(t *testing.T) {
	store := &blockingChunkStore{entered: make(chan struct{}, 1), release: make(chan struct{})}
	cap := &capHandler{}
	bus := voiceevent.NewBus()
	fs := &fakeSessions{id: uuid.New(), active: true}
	// MaxUtterances=1: each STTFinal closes and enqueues its own chunk insert. The
	// chunker is driven entirely through the bus, so it needs no local handle.
	_ = NewChunker(bus, fs, store, nil, slog.New(cap), ChunkerConfig{MaxUtterances: 1})

	// First close: the writer dequeues it and pins on release. entered confirms the
	// queue is drained to empty before we fill it, so the accepted count is exact.
	bus.Publish(voiceevent.STTFinal{At: at(0), Text: "0", TurnID: "t0"})
	select {
	case <-store.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("writer never reached InsertTranscriptChunk")
	}

	// Fill the bounded queue (chunkQueue) and then overflow it. Each Publish must
	// return promptly; a blocked send would hang the test (caught by go test).
	total := chunkQueue + 25
	done := make(chan struct{})
	go func() {
		for i := 1; i <= total; i++ {
			bus.Publish(voiceevent.STTFinal{At: at(i), Text: "x", TurnID: "t" + string(rune(i))})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a full-queue Publish blocked the bus callback")
	}

	if !cap.has("queue full") {
		t.Errorf("overflow was not logged")
	}

	// Release the writer; exactly 1 (in-flight) + chunkQueue accepted are written,
	// the rest dropped.
	close(store.release)
	want := 1 + chunkQueue
	if !eventually(t, 3*time.Second, func() bool { return store.done() == want }) {
		t.Fatalf("writer wrote %d chunks, want %d (in-flight + queue; overflow dropped)", store.done(), want)
	}
}
