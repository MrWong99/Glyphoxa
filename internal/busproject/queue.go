package busproject

import "context"

// Queue is the async write queue shared by the bus projections (#447, mined
// from #74/#104): a bounded channel drained by ONE writer goroutine. The bus
// delivers project synchronously and must not block, so Enqueue is
// non-blocking — an overflow is DROPPED (reported to the caller, who logs it)
// rather than ever calling the DB inline; durability is best-effort while
// live. Flush sends a barrier through the SAME channel, so FIFO ordering
// guarantees every item enqueued before it has been written when the barrier
// runs — that is what makes a drain-at-Stop count authoritative (ADR-0040).
//
// A nil *Queue is the persistence-disabled projector: Enqueue accepts (and
// discards) silently, Flush returns immediately without running its barrier.
// The writer goroutine lives for the process, like the subscription it drains.
type Queue[T any] struct {
	ch chan queueOp[T]
}

// queueOp is one item on the writer channel: an item to write, or a flush
// barrier. Exactly one is set; barrier != nil discriminates.
type queueOp[T any] struct {
	item    T
	barrier *flushBarrier
}

// flushBarrier is the Flush marker: the writer runs fn (if any) only after
// every item enqueued before it has been written, then closes done.
type flushBarrier struct {
	fn   func()
	done chan struct{}
}

// NewQueue starts the single writer goroutine draining a channel of the given
// capacity into write. write is called serially, one item at a time; it owns
// its own timeout and error handling (a failed write must not stop the loop —
// the queue has no opinion on durability beyond FIFO).
func NewQueue[T any](capacity int, write func(T)) *Queue[T] {
	q := &Queue[T]{ch: make(chan queueOp[T], capacity)}
	go q.loop(write)
	return q
}

// loop is the single writer goroutine: it serially writes queued items and
// services flush barriers.
func (q *Queue[T]) loop(write func(T)) {
	for op := range q.ch {
		if op.barrier != nil {
			if op.barrier.fn != nil {
				op.barrier.fn()
			}
			close(op.barrier.done)
			continue
		}
		write(op.item)
	}
}

// Enqueue tees one item onto the writer channel with a non-blocking send — the
// bus must not block — and reports false when the queue is full: the item is
// dropped and the CALLER logs it (it holds the item's identifying context).
// A nil Queue (persistence disabled) accepts silently: nothing was dropped,
// there is just nowhere to write.
func (q *Queue[T]) Enqueue(item T) bool {
	if q == nil {
		return true
	}
	select {
	case q.ch <- queueOp[T]{item: item}:
		return true
	default:
		return false
	}
}

// Flush drains the queue via a barrier riding the SAME channel as the items:
// when it returns nil, every item enqueued before the call has been written.
// fn (optional) runs in the writer goroutine after that drain and before Flush
// returns — the seam for an authoritative post-drain read like the Relay's
// COUNT(*) (ADR-0040). ctx bounds both the enqueue (the channel may be full of
// items) and the wait; on expiry Flush returns ctx.Err() — an already-queued
// barrier still runs, its result discarded. A nil Queue returns nil without
// running fn.
func (q *Queue[T]) Flush(ctx context.Context, fn func()) error {
	if q == nil {
		return nil
	}
	b := &flushBarrier{fn: fn, done: make(chan struct{})}
	select {
	case q.ch <- queueOp[T]{barrier: b}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-b.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
