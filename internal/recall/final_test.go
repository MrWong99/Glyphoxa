package recall

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// gatedEmbedder holds every Embed call until release is closed, so a test can
// observe a recall that is waiting on an embed already under way.
type gatedEmbedder struct {
	mu      sync.Mutex
	calls   int
	started chan struct{} // receives once per call, when it begins
	release chan struct{}
}

func newGatedEmbedder() *gatedEmbedder {
	return &gatedEmbedder{started: make(chan struct{}, 8), release: make(chan struct{})}
}

func (g *gatedEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	g.mu.Lock()
	g.calls++
	g.mu.Unlock()
	g.started <- struct{}{}
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = fixedVec()
	}
	return out, nil
}

func (g *gatedEmbedder) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

// TestRecall_JoinsTheFinalsOwnEmbed pins the batch-STT path (no partials): the
// final starts its embed when it is published, and the turn's Recall waits on
// that one call instead of issuing a second embed behind it. On a single local
// model the second call would queue behind the first and double the wait — which
// in production put every recall past its budget.
func TestRecall_JoinsTheFinalsOwnEmbed(t *testing.T) {
	emb := newGatedEmbedder()
	ret := &fakeRetriever{
		agentChunks: []storage.ChunkMatch{chunkMatch("I served the knight last night.")},
		campChunks:  []storage.ChunkMatch{chunkMatch("Bandits on the north road.")},
	}
	m := newFakeMetrics()
	bus := voiceevent.NewBus()
	sess := fakeSessions{campaignID: uuid.New(), active: true}
	r := newTestRecaller(t, emb, ret, sess, m, bus, Config{Budget: 5 * time.Second})

	bus.Publish(voiceevent.STTFinal{Text: "Do you remember the knight?"})
	select {
	case <-emb.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the final did not start an embed")
	}

	got := make(chan agent.Memory, 1)
	go func() { got <- r.Recall(recallCtx(sess), uuid.NewString(), "Do you remember the knight?") }()

	select {
	case mem := <-got:
		t.Fatalf("Recall returned %+v before the embed finished; it must wait for the flight", mem)
	case <-time.After(100 * time.Millisecond):
	}
	close(emb.release)

	var mem agent.Memory
	select {
	case mem = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("Recall did not return after the embed finished")
	}
	if emb.callCount() != 1 {
		t.Errorf("embed calls = %d, want 1 (Recall must join the final's embed)", emb.callCount())
	}
	if len(mem.World) != 1 || len(mem.Personal) != 1 {
		t.Errorf("memory = %+v, want one world and one personal chunk", mem)
	}
	// The turn waited for the embed, so this is a miss: "hit" stays the measure
	// of speculation over partials, which costs the turn nothing.
	if m.count(observe.RecallMiss) != 1 || m.count(observe.RecallHit) != 0 || m.count(observe.RecallSkip) != 0 {
		t.Errorf("outcomes: miss=%d hit=%d skip=%d, want miss=1 hit=0 skip=0",
			m.count(observe.RecallMiss), m.count(observe.RecallHit), m.count(observe.RecallSkip))
	}
}

// TestRecall_FinalFlightOverBudget_DegradesWithoutASecondEmbed: when the final's
// embed does not finish inside the budget, the turn goes on without memory — it
// does not pile a second embed onto a provider that is already too slow.
func TestRecall_FinalFlightOverBudget_DegradesWithoutASecondEmbed(t *testing.T) {
	emb := newGatedEmbedder()
	m := newFakeMetrics()
	bus := voiceevent.NewBus()
	sess := fakeSessions{campaignID: uuid.New(), active: true}
	r := newTestRecaller(t, emb, &fakeRetriever{}, sess, m, bus, Config{Budget: 30 * time.Millisecond})
	t.Cleanup(func() { close(emb.release) })

	bus.Publish(voiceevent.STTFinal{Text: "Do you remember the knight?"})
	<-emb.started

	mem := r.Recall(recallCtx(sess), uuid.NewString(), "Do you remember the knight?")
	if len(mem.World)+len(mem.Personal) != 0 {
		t.Errorf("memory = %+v, want none", mem)
	}
	if emb.callCount() != 1 {
		t.Errorf("embed calls = %d, want 1", emb.callCount())
	}
	if m.count(observe.RecallSkip) != 1 {
		t.Errorf("skip = %d, want 1", m.count(observe.RecallSkip))
	}
}

