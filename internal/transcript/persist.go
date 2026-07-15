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

// persist tees one emitted Line onto the writer queue, keyed for the coalescing
// UPSERT and carrying seq as the ordering key. Caller holds r.mu; the send is
// non-blocking (the bus must not block) so an overflow drops + logs. No-op when
// persistence is disabled (nil queue).
func (r *Relay) persist(l Line, seq uint64) {
	sess := r.proj.Session()
	line := &storage.TranscriptLine{
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
	if !r.queue.Enqueue(line) {
		r.log.Warn("transcript: persist queue full, dropping line", "line_id", l.ID)
	}
}

// writeLine is the Relay's flush sink — the write half of the single writer
// goroutine (#74): one serial, bounded UPSERT per queued line. A write failure
// is logged but does not stop the loop — durability is best-effort and the next
// Stop's COUNT(*) is still authoritative over whatever landed.
func (r *Relay) writeLine(l *storage.TranscriptLine) {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	if err := r.store.UpsertTranscriptLine(ctx, *l); err != nil {
		r.log.Warn("transcript: persist line", "err", err, "line_id", l.LineID)
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
	r.endSession(id)
	var (
		n   int
		err error
	)
	if ferr := r.queue.Flush(ctx, func() { n, err = r.store.CountTranscriptLines(ctx, id) }); ferr != nil {
		return 0, ferr
	}
	return n, err
}
