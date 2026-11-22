package recall

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// slotEmbedder models the embeddings provider the #700 pile-up happened on: it
// serves at most `slots` requests at a time (one, for a single local model), a
// request waits for a free slot, and a request holding a slot runs until release
// is closed or its context is cancelled. Every finished call is recorded with the
// error it returned, so a test can tell a cancelled embed from a completed one.
type slotEmbedder struct {
	slots   chan struct{}
	entered chan string // the text of each call, sent when it GETS a slot
	release chan struct{}

	mu       sync.Mutex
	finished []slotCall
}

type slotCall struct {
	text string
	err  error
}

func newSlotEmbedder(slots int) *slotEmbedder {
	return &slotEmbedder{
		slots:   make(chan struct{}, slots),
		entered: make(chan string, 16),
		release: make(chan struct{}),
	}
}

func (s *slotEmbedder) Embed(ctx context.Context, texts []string) (vecs [][]float32, err error) {
	// The call is recorded BEFORE its slot is freed: a test that sees the next
	// call enter the slot may read this one's outcome at once.
	held := false
	defer func() {
		s.mu.Lock()
		s.finished = append(s.finished, slotCall{text: texts[0], err: err})
		s.mu.Unlock()
		if held {
			<-s.slots
		}
	}()
	select {
	case s.slots <- struct{}{}:
		held = true
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	s.entered <- texts[0]
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return [][]float32{fixedVec()}, nil
}

// awaitEntered waits for the next call to get a slot and returns its text.
func (s *slotEmbedder) awaitEntered(t *testing.T, what string) string {
	t.Helper()
	select {
	case text := <-s.entered:
		return text
	case <-time.After(2 * time.Second):
		t.Fatalf("no embed got a slot: %s", what)
		return ""
	}
}

func (s *slotEmbedder) calls() []slotCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.finished)
}

// errOf returns the error the finished call for text returned, and whether such
// a call finished at all.
func (s *slotEmbedder) errOf(text string) (error, bool) {
	for _, c := range s.calls() {
		if c.text == text {
			return c.err, true
		}
	}
	return nil, false
}

// campaignRetriever answers the world search per campaign, so a test can see
// WHOSE chunks a recall was served.
type campaignRetriever struct{ world map[uuid.UUID]string }

func (c campaignRetriever) SearchChunksByAgent(context.Context, uuid.UUID, uuid.UUID, []float32, int) ([]storage.ChunkMatch, error) {
	return nil, nil
}

func (c campaignRetriever) SearchChunksByCampaign(_ context.Context, campaignID uuid.UUID, _ []float32, _ int) ([]storage.ChunkMatch, error) {
	return []storage.ChunkMatch{chunkMatch(c.world[campaignID])}, nil
}

// twoSessions is two live Voice Sessions of two campaigns on one process bus, as
// the session manager wires them (#487).
type twoSessions struct {
	sidA, sidB   uuid.UUID
	campA, campB uuid.UUID
	busA, busB   *voiceevent.Bus
	proc         *voiceevent.Bus
	sessions     multiSessions
}

func newTwoSessions(t *testing.T) *twoSessions {
	t.Helper()
	ts := &twoSessions{
		sidA: uuid.New(), sidB: uuid.New(), campA: uuid.New(), campB: uuid.New(),
		busA: voiceevent.NewBus(), busB: voiceevent.NewBus(), proc: voiceevent.NewBus(),
	}
	ts.sessions = multiSessions{live: map[uuid.UUID]uuid.UUID{ts.sidA: ts.campA, ts.sidB: ts.campB}}
	t.Cleanup(voiceevent.Forward(ts.busA, ts.proc, ts.sidA.String()))
	t.Cleanup(voiceevent.Forward(ts.busB, ts.proc, ts.sidB.String()))
	return ts
}

func (ts *twoSessions) ctxA() context.Context {
	return session.NewContext(context.Background(), session.Identity{SessionID: ts.sidA, CampaignID: ts.campA})
}

func (ts *twoSessions) ctxB() context.Context {
	return session.NewContext(context.Background(), session.Identity{SessionID: ts.sidB, CampaignID: ts.campB})
}

