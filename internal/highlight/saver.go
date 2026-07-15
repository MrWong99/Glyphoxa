package highlight

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/mixdown"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// The Saver is #308's [Sink]: it turns each detector [Trigger] into a stored
// audio clip (behind the blob seam, ADR-0048) plus a 'candidate' highlight row,
// then — on session end — schedules the 7-day candidate purge (ADR-0051/0049).
//
// It follows the same no-block discipline the detector demands of a Sink
// (ADR-0020/0026): HandleTrigger runs on the detector's single worker goroutine,
// so it only hands the trigger to a bounded mailbox and returns; a full mailbox
// drops + logs rather than stalling detection. A separate per-session worker
// goroutine does the blocking I/O (WAVClip → blob.Put → CreateHighlight). A save
// failure logs and is dropped — one missed highlight never crashes the session.
//
// The Saver is a process singleton bound to one Voice Session at a time (there is
// only ever one live session, ADR-0039). Begin binds a session and starts its
// worker; Finalize drains the worker, schedules the purge, and unbinds. It rides
// the VOICE process; the RPC read side and clip serve ride the WEB process — they
// meet only through Postgres (the blob backend), never shared memory.

// saveQueue bounds a session's pending-trigger mailbox. A detector emits at most
// MaxCandidates (#305: 10) triggers a session, so 16 is generous headroom; a
// (pathological) overflow drops the newest and logs.
const saveQueue = 16

// saveTimeout bounds one trigger's WAVClip → blob.Put → CreateHighlight so a
// stalled DB/blob backend can't wedge the single per-session worker.
const saveTimeout = 30 * time.Second

// clipBlobName is the blob.Key name segment for a Highlight's audio clip
// (mirrors the image's imageBlobName). The key is
// t/<tenant>/highlight/<id>/clip.wav; the boot orphan sweep keys off it (#435).
const clipBlobName = "clip.wav"

// compensateTimeout bounds the compensating blob delete after a failed
// CreateHighlight (#435). It is a fresh budget deliberately NOT tied to the save
// ctx: compensation is needed most exactly when that budget has expired.
const compensateTimeout = 10 * time.Second

// Store is the persistence surface the Saver's worker needs; *storage.Store
// satisfies it and tests fake it.
type Store interface {
	CreateHighlight(ctx context.Context, h storage.Highlight) error
}

// JobEnqueuer schedules the candidate purge job (ADR-0049). It is the small
// adapter over storage.EnqueueJob's kind/payload/run_after surface; main.go wires
// it. payload is JSON-marshalled by the adapter.
type JobEnqueuer interface {
	Enqueue(ctx context.Context, kind string, payload any, runAfter time.Time) error
}

// Saver implements [Sink]. Construct with [NewSaver], then drive it from the
// session Manager: Begin at session Start, HandleTrigger via cfg.Highlights,
// Finalize at loop exit.
type Saver struct {
	store   Store
	blobs   blob.Store
	enqueue JobEnqueuer
	log     *slog.Logger

	// saveTimeout is the per-trigger save budget — the saveTimeout constant in
	// production, shrunk by tests that need the budget to expire (#435).
	saveTimeout time.Duration

	mu   sync.Mutex
	sess *saverSession // nil when unbound (idle)
}

// saverSession is the Saver's per-Voice-Session binding: the owning ids and the
// worker's mailbox + done signal. Recreated by each Begin, torn down by Finalize.
type saverSession struct {
	voiceSessionID uuid.UUID
	campaignID     uuid.UUID
	tenantID       uuid.UUID
	queue          chan Trigger
	done           chan struct{} // closed when the worker goroutine exits
}

// NewSaver builds a Saver over the storage + blob + job-enqueue seams.
func NewSaver(store Store, blobs blob.Store, enqueue JobEnqueuer, log *slog.Logger) *Saver {
	if log == nil {
		log = slog.Default()
	}
	return &Saver{store: store, blobs: blobs, enqueue: enqueue, log: log, saveTimeout: saveTimeout}
}

// Begin binds the Saver to a new Voice Session and starts its worker goroutine.
// The Manager calls it at Start, before any trigger can fire. A Begin while a
// prior session is still bound (a missing Finalize) replaces the binding after
// tearing the old worker down — defensive; the normal path is Finalize then Begin.
func (s *Saver) Begin(voiceSessionID, campaignID, tenantID uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sess != nil {
		close(s.sess.queue)
		old := s.sess
		s.sess = nil
		go func() { <-old.done }() // reap without blocking Begin
	}
	ss := &saverSession{
		voiceSessionID: voiceSessionID,
		campaignID:     campaignID,
		tenantID:       tenantID,
		queue:          make(chan Trigger, saveQueue),
		done:           make(chan struct{}),
	}
	s.sess = ss
	go s.worker(ss)
}

// HandleTrigger is the [Sink] impl: a non-blocking hand-off to the bound
// session's worker. It runs on the detector's worker goroutine, so it never does
// I/O inline and never blocks — a full mailbox drops the trigger and logs, and a
// trigger arriving with no session bound (a detector outliving Finalize) is
// likewise dropped. Guarded by mu so a concurrent Finalize can never close the
// queue mid-send.
func (s *Saver) HandleTrigger(t Trigger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ss := s.sess
	if ss == nil {
		s.log.Warn("highlight saver: trigger with no bound session, dropping", "score", t.Score)
		return
	}
	select {
	case ss.queue <- t:
	default:
		s.log.Warn("highlight saver: save queue full, dropping trigger", "score", t.Score)
	}
}

