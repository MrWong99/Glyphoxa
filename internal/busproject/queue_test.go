package busproject

import (
	"context"
	"sync"
	"testing"
	"time"
)

// blockingSink is a write sink the test can pin: the first write signals
// entered and parks on release, so the queue's backlog fills deterministically.
type blockingSink struct {
	entered chan struct{}
	release chan struct{}

	mu      sync.Mutex
	written []int
}

func newBlockingSink() *blockingSink {
	return &blockingSink{entered: make(chan struct{}, 1), release: make(chan struct{})}
}

func (s *blockingSink) write(n int) {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	s.mu.Lock()
	defer s.mu.Unlock()
	s.written = append(s.written, n)
}

func (s *blockingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.written)
}

func (s *blockingSink) items() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.written...)
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

// TestQueue_OverflowDrops: the enqueue is non-blocking — with the writer pinned
// and the channel full, Enqueue reports the drop instead of ever blocking, and
// exactly in-flight + capacity items survive.
func TestQueue_OverflowDrops(t *testing.T) {
	const cap = 4
	sink := newBlockingSink()
	q := NewQueue(cap, sink.write)

	// First item: the writer dequeues it and parks — the channel is drained to
	// empty, so the accepted count below is exact.
	if !q.Enqueue(0) {
		t.Fatal("first Enqueue dropped on an empty queue")
	}
	select {
	case <-sink.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("writer never reached the sink")
	}

	accepted, dropped := 0, 0
	for i := 1; i <= cap+3; i++ {
		if q.Enqueue(i) {
			accepted++
		} else {
			dropped++
		}
	}
	if accepted != cap || dropped != 3 {
		t.Fatalf("accepted %d dropped %d, want %d accepted (capacity) and 3 dropped", accepted, dropped, cap)
	}

	close(sink.release)
	want := 1 + cap
	if !eventually(t, 3*time.Second, func() bool { return sink.count() == want }) {
		t.Fatalf("writer wrote %d items, want %d (in-flight + queue; overflow dropped)", sink.count(), want)
	}
}

// TestQueue_FlushDrainsBeforeBarrier: the barrier rides the SAME channel as the
// items, so FIFO guarantees every item enqueued before Flush has been written
// when the barrier fn runs — even when the writer was parked mid-item at flush
// time. This is what makes the Relay's post-drain COUNT(*) authoritative
// (ADR-0040).
func TestQueue_FlushDrainsBeforeBarrier(t *testing.T) {
	sink := newBlockingSink()
	q := NewQueue(8, sink.write)

	// Writer parks on item 1; items 2 and 3 queue behind it.
	q.Enqueue(1)
	select {
	case <-sink.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("writer never reached the sink")
	}
	q.Enqueue(2)
	q.Enqueue(3)

	barrierSaw := make(chan int, 1)
	flushed := make(chan error, 1)
	go func() {
		flushed <- q.Flush(context.Background(), func() { barrierSaw <- sink.count() })
	}()

	close(sink.release)
	select {
	case err := <-flushed:
		if err != nil {
			t.Fatalf("Flush: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Flush never returned after release")
	}
	if saw := <-barrierSaw; saw != 3 {
		t.Errorf("barrier ran with %d items written, want all 3 enqueued before it", saw)
	}
	if got := sink.items(); len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("items written %v, want FIFO [1 2 3]", got)
	}
}

// TestQueue_FlushCtxBoundsEnqueue: with the channel full of items, the barrier
// send blocks — an expired ctx aborts it with ctx.Err() instead of hanging.
func TestQueue_FlushCtxBoundsEnqueue(t *testing.T) {
	sink := newBlockingSink()
	q := NewQueue(1, sink.write)

	q.Enqueue(1) // writer parks on it
	select {
	case <-sink.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("writer never reached the sink")
	}
	q.Enqueue(2) // fills the channel

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.Flush(ctx, nil); err != context.Canceled {
		t.Fatalf("Flush on a full queue with expired ctx = %v, want context.Canceled", err)
	}
	close(sink.release)
}

// TestQueue_FlushCtxBoundsWait: a barrier already enqueued keeps its FIFO slot,
// but the caller stops waiting on ctx expiry; the barrier still runs later and
// its result is discarded.
func TestQueue_FlushCtxBoundsWait(t *testing.T) {
	sink := newBlockingSink()
	q := NewQueue(8, sink.write)

	q.Enqueue(1) // writer parks; the barrier below sits queued behind it
	select {
	case <-sink.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("writer never reached the sink")
	}

	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	flushed := make(chan error, 1)
	go func() {
		flushed <- q.Flush(ctx, func() { close(ran) })
	}()
	// Wait for the barrier to actually sit in the channel — cancelling before
	// the enqueue would abort the send instead of abandoning the wait.
	if !eventually(t, 2*time.Second, func() bool { return len(q.ch) == 1 }) {
		t.Fatal("barrier never reached the queue")
	}
	// The barrier is queued but cannot run; cancel makes Flush return now.
	cancel()
	select {
	case err := <-flushed:
		if err != context.Canceled {
			t.Fatalf("Flush = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Flush did not honor ctx cancellation while waiting")
	}

	// The abandoned barrier still drains — the writer never wedges on it.
	close(sink.release)
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("abandoned barrier never ran")
	}
}

// TestQueue_FlushNilFn: a barrier with no fn is a pure drain.
func TestQueue_FlushNilFn(t *testing.T) {
	sink := newBlockingSink()
	close(sink.release) // sink never parks
	q := NewQueue(8, sink.write)

	q.Enqueue(1)
	q.Enqueue(2)
	if err := q.Flush(context.Background(), nil); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := sink.count(); got != 2 {
		t.Errorf("flushed with %d items written, want 2", got)
	}
}

// TestQueue_NilQueueIsDisabledPersistence: the nil Queue is the
// persistence-disabled projector — Enqueue accepts silently (nothing to log as
// dropped), Flush returns nil WITHOUT running the barrier fn (the Relay's
// disabled Finalize reports count 0 without touching the store).
func TestQueue_NilQueueIsDisabledPersistence(t *testing.T) {
	var q *Queue[int]
	if !q.Enqueue(1) {
		t.Error("nil queue reported a drop; disabled persistence must be silent")
	}
	if err := q.Flush(context.Background(), func() { t.Error("nil queue ran the barrier fn") }); err != nil {
		t.Errorf("nil queue Flush = %v, want nil", err)
	}
}
