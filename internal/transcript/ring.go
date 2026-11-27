package transcript

// frameRing is one session's fixed-capacity replay buffer (ADR-0014): the last
// ringCap frames in emit order. It grows by append until full, so a short
// session stays small, then overwrites the oldest frame in place — a push on a
// full ring allocates nothing (#638). Not safe for concurrent use; the Relay
// guards it with r.mu.
type frameRing struct {
	buf   []Frame
	start int // index of the oldest frame once len(buf) == ringCap; 0 before
}

// len reports how many frames the ring holds (at most ringCap).
func (q *frameRing) len() int { return len(q.buf) }

// at returns the i-th oldest frame, 0 <= i < len().
func (q *frameRing) at(i int) Frame {
	return q.buf[(q.start+i)%len(q.buf)]
}

// push appends f, dropping the oldest frame once the ring is full.
func (q *frameRing) push(f Frame) {
	if len(q.buf) < ringCap {
		q.buf = append(q.buf, f)
		return
	}
	q.buf[q.start] = f
	q.start = (q.start + 1) % ringCap
}

// after returns the frames with Seq > seq, oldest first, or nil when there are
// none. The result is a fresh copy sized exactly, so the caller may hand it
// outside the lock. Frames are pushed in ascending Seq (emit numbers them), so
// the match is always a suffix: walk back from the newest to find where it
// starts instead of filtering the whole ring.
func (q *frameRing) after(seq uint64) []Frame {
	n := q.len()
	first := n
	for first > 0 && q.at(first-1).Seq > seq {
		first--
	}
	if first == n {
		return nil
	}
	out := make([]Frame, 0, n-first)
	for i := first; i < n; i++ {
		out = append(out, q.at(i))
	}
	return out
}
