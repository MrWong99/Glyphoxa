package highlight

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/tape"
)

// fakeHighlightStore records created highlights and can be told to fail.
type fakeHighlightStore struct {
	mu       sync.Mutex
	created  []storage.Highlight
	failNext bool
}

func (f *fakeHighlightStore) CreateHighlight(_ context.Context, h storage.Highlight) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext {
		f.failNext = false
		return errors.New("boom")
	}
	f.created = append(f.created, h)
	return nil
}

func (f *fakeHighlightStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created)
}

// fakeBlobs is an in-memory blob.Store. put can be gated on a channel to hold the
// worker; putErr forces a Put failure.
type fakeBlobs struct {
	mu        sync.Mutex
	data      map[string][]byte
	gate      chan struct{} // if non-nil, Put blocks until a receive
	putErr    error
	deleteErr error    // if set, Delete returns it (blob-backend failure)
	listErr   error    // if set, List returns it (seam-list failure)
	deleted   []string // keys passed to Delete (compensation assertions)
}

func newFakeBlobs() *fakeBlobs { return &fakeBlobs{data: map[string][]byte{}} }

func (f *fakeBlobs) Put(_ context.Context, key, _ string, r io.Reader, _ int64) error {
	if f.gate != nil {
		<-f.gate
	}
	if f.putErr != nil {
		return f.putErr
	}
	b, _ := io.ReadAll(r)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[key] = b
	return nil
}

func (f *fakeBlobs) Get(_ context.Context, key string) (io.ReadCloser, blob.Meta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.data[key]
	if !ok {
		return nil, blob.Meta{}, blob.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), blob.Meta{ContentType: "audio/wav", Size: int64(len(b))}, nil
}

func (f *fakeBlobs) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.data, key)
	f.deleted = append(f.deleted, key)
	return nil
}

func (f *fakeBlobs) List(_ context.Context, prefix string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []string
	for k := range f.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (f *fakeBlobs) keys() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.data)
}

func (f *fakeBlobs) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.data[key]
	return ok
}

// fakeEnqueuer records the last enqueued job.
type fakeEnqueuer struct {
	mu       sync.Mutex
	kind     string
	payload  any
	runAfter time.Time
	calls    int
}

func (f *fakeEnqueuer) Enqueue(_ context.Context, kind string, payload any, runAfter time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kind = kind
	f.payload = payload
	f.runAfter = runAfter
	f.calls++
	return nil
}

func newTrigger() Trigger {
	t0 := time.Now()
	return Trigger{
		At:         t0,
		From:       t0.Add(-15 * time.Second),
		To:         t0.Add(5 * time.Second),
		Score:      9.5,
		SpeakerIDs: []string{"111", "222"},
		Excerpt:    "natural 20 against the dragon",
		Reason:     "critical hit",
		Snapshot:   tape.Snapshot{From: t0.Add(-15 * time.Second), To: t0.Add(5 * time.Second)},
	}
}