// TestRecall_UnrelatedFinalDoesNotQueueAheadOfATurn: table chatter ("yeah") is a
// final too. Its embed must not sit in front of the addressed utterance's — each
// final speculates on its own, so the turn waits for one embed, not two.
func TestRecall_UnrelatedFinalDoesNotQueueAheadOfATurn(t *testing.T) {
	emb := newGatedEmbedder()
	bus := voiceevent.NewBus()
	sess := fakeSessions{campaignID: uuid.New(), active: true}
	r := newTestRecaller(t, emb, &fakeRetriever{}, sess, newFakeMetrics(), bus, Config{Budget: 5 * time.Second})

	bus.Publish(voiceevent.STTFinal{Text: "yeah"})
	bus.Publish(voiceevent.STTFinal{Text: "Do you remember the knight?"})
	for range 2 {
		select {
		case <-emb.started:
		case <-time.After(2 * time.Second):
			t.Fatal("both finals must start embedding at once; the second is queued behind the first")
		}
	}
	close(emb.release)
	r.Recall(recallCtx(sess), uuid.NewString(), "Do you remember the knight?")
	if emb.callCount() != 2 {
		t.Errorf("embed calls = %d, want 2 (one per final, none inline)", emb.callCount())
	}
}

// TestRecall_FlightOfAnotherCampaignIsNotJoined (#487): a concurrent session
// saying the same words must not lend this turn its flight or its prefetch.
func TestRecall_FlightOfAnotherCampaignIsNotJoined(t *testing.T) {
	emb := &fakeEmbedder{vec: fixedVec()}
	other := fakeSessions{campaignID: uuid.New(), active: true}
	r := newRecaller(emb, &fakeRetriever{}, other, newFakeMetrics(), testLogger(), Config{})
	t.Cleanup(r.Close)
	r.start(voiceevent.NewBus())

	r.onFinal(voiceevent.STTFinal{Text: "Do you remember the knight?", SessionID: uuid.NewString()})
	r.finals.Wait()

	mine := uuid.New()
	if fl := r.flightFor(normalize("Do you remember the knight?"), mine); fl != nil {
		t.Error("joined another campaign's flight")
	}
	if _, hit := r.cacheLookup(normalize("Do you remember the knight?"), mine); hit {
		t.Error("served another campaign's prefetch")
	}
}

// TestWarm_RetriesUntilTheProviderIsUp: the embeddings server commonly starts
// after this process.
func TestWarm_RetriesUntilTheProviderIsUp(t *testing.T) {
	emb := &flakyEmbedder{failures: 2}
	r := newRecaller(emb, &fakeRetriever{}, fakeSessions{}, newFakeMetrics(), testLogger(), Config{})
	t.Cleanup(r.cancel)
	r.sleep = func(context.Context, time.Duration) error { return nil }

	r.Warm(context.Background())
	if emb.calls != 3 {
		t.Errorf("embed calls = %d, want 3 (two failures, then loaded)", emb.calls)
	}
}

type flakyEmbedder struct{ failures, calls int }

func (f *flakyEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	f.calls++
	if f.calls <= f.failures {
		return nil, context.DeadlineExceeded
	}
	return [][]float32{fixedVec()}, nil
}

// TestRecall_FinalAlreadySpeculatedByPartials_NoSecondEmbed: with streaming STT
// the partials usually embed the exact final text first; the final then adds
// nothing.
func TestRecall_FinalAlreadySpeculatedByPartials_NoSecondEmbed(t *testing.T) {
	emb := &fakeEmbedder{vec: fixedVec()}
	bus := voiceevent.NewBus()
	sess := fakeSessions{campaignID: uuid.New(), active: true}
	r := newTestRecaller(t, emb, &fakeRetriever{}, sess, newFakeMetrics(), bus, Config{})

	bus.Publish(voiceevent.STTPartial{Text: "Do you remember the knight?", UtteranceID: "u1"})
	waitSpeculated(t, r)
	bus.Publish(voiceevent.STTFinal{Text: "Do you remember the knight?", UtteranceID: "u1"})
	r.Recall(recallCtx(sess), uuid.NewString(), "Do you remember the knight?")

	if emb.callCount() != 1 {
		t.Errorf("embed calls = %d, want 1", emb.callCount())
	}
}