// settleSpeculator returns once the serial speculator has finished every pass
// that was pending when it was called: the first wake is taken only after the
// passes before it, and the second send returns only once the first is taken.
func settleSpeculator(r *Recaller) {
	r.signal <- struct{}{}
	r.signal <- struct{}{}
	r.signal <- struct{}{}
}

// TestFinal_CancelsTheStaleEmbedAndTakesItsSlot is the heart of #700 on the
// provider it happened on — one embeddings slot. A partial's speculation holds
// the slot when the final (a different text) arrives. The final must not queue
// behind it: the stale embed is cancelled and the final's own embed gets the slot
// while the provider is still "busy" (nothing is released until the assertions
// are done). Without the cancel the final waits for the stale embed and the
// turn's recall runs over its budget — "degraded to no-memory".
func TestFinal_CancelsTheStaleEmbedAndTakesItsSlot(t *testing.T) {
	const (
		partial = "I walk to the bar."
		final   = "I walk to the bar and ask the keeper."
	)
	emb := newSlotEmbedder(1)
	bus := voiceevent.NewBus()
	sess := fakeSessions{campaignID: uuid.New(), active: true}
	newTestRecaller(t, emb, &fakeRetriever{}, sess, newFakeMetrics(), bus, Config{Budget: 5 * time.Second})
	t.Cleanup(func() { close(emb.release) })

	bus.Publish(voiceevent.STTPartial{Text: partial, UtteranceID: "u1"})
	if got := emb.awaitEntered(t, "the partial's speculation"); got != partial {
		t.Fatalf("the slot went to %q, want the partial", got)
	}
	bus.Publish(voiceevent.STTFinal{Text: final, UtteranceID: "u1"})

	if got := emb.awaitEntered(t, "the final's embed — it is queued behind the stale partial's"); got != final {
		t.Fatalf("the slot went to %q, want the final", got)
	}
	err, done := emb.errOf(partial)
	if !done || !errors.Is(err, context.Canceled) {
		t.Errorf("the partial's embed finished=%v err=%v, want it cancelled by the final", done, err)
	}
}

// TestFinal_OfAnotherCampaign_NeitherJoinsNorCancelsASpeculation is the tenant
// boundary of the preemption (#487): the speculation in flight belongs to
// campaign A. A final in campaign B — even one that normalizes to the very same
// words — must not join it (B's recall would be served A's world chunks) and
// must not cancel it (one table could starve another's prefetch).
func TestFinal_OfAnotherCampaign_NeitherJoinsNorCancelsASpeculation(t *testing.T) {
	const partialA = "Do you remember the knight."
	for _, tc := range []struct{ name, finalB string }{
		{"same words", "Do you remember the knight?"},
		{"other words", "Where is the blacksmith today?"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := newTwoSessions(t)
			emb := newSlotEmbedder(2)
			ret := campaignRetriever{world: map[uuid.UUID]string{ts.campA: "a-world", ts.campB: "b-world"}}
			m := newFakeMetrics()
			r := New(emb, ret, ts.sessions, ts.proc, m, testLogger(), Config{Budget: 5 * time.Second})
			t.Cleanup(r.Close)

			ts.busA.Publish(voiceevent.STTPartial{Text: partialA, UtteranceID: "a1"})
			emb.awaitEntered(t, "campaign A's speculation")
			ts.busB.Publish(voiceevent.STTFinal{Text: tc.finalB, UtteranceID: "b1"})
			if got := emb.awaitEntered(t, "campaign B's own embed (it must not join campaign A's)"); got != tc.finalB {
				t.Fatalf("the second embed is for %q, want campaign B's final", got)
			}
			close(emb.release)

			memB := r.Recall(ts.ctxB(), uuid.NewString(), tc.finalB)
			if len(memB.World) != 1 || !strings.Contains(memB.World[0], "b-world") {
				t.Errorf("campaign B's recall got world %q, want only its own chunk", memB.World)
			}
			settleSpeculator(r)
			if err, done := emb.errOf(partialA); !done || err != nil {
				t.Errorf("campaign A's speculation finished=%v err=%v, want it to complete untouched", done, err)
			}
			// ...and A's turn still gets its prefetch.
			hitsBefore := m.count(observe.RecallHit)
			memA := r.Recall(ts.ctxA(), uuid.NewString(), partialA)
			if len(memA.World) != 1 || !strings.Contains(memA.World[0], "a-world") {
				t.Errorf("campaign A's recall got world %q, want its own chunk", memA.World)
			}
			if got := m.count(observe.RecallHit) - hitsBefore; got != 1 {
				t.Errorf("campaign A's recall: hits = %d, want 1 (its speculation survived B's final)", got)
			}
		})
	}
}

