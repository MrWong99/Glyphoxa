package transcript

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Incremental async persistence (#74, ADR-0040): the projection tees each emitted
// Line into a busproject.Queue — a buffered queue drained by ONE writer goroutine
// that UPSERTs it. The bus delivers project SYNCHRONOUSLY and must not block, so
// the tee is non-blocking (drop + log on overflow) — durability is best-effort
// while live. On Stop the Manager calls Finalize, which sends a flush barrier
// through the SAME queue: the writer processes it only after every line enqueued
// before it has landed, then returns COUNT(*) — the authoritative line_count
// (rows == lines). The queue/barrier mechanics live in the shared scaffold
// (#447); this file keeps only the Relay's tee and flush sink.

const (
	// persistQueue bounds the writer's backlog. Tees past this are dropped (logged)
	// rather than blocking the bus; a healthy DB drains far faster than the live
	// transcript fills it.
	persistQueue = 256
	// writeTimeout bounds one line UPSERT so a stalled DB can't wedge the single
	// writer goroutine indefinitely and back the queue up into drops.
	writeTimeout = 5 * time.Second
)

// lineOp is one item on the relay's writer queue: an UPSERT of a projected Line,
// or the DELETE that reconciles a never-delivered line back out (#437, ADR-0040
// amendment). Both ride the SAME queue so FIFO ordering guarantees a retraction
// can never overtake the UPSERT it undoes — and so the flush barrier at Finalize
// still drains everything before the authoritative COUNT(*).
type lineOp struct {
	line   storage.TranscriptLine
	delete bool
}

// persist tees one emitted Line onto the writer queue, keyed for the coalescing
// UPSERT and carrying seq as the ordering key. Caller holds r.mu; the send is
// non-blocking (the bus must not block) so an overflow drops + logs. No-op when
// persistence is disabled (nil queue).
func (r *Relay) persist(sid string, l Line, seq uint64) {
	sess := r.proj.Session(sid)
	line := storage.TranscriptLine{
		VoiceSessionID:       sess.ID,
		CampaignID:           sess.CampaignID,
		LineID:               l.ID,
		Seq:                  int64(seq), //nolint:gosec // seq is a small monotonic counter
		Who:                  l.Who,
		Tag:                  l.Tag,
		Kind:                 string(l.Kind),
		TS:                   l.TS,
		Text:                 l.Text,
		SpeakerDiscordUserID: l.SpeakerID, // #278: "" (unattributed / Agent) → NULLIF → NULL in storage
	}
	if !r.queue.Enqueue(&lineOp{line: line}) {
		r.log.Warn("transcript: persist queue full, dropping line", "line_id", l.ID)
	}
}

// unpersist tees the retraction of one never-delivered line onto the writer
// queue (#437): the turn ended (or the session finalized) without a single
// delivered sentence, so the optimistically-written row must not outlive the
// session. Same non-blocking discipline as persist — a dropped retraction leaves
// a stale row rather than blocking the bus, and is logged. It reports whether
// the retraction was accepted (a nil queue counts: nothing to undo), so retract
// can keep the line in the reconcile set and the Finalize sweep retries a
// TurnEnded-time drop once the queue has drained. Caller holds r.mu.
func (r *Relay) unpersist(sid, lineID string) bool {
	sess := r.proj.Session(sid)
	op := &lineOp{
		delete: true,
		line:   storage.TranscriptLine{VoiceSessionID: sess.ID, LineID: lineID},
	}
	if !r.queue.Enqueue(op) {
		r.log.Warn("transcript: persist queue full, dropping line retraction", "line_id", lineID)
		return false
	}
	return true
}

// writeLine is the Relay's flush sink — the write half of the single writer
// goroutine (#74): one serial, bounded UPSERT (or #437 retraction DELETE) per
// queued op. A write failure is logged but does not stop the loop — durability
// is best-effort and the next Stop's COUNT(*) is still authoritative over
// whatever landed.
func (r *Relay) writeLine(op *lineOp) {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	if op.delete {
		if err := r.store.DeleteTranscriptLine(ctx, op.line.VoiceSessionID, op.line.LineID); err != nil {
			r.log.Warn("transcript: retract undelivered line", "err", err, "line_id", op.line.LineID)
		}
		return
	}
	if err := r.store.UpsertTranscriptLine(ctx, op.line); err != nil {
		r.log.Warn("transcript: persist line", "err", err, "line_id", op.line.LineID)
	}
}

// Finalize drains the writer queue for a session via a flush barrier and returns
// the authoritative persisted line_count (#74). The Manager calls it on Stop
// BEFORE EndVoiceSession, so the recorded count matches the persisted rows. The
// barrier rides the SAME queue as the line tees, so FIFO ordering guarantees every
// line emitted before Finalize has been written when the count runs. Persistence
// disabled (no store) returns 0.
//
// Finalize is also the relay's session-end signal (#144): the Manager calls it
// at EVERY loop exit — Stop, self-exit (Discord outage, wirenpc error) and
// Shutdown — so it pushes the terminal `status: idle` frame to the attached SSE
// subscribers here, before the flush, independent of whether persistence is
// enabled. Without it a self-terminated session leaves the open EventSource
// silent and the screen "Live" forever.
func (r *Relay) Finalize(ctx context.Context, id uuid.UUID) (int, error) {
	// Close the session's projection (#487): the scaffold runs the relay's
	// finishSession hook — which emits the terminal `status: idle` frame and drops
	// the display state — for exactly this session, then deletes its entry. A
	// session that folded zero events has no entry, so Close is a no-op (no
	// spurious idle frame into another session's stream).
	r.proj.Close(id.String())
	var (
		n   int
		err error
	)
	if ferr := r.queue.Flush(ctx, func() { n, err = r.store.CountTranscriptLines(ctx, id) }); ferr != nil {
		return 0, ferr
	}
	return n, err
}
