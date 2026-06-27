package transcript

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Incremental async persistence (#74, ADR-0040): the projection tees each emitted
// Line into a buffered queue drained by ONE writer goroutine that UPSERTs it. The
// bus delivers project SYNCHRONOUSLY and must not block, so the tee is
// non-blocking (drop + log on overflow) — durability is best-effort while live.
// On Stop the Manager calls Finalize, which sends a flush barrier through the SAME
// queue: the writer processes it only after every line enqueued before it has
// landed, then returns COUNT(*) — the authoritative line_count (rows == lines).

const (
	// persistQueue bounds the writer's backlog. Tees past this are dropped (logged)
	// rather than blocking the bus; a healthy DB drains far faster than the live
	// transcript fills it.
	persistQueue = 256
	// writeTimeout bounds one line UPSERT so a stalled DB can't wedge the single
	// writer goroutine indefinitely and back the queue up into drops.
	writeTimeout = 5 * time.Second
)

// writeOp is one item on the writer queue: a line to UPSERT, or a flush barrier.
// Exactly one field is set.
type writeOp struct {
	line  *storage.TranscriptLine
	flush *flushReq
}

// flushReq is the Finalize barrier: the writer drains every line before it, then
// counts the session's persisted rows and replies on result.
type flushReq struct {
	ctx       context.Context
	sessionID uuid.UUID
	result    chan flushResult
}

type flushResult struct {
	count int
	err   error
}

// persist tees one emitted Line onto the writer queue, keyed for the coalescing
// UPSERT and carrying seq as the ordering key. Caller holds r.mu; the send is
// non-blocking (the bus must not block) so an overflow drops + logs. No-op when
// persistence is disabled.
func (r *Relay) persist(l Line, seq uint64) {
	if r.writeCh == nil {
		return
	}
	op := writeOp{line: &storage.TranscriptLine{
		VoiceSessionID: r.activeUUID,
		CampaignID:     r.activeCampaignID,
		LineID:         l.ID,
		Seq:            int64(seq), //nolint:gosec // seq is a small monotonic counter
		Who:            l.Who,
		Tag:            l.Tag,
		Kind:           string(l.Kind),
		TS:             l.TS,
		Text:           l.Text,
	}}
	select {
	case r.writeCh <- op:
	default:
		r.log.Warn("transcript: persist queue full, dropping line", "line_id", l.ID)
	}
}

// writeLoop is the single writer goroutine (#74): it serially UPSERTs queued lines
// and services flush barriers. A line write failure is logged but does not stop
// the loop — durability is best-effort and the next Stop's COUNT(*) is still
// authoritative over whatever landed.
func (r *Relay) writeLoop() {
	for op := range r.writeCh {
		switch {
		case op.line != nil:
			ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			if err := r.store.UpsertTranscriptLine(ctx, *op.line); err != nil {
				r.log.Warn("transcript: persist line", "err", err, "line_id", op.line.LineID)
			}
			cancel()
		case op.flush != nil:
			n, err := r.store.CountTranscriptLines(op.flush.ctx, op.flush.sessionID)
			op.flush.result <- flushResult{count: n, err: err}
		}
	}
}

// Finalize drains the writer queue for a session via a flush barrier and returns
// the authoritative persisted line_count (#74). The Manager calls it on Stop
// BEFORE EndVoiceSession, so the recorded count matches the persisted rows. The
// barrier rides the SAME queue as the line tees, so FIFO ordering guarantees every
// line emitted before Finalize has been written when the count runs. Persistence
// disabled (no store) returns 0.
func (r *Relay) Finalize(ctx context.Context, id uuid.UUID) (int, error) {
	if r.writeCh == nil {
		return 0, nil
	}
	res := make(chan flushResult, 1)
	select {
	case r.writeCh <- writeOp{flush: &flushReq{ctx: ctx, sessionID: id, result: res}}:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	select {
	case fr := <-res:
		return fr.count, fr.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}