// twoSessionSeamRecaller is a recaller over ts with the fake clock installed, so
// the embed rate limit defers without real waits.
func twoSessionSeamRecaller(t *testing.T, ts *twoSessions, emb *slotEmbedder, sleep func(context.Context, time.Duration) error) *Recaller {
	t.Helper()
	clock := newFakeClock()
	r := newRecaller(emb, &fakeRetriever{}, ts.sessions, newFakeMetrics(), testLogger(), Config{Budget: 5 * time.Second})
	r.now = clock.now
	r.sleep = clock.sleep
	if sleep != nil {
		r.sleep = func(ctx context.Context, d time.Duration) error {
			if err := sleep(ctx, d); err != nil {
				return err
			}
			return clock.sleep(ctx, d)
		}
	}
	r.start(ts.proc)
	t.Cleanup(r.Close)
	return r
}

// TestFinal_OfAnotherSession_LeavesThisSessionsPartialAlone: a final ends the
// speculation of ITS session only. Session A's newer partial is waiting in the
// mailbox when session B's final arrives; it must still be speculated on.
func TestFinal_OfAnotherSession_LeavesThisSessionsPartialAlone(t *testing.T) {
	const (
		p1 = "I walk to the bar."
		p2 = "I walk to the bar and ask the keeper."
	)
	ts := newTwoSessions(t)
	emb := newSlotEmbedder(4)
	r := twoSessionSeamRecaller(t, ts, emb, nil)

	ts.busA.Publish(voiceevent.STTPartial{Text: p1, UtteranceID: "a1"})
	emb.awaitEntered(t, "session A's first speculation")
	ts.busA.Publish(voiceevent.STTPartial{Text: p2, UtteranceID: "a1"}) // waits in the mailbox
	ts.busB.Publish(voiceevent.STTFinal{Text: "Where is the blacksmith today?", UtteranceID: "b1"})
	emb.awaitEntered(t, "session B's final")
	close(emb.release)
	settleSpeculator(r)

	if err, done := emb.errOf(p2); !done || err != nil {
		t.Errorf("session A's waiting partial: embedded=%v err=%v, want it speculated on (another session's final dropped it)", done, err)
	}
}

// TestFinal_DropsItsSessionsWaitingPartial: the partial waiting in the mailbox
// when its own utterance's final arrives is dropped there and then — it does not
// depend on that final still being the LATEST one when the speculator gets round
// to it (another session's final lands in between here).
func TestFinal_DropsItsSessionsWaitingPartial(t *testing.T) {
	const (
		p1 = "I walk to the bar."
		p2 = "I walk to the bar and ask the keeper."
	)
	ts := newTwoSessions(t)
	emb := newSlotEmbedder(4)
	r := twoSessionSeamRecaller(t, ts, emb, nil)

	ts.busA.Publish(voiceevent.STTPartial{Text: p1, UtteranceID: "a1"})
	emb.awaitEntered(t, "session A's first speculation")
	ts.busA.Publish(voiceevent.STTPartial{Text: p2, UtteranceID: "a1"}) // waits in the mailbox
	// A's final equals the text being speculated on, so it joins that embed and
	// cancels nothing: the speculator stays busy while the next final arrives.
	ts.busA.Publish(voiceevent.STTFinal{Text: p1, UtteranceID: "a1"})
	ts.busB.Publish(voiceevent.STTFinal{Text: "Where is the blacksmith today?", UtteranceID: "b1"})
	emb.awaitEntered(t, "session B's final")
	close(emb.release)
	settleSpeculator(r)

	if _, done := emb.errOf(p2); done {
		t.Errorf("the partial that was waiting when its final arrived was embedded anyway; calls = %+v", emb.calls())
	}
}