func newTestSaver(t *testing.T) (*Saver, *fakeHighlightStore, *fakeBlobs, *fakeEnqueuer) {
	t.Helper()
	store := &fakeHighlightStore{}
	blobs := newFakeBlobs()
	enq := &fakeEnqueuer{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewSaver(store, blobs, enq, log), store, blobs, enq
}

func TestSaver_HandleTrigger_ClipAndRow(t *testing.T) {
	saver, store, blobs, _ := newTestSaver(t)
	vsID, campID, tenID := uuid.New(), uuid.New(), uuid.New()

	saver.Begin(vsID, campID, tenID)
	saver.HandleTrigger(newTrigger())
	if err := saver.Finalize(context.Background()); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	if store.count() != 1 {
		t.Fatalf("want 1 highlight row, got %d", store.count())
	}
	h := store.created[0]
	if h.Status != storage.HighlightCandidate {
		t.Fatalf("want candidate status, got %q", h.Status)
	}
	if h.VoiceSessionID != vsID || h.CampaignID != campID || h.TenantID != tenID {
		t.Fatalf("owning ids not threaded: %+v", h)
	}
	if h.Excerpt != "natural 20 against the dragon" || h.Score != 9.5 {
		t.Fatalf("caption fields not threaded: %+v", h)
	}
	if h.ClipSizeBytes == 0 {
		t.Fatalf("clip size not recorded")
	}
	if blobs.keys() != 1 {
		t.Fatalf("want 1 stored clip, got %d", blobs.keys())
	}
	if _, ok := blobs.data[h.ClipKey]; !ok {
		t.Fatalf("row clip_key %q has no stored blob", h.ClipKey)
	}
}

func TestSaver_Finalize_SchedulesPurge(t *testing.T) {
	saver, _, _, enq := newTestSaver(t)
	vsID := uuid.New()

	saver.Begin(vsID, uuid.New(), uuid.New())
	before := time.Now().Add(purgeDelay)
	if err := saver.Finalize(context.Background()); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	after := time.Now().Add(purgeDelay)

	if enq.calls != 1 || enq.kind != JobKindPurgeCandidates {
		t.Fatalf("want one purge enqueue, got calls=%d kind=%q", enq.calls, enq.kind)
	}
	if enq.runAfter.Before(before) || enq.runAfter.After(after.Add(time.Second)) {
		t.Fatalf("purge run_after %v not ~7d out", enq.runAfter)
	}
	p, ok := enq.payload.(purgePayload)
	if !ok || p.VoiceSessionID != vsID {
		t.Fatalf("purge payload wrong: %#v", enq.payload)
	}
}

func TestSaver_Finalize_DrainTimeoutStillEnqueues(t *testing.T) {
	saver, _, blobs, enq := newTestSaver(t)
	blobs.gate = make(chan struct{}) // hold the worker inside Put so the drain can't finish
	defer close(blobs.gate)          // release on test exit so the worker goroutine can reap

	vsID := uuid.New()
	saver.Begin(vsID, uuid.New(), uuid.New())
	saver.HandleTrigger(newTrigger()) // the worker will block on the gated Put

	// A ctx that is already effectively expired: the drain times out.
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	err := saver.Finalize(ctx)
	if err == nil {
		t.Fatalf("want a drain-timeout error, got nil")
	}
	// Drain timing out must NOT lose the purge horizon: the purge is still scheduled
	// off the captured session id (the boot sweep is a backstop, not the only path).
	if enq.calls != 1 || enq.kind != JobKindPurgeCandidates {
		t.Fatalf("drain timeout skipped purge scheduling: calls=%d kind=%q", enq.calls, enq.kind)
	}
	p, ok := enq.payload.(purgePayload)
	if !ok || p.VoiceSessionID != vsID {
		t.Fatalf("purge payload wrong after drain timeout: %#v", enq.payload)
	}
}

func TestSaver_Finalize_IdleNoop(t *testing.T) {
	saver, _, _, enq := newTestSaver(t)
	if err := saver.Finalize(context.Background()); err != nil {
		t.Fatalf("idle finalize: %v", err)
	}
	if enq.calls != 0 {
		t.Fatalf("idle finalize scheduled a purge")
	}
}

func TestSaver_HandleTrigger_NonBlockingDropsWhenFull(t *testing.T) {
	saver, store, blobs, _ := newTestSaver(t)
	blobs.gate = make(chan struct{}) // hold the worker inside the first Put

	saver.Begin(uuid.New(), uuid.New(), uuid.New())

	// Push far more than the mailbox holds; none of these must block.
	done := make(chan struct{})
	go func() {
		for i := 0; i < saveQueue+64; i++ {
			saver.HandleTrigger(newTrigger())
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("HandleTrigger blocked when the queue was full")
	}

	// Release the worker and let it drain the buffered triggers.
	close(blobs.gate)
	if err := saver.Finalize(context.Background()); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	// At most one in-flight + saveQueue buffered ever get saved; the rest dropped.
	if store.count() > saveQueue+1 {
		t.Fatalf("saved %d, expected the overflow to be dropped (<= %d)", store.count(), saveQueue+1)
	}
	if store.count() == 0 {
		t.Fatalf("nothing saved at all")
	}
}

func TestSaver_WorkerFailureSurvives(t *testing.T) {
	saver, store, blobs, _ := newTestSaver(t)
	store.failNext = true // first CreateHighlight fails

	saver.Begin(uuid.New(), uuid.New(), uuid.New())
	saver.HandleTrigger(newTrigger()) // Put ok, then CreateHighlight fails
	saver.HandleTrigger(newTrigger()) // must still be processed
	if err := saver.Finalize(context.Background()); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if store.count() != 1 {
		t.Fatalf("worker did not survive a failed save: got %d rows", store.count())
	}
	// The failed save's clip must be compensated away (ADR-0048): a blob.Put that
	// succeeds followed by a CreateHighlight that fails leaves NO orphan blob, so only
	// the one successfully-rowed clip remains.
	if blobs.keys() != 1 {
		t.Fatalf("orphan clip not cleaned after row-create failure: %d clips left", blobs.keys())
	}
}
