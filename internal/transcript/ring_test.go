package transcript

import (
	"testing"
)

// fillRing pushes n frames numbered 1..n, the way emit does.
func fillRing(q *frameRing, n int) {
	for i := 1; i <= n; i++ {
		q.push(Frame{Seq: uint64(i), Event: "line"})
	}
}

// wantSeqs asserts got holds exactly the frames first..last in ascending order.
func wantSeqs(t *testing.T, got []Frame, first, last uint64) {
	t.Helper()
	if want := int(last - first + 1); len(got) != want {
		t.Fatalf("got %d frames, want %d (%d..%d)", len(got), want, first, last)
	}
	for i, f := range got {
		if want := first + uint64(i); f.Seq != want {
			t.Fatalf("frame %d has seq %d, want %d", i, f.Seq, want)
		}
	}
}

func TestFrameRing_Empty(t *testing.T) {
	t.Parallel()
	var q frameRing
	if got := q.after(0); got != nil {
		t.Fatalf("empty ring after(0) = %v, want nil", got)
	}
}

func TestFrameRing_BelowCapKeepsEverything(t *testing.T) {
	t.Parallel()
	var q frameRing
	fillRing(&q, 10)
	if q.len() != 10 {
		t.Fatalf("len = %d, want 10", q.len())
	}
	wantSeqs(t, q.after(0), 1, 10)
	wantSeqs(t, q.after(7), 8, 10)
	if got := q.after(10); got != nil {
		t.Fatalf("after(newest) = %v, want nil", got)
	}
	if cap(q.buf) >= ringCap {
		t.Fatalf("a short session's ring grew to cap %d; it should stay small", cap(q.buf))
	}
}

func TestFrameRing_EvictsOldestPastCap(t *testing.T) {
	t.Parallel()
	var q frameRing
	const total = ringCap + 100
	fillRing(&q, total)
	if q.len() != ringCap {
		t.Fatalf("len = %d, want %d", q.len(), ringCap)
	}
	// The oldest 100 are gone: the replay starts at 101.
	wantSeqs(t, q.after(0), 101, total)
	// A Last-Event-ID older than the oldest kept frame replays the whole ring.
	wantSeqs(t, q.after(50), 101, total)
	// Exactly the oldest kept frame's predecessor / the oldest kept frame itself.
	wantSeqs(t, q.after(100), 101, total)
	wantSeqs(t, q.after(101), 102, total)
	// A suffix that straddles the wrap point (physical index 0 holds seq 501).
	wantSeqs(t, q.after(ringCap-10), ringCap-9, total)
	// Entirely after the wrap point.
	wantSeqs(t, q.after(total-1), total, total)
	if got := q.after(total); got != nil {
		t.Fatalf("after(newest) = %v, want nil", got)
	}
	if got := q.after(total + 5); got != nil {
		t.Fatalf("after(beyond newest) = %v, want nil", got)
	}
}

// TestFrameRing_AfterReturnsCopy: the replay set leaves the lock, so later
// pushes (which overwrite in place) and caller mutations must not reach it.
func TestFrameRing_AfterReturnsCopy(t *testing.T) {
	t.Parallel()
	var q frameRing
	fillRing(&q, ringCap)
	got := q.after(0)
	for i := 1; i <= ringCap; i++ {
		q.push(Frame{Seq: uint64(ringCap + i), Event: "line"})
	}
	wantSeqs(t, got, 1, ringCap)

	got[0].Seq = 9999
	wantSeqs(t, q.after(0), ringCap+1, 2*ringCap)
}

// TestFrameRing_PushOnFullRingDoesNotAllocate pins #638: once a session has
// emitted ringCap frames, every further emit used to copy the whole buffer.
func TestFrameRing_PushOnFullRingDoesNotAllocate(t *testing.T) {
	var q frameRing
	fillRing(&q, ringCap+1)
	seq := uint64(ringCap + 1)
	allocs := testing.AllocsPerRun(100, func() {
		seq++
		q.push(Frame{Seq: seq, Event: "line"})
	})
	if allocs != 0 {
		t.Fatalf("push on a full ring allocated %.0f times per call, want 0", allocs)
	}
}

// TestFrameRing_AfterAllocatesOnce: the replay set is sized up front rather
// than grown by append.
func TestFrameRing_AfterAllocatesOnce(t *testing.T) {
	var q frameRing
	fillRing(&q, ringCap+100)
	allocs := testing.AllocsPerRun(100, func() {
		_ = q.after(0)
	})
	if allocs != 1 {
		t.Fatalf("after allocated %.0f times per call, want 1", allocs)
	}
}