// TestFinal_SupersedesAPartialTheSpeculatorAlreadyTook: the speculator has taken
// a partial out of the mailbox and is waiting out the embed rate limit when the
// utterance's final arrives. The mailbox is empty, so only the arrival stamp can
// tell the speculator the partial is stale — it must not embed it after the wait.
func TestFinal_SupersedesAPartialTheSpeculatorAlreadyTook(t *testing.T) {
	const (
		p1    = "I walk to the bar."
		p2    = "I walk to the bar and ask."
		final = "I walk to the bar and ask the keeper."
	)
	ts := newTwoSessions(t)
	emb := newSlotEmbedder(4)
	close(emb.release) // embeds complete at once; the rate-limit wait is the gate
	sleeping, wake := make(chan struct{}, 4), make(chan struct{})
	r := twoSessionSeamRecaller(t, ts, emb, func(ctx context.Context, _ time.Duration) error {
		sleeping <- struct{}{}
		select {
		case <-wake:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	ts.busA.Publish(voiceevent.STTPartial{Text: p1, UtteranceID: "a1"})
	emb.awaitEntered(t, "the first speculation")
	waitSpeculated(t, r)
	ts.busA.Publish(voiceevent.STTPartial{Text: p2, UtteranceID: "a1"})
	select {
	case <-sleeping: // taken from the mailbox, waiting out the rate limit
	case <-time.After(2 * time.Second):
		t.Fatal("the speculator never deferred the second partial")
	}
	ts.busA.Publish(voiceevent.STTFinal{Text: final, UtteranceID: "a1"})
	if got := emb.awaitEntered(t, "the final's embed"); got != final {
		t.Fatalf("embed for %q, want the final", got)
	}
	close(wake)
	settleSpeculator(r)

	if _, done := emb.errOf(p2); done {
		t.Errorf("the partial the final superseded was embedded after the rate-limit wait; calls = %+v", emb.calls())
	}
}

// cancelDuringSearch is a retriever whose world search is under way when its
// context is cancelled.
type cancelDuringSearch struct{ searching chan struct{} }

func (c cancelDuringSearch) SearchChunksByAgent(context.Context, uuid.UUID, uuid.UUID, []float32, int) ([]storage.ChunkMatch, error) {
	return nil, nil
}

func (c cancelDuringSearch) SearchChunksByCampaign(ctx context.Context, _ uuid.UUID, _ []float32, _ int) ([]storage.ChunkMatch, error) {
	close(c.searching)
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestSpeculation_PreemptedDuringTheWorldSearch_IsDroppedQuietly: the final's
// cancel can land after the embed returned, while the world prefetch is running.
// That is still a preempted speculation, not a failed prefetch: no warning, and
// the stale text must not take the campaign's cache slot.
func TestSpeculation_PreemptedDuringTheWorldSearch_IsDroppedQuietly(t *testing.T) {
	logs := &syncBuffer{}
	ret := cancelDuringSearch{searching: make(chan struct{})}
	campaignID := uuid.New()
	r := newRecaller(&fakeEmbedder{vec: fixedVec()}, ret, fakeSessions{campaignID: campaignID, active: true},
		newFakeMetrics(), slog.New(slog.NewTextHandler(logs, nil)), Config{})
	r.start(voiceevent.NewBus())
	t.Cleanup(r.Close)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-ret.searching
		cancel() // the final preempts
	}()
	got := r.embedAndCache(ctx, "I walk to the bar.", "i walk to the bar", campaignID)

	if got.valid {
		t.Errorf("a preempted speculation returned a valid entry: %+v", got)
	}
	if _, hit := r.cacheLookup("i walk to the bar", campaignID); hit {
		t.Error("a preempted speculation took the campaign's cache slot")
	}
	if out := logs.String(); strings.Contains(out, "prefetch failed") || strings.Contains(out, "embed failed") {
		t.Errorf("a preempted speculation logged a failure:\n%s", out)
	}
}