// enqueueTimeout bounds the purge-schedule Enqueue when the caller's ctx has
// already expired during the drain — the purge horizon must still be recorded, so
// it runs on a fresh short budget rather than an already-dead context.
const enqueueTimeout = 5 * time.Second

// Finalize drains the bound session's worker (a flush barrier: it closes the
// mailbox and waits for the worker to finish every queued trigger), then
// schedules the 7-day candidate purge job and unbinds. The Manager calls it at
// EVERY loop exit beside transcript.Finalize (Stop, self-exit, Shutdown), so a
// session's candidates always get a purge horizon. Idle (no bound session) is a
// no-op. A ctx timeout during the drain is returned (the Manager logs it) but the
// purge is STILL scheduled off the captured session id — a drain timeout must not
// lose the purge horizon (that would strand candidates until the boot sweep, which
// is a backstop, not the primary path).
func (s *Saver) Finalize(ctx context.Context) error {
	s.mu.Lock()
	ss := s.sess
	s.sess = nil
	s.mu.Unlock()
	if ss == nil {
		return nil
	}

	// Flush barrier: no more sends can happen (sess is nil, HandleTrigger drops),
	// so closing the mailbox lets the worker drain every buffered trigger and exit.
	close(ss.queue)
	var drainErr error
	select {
	case <-ss.done:
	case <-ctx.Done():
		drainErr = ctx.Err()
	}

	// Schedule the candidate purge 7 days out (ADR-0051) REGARDLESS of the drain
	// outcome — ss captured the session id before the unbind, so a drain timeout can
	// still record the horizon. At-least-once + idempotent (ADR-0049): the handler
	// blob-deletes then row-deletes remaining candidates. If the drain timed out, the
	// caller's ctx is dead, so enqueue on a fresh short budget.
	enqCtx := ctx
	if drainErr != nil {
		var cancel context.CancelFunc
		enqCtx, cancel = context.WithTimeout(context.Background(), enqueueTimeout)
		defer cancel()
	}
	payload := purgePayload{VoiceSessionID: ss.voiceSessionID}
	if err := s.enqueue.Enqueue(enqCtx, JobKindPurgeCandidates, payload, time.Now().Add(purgeDelay)); err != nil {
		return errors.Join(drainErr, err)
	}
	return drainErr
}

// worker is the per-session goroutine: it serially saves each queued trigger and
// exits when the mailbox is closed (Finalize's barrier). A save failure logs and
// is dropped so one bad trigger never stops the drain.
func (s *Saver) worker(ss *saverSession) {
	defer close(ss.done)
	for t := range ss.queue {
		s.save(ss, t)
	}
}

// save encodes one trigger's tape snapshot to a WAV clip, stores it behind the
// blob seam, and writes the 'candidate' highlight row. The blob key is
// deterministic (blob.Key) so the row and its clip agree and the delete hook can
// reconstruct it. Any step failing logs and returns — best-effort durability.
func (s *Saver) save(ss *saverSession, t Trigger) {
	ctx, cancel := context.WithTimeout(context.Background(), s.saveTimeout)
	defer cancel()

	highlightID := uuid.New()
	key, err := blob.Key(ss.tenantID, highlightOwnerKind, highlightID, clipBlobName)
	if err != nil {
		s.log.Error("highlight saver: build clip key", "err", err)
		return
	}
	wav, err := mixdown.WAVClip(t.Snapshot, mixdown.Options{})
	if err != nil {
		s.log.Error("highlight saver: encode clip", "err", err, "voice_session", ss.voiceSessionID)
		return
	}
	if err := s.blobs.Put(ctx, key, "audio/wav", bytes.NewReader(wav), int64(len(wav))); err != nil {
		s.log.Error("highlight saver: store clip", "err", err, "voice_session", ss.voiceSessionID)
		return
	}
	h := storage.Highlight{
		ID:              highlightID,
		TenantID:        ss.tenantID,
		VoiceSessionID:  ss.voiceSessionID,
		CampaignID:      ss.campaignID,
		Status:          storage.HighlightCandidate,
		StartsAt:        t.From,
		EndsAt:          t.To,
		Score:           t.Score,
		Excerpt:         t.Excerpt,
		Reason:          t.Reason,
		SpeakerIDs:      t.SpeakerIDs,
		ClipKey:         key,
		ClipContentType: "audio/wav",
		ClipSizeBytes:   int64(len(wav)),
	}
	if err := s.store.CreateHighlight(ctx, h); err != nil {
		s.log.Error("highlight saver: create highlight row", "err", err, "voice_session", ss.voiceSessionID)
		// Compensate the orphaned clip (ADR-0048): the blob is stored but no row will
		// ever reference it, so drop it through the seam. The delete runs on a FRESH
		// bounded budget (#435, the #421 pattern): the row insert frequently fails
		// BECAUSE the shared save budget expired, and a delete on that dead ctx would
		// deterministically fail too — orphaning consented room audio no row-driven
		// reclaim can see. A compensation failure still only logs — the row create
		// already failed, and a lingering blob is the lesser evil than crashing the
		// worker; the boot orphan sweep (SweepEnrichmentReconciliation) reclaims it.
		dctx, dcancel := context.WithTimeout(context.WithoutCancel(ctx), compensateTimeout)
		defer dcancel()
		if derr := s.blobs.Delete(dctx, key); derr != nil {
			s.log.Error("highlight saver: compensate orphan clip", "err", derr, "voice_session", ss.voiceSessionID, "key", key)
		}
		return
	}
	s.log.Info("highlight saved", "voice_session", ss.voiceSessionID, "highlight", highlightID, "score", t.Score)
}
