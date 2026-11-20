package highlight

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// JobKindPurgeCandidates is the background-job kind that drops a Voice Session's
// still-candidate highlights once their 7-day horizon passes (ADR-0051/0049).
const JobKindPurgeCandidates = "highlight.purge_candidates"

// JobKindSweepCampaignClips is the background-job kind that drops a hard-deleted
// Campaign's Highlight clip blobs (#308, ADR-0048). The highlight ROWS cascade
// with the campaign row; their clip blobs have no FK, so they are swept through
// the seam. The job is enqueued in the SAME transaction as the campaign delete, so
// it exists iff the delete committed — no orphan sweep of a surviving campaign, no
// lost sweep of a crashed process (ADR-0049 at-least-once + idempotent).
const JobKindSweepCampaignClips = "highlight.sweep_campaign_clips"

// campaignSweepPayload carries the clip keys a campaign hard-delete must drop. The
// keys are captured BEFORE the row cascade removes the highlight rows (after which
// they can no longer be listed).
type campaignSweepPayload struct {
	ClipKeys []string `json:"clip_keys"`
}

// MarshalCampaignSweep builds the JobKindSweepCampaignClips payload from the clip
// keys captured before a campaign delete. The RPC layer marshals it and hands it,
// with the kind, to the delete-in-one-tx store method.
func MarshalCampaignSweep(clipKeys []string) ([]byte, error) {
	return json.Marshal(campaignSweepPayload{ClipKeys: clipKeys})
}

// CampaignSweepHandler builds the JobKindSweepCampaignClips handler: it drops each
// carried clip key through the blob seam (ADR-0048). Idempotent + at-least-once
// (ADR-0049): blob.Delete on an absent key is a no-op (internal/blob/postgres.go),
// so a re-run after a partial sweep, or after a prior success, completes cleanly. A
// backend error on any key returns so the job retries the whole set (re-deleting an
// already-gone key is harmless).
func CampaignSweepHandler(blobs blob.Store, log *slog.Logger) func(context.Context, json.RawMessage) error {
	if log == nil {
		log = slog.Default()
	}
	return func(ctx context.Context, payload json.RawMessage) error {
		var p campaignSweepPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("highlight campaign sweep: decode payload: %w", err)
		}
		for _, k := range p.ClipKeys {
			if err := blobs.Delete(ctx, k); err != nil {
				return fmt.Errorf("highlight campaign sweep: delete clip %q: %w", k, err)
			}
		}
		if len(p.ClipKeys) > 0 {
			log.Info("swept hard-deleted campaign highlight clips", "count", len(p.ClipKeys))
		}
		return nil
	}
}

// purgeDelay is the candidate retention window (ADR-0051): a candidate highlight
// not promoted within 7 days is purged.
const purgeDelay = 7 * 24 * time.Hour

// purgePayload is the purge job's payload: which session's candidates to sweep.
// It carries no tenant — the session id scopes the sweep (the row cascade and
// blob keys derive from it), and the handler runs process-wide (ADR-0049).
type purgePayload struct {
	VoiceSessionID uuid.UUID `json:"voice_session_id"`
}

// PurgeStore is the storage surface the purge handler needs; *storage.Store
// satisfies it and tests fake it.
type PurgeStore interface {
	// TakeSessionCandidates deletes the session's candidate rows in one statement
	// and returns the blob keys they owned plus the row count.
	TakeSessionCandidates(ctx context.Context, voiceSessionID uuid.UUID) (blobKeys []string, rows int, err error)
}

// PurgeScheduleStore is the storage surface the boot-time purge backstop needs:
// the ended Voice Sessions (id + ended_at) that still hold candidate highlights but
// have no purge job scheduled. *storage.Store satisfies it.
type PurgeScheduleStore interface {
	ListSessionsNeedingCandidatePurge(ctx context.Context, purgeKind string) ([]storage.SessionPurgeCandidate, error)
}

// SweepMissingCandidatePurges is the boot-time retention backstop (ADR-0051, the
// #184 "rows are the source of truth, reconcile on boot" spirit / ADR-0043): a
// crash between a session ending and Finalize scheduling its purge would strand
// that session's candidate highlights forever (their 7-day horizon never enqueued).
// At boot — before serving — this finds every ended session with candidates and NO
// pending/running/done purge job and enqueues one. The horizon anchors at session
// END (ended_at + 7d), NOT boot time, so ADR-0051's 7-day safety purge is honored
// regardless of when the crash-restart lands: a session that ended 8 days ago is
// past-due and purges immediately (a now-floor keeps run_after from going negative),
// while one that ended yesterday keeps its remaining ~6 days. It complements, not
// replaces, Finalize's per-session scheduling. A per-session enqueue failure logs
// and the sweep continues (the next boot retries), so one bad row never stalls the
// rest; a store-list failure is returned (a boot-time diagnostic).
func SweepMissingCandidatePurges(ctx context.Context, store PurgeScheduleStore, enqueue JobEnqueuer, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	candidates, err := store.ListSessionsNeedingCandidatePurge(ctx, JobKindPurgeCandidates)
	if err != nil {
		return fmt.Errorf("highlight purge sweep: list sessions needing purge: %w", err)
	}
	now := time.Now()
	for _, c := range candidates {
		// Anchor at session end: ended_at + 7d, floored at now so a long-past session
		// purges immediately rather than 7 days after this boot (ADR-0051).
		runAfter := c.EndedAt.Add(purgeDelay)
		if runAfter.Before(now) {
			runAfter = now
		}
		payload := purgePayload{VoiceSessionID: c.VoiceSessionID}
		if err := enqueue.Enqueue(ctx, JobKindPurgeCandidates, payload, runAfter); err != nil {
			log.Error("highlight purge sweep: enqueue backstop purge", "err", err, "voice_session", c.VoiceSessionID)
			continue
		}
	}
	if len(candidates) > 0 {
		log.Warn("scheduled backstop candidate purge for orphaned ended sessions", "count", len(candidates))
	}
	return nil
}

// PurgeHandler builds the background-job handler for JobKindPurgeCandidates. It
// is at-least-once and idempotent (ADR-0049). It removes the remaining candidate
// ROWS first, in one atomic statement that returns the blob keys they owned, and
// only then drops those blobs through the seam (ADR-0048).
//
// Rows first is deliberate (see storage.TakeSessionCandidates): rows and blobs
// cannot share a transaction, and of the two ways the second step can be lost,
// an orphan BLOB is the recoverable one. A blob that fails to delete here, or
// that a crash strands between the two steps, has no row any more, and the boot
// reconciliation sweep ([SweepEnrichmentReconciliation] half (b)) deletes every
// Highlight-owned clip, image and sound blob without a row. So a blob error is
// logged and the purge carries on: failing the job would retry against rows that
// are already gone and find nothing to do. The reverse order left rows pointing
// at deleted clips, and let a GM's promote keep a Highlight whose clip the purge
// had just dropped.
//
// Promoted highlights are untouched (the delete filters to status='candidate').
// The returned func matches the jobs runner's HandlerFunc.
func PurgeHandler(store PurgeStore, blobs blob.Store, log *slog.Logger) func(context.Context, json.RawMessage) error {
	if log == nil {
		log = slog.Default()
	}
	return func(ctx context.Context, payload json.RawMessage) error {
		var p purgePayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("highlight purge: decode payload: %w", err)
		}

		keys, n, err := store.TakeSessionCandidates(ctx, p.VoiceSessionID)
		if err != nil {
			return fmt.Errorf("highlight purge: delete candidate rows: %w", err)
		}
		// The rows are gone for good; the blob drops must not be cut short by the
		// job's own cancellation (shutdown), or every remaining clip waits for the
		// next boot's sweep.
		dropCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), purgeBlobDropTimeout)
		defer cancel()
		stranded := 0
		for _, k := range keys {
			if err := blobs.Delete(dropCtx, k); err != nil {
				stranded++
				log.Error("highlight purge: delete blob failed; its row is gone, the boot orphan sweep will reclaim it",
					"err", err, "key", k, "voice_session", p.VoiceSessionID)
			}
		}
		if n > 0 {
			log.Info("purged candidate highlights", "voice_session", p.VoiceSessionID, "count", n, "blobs_left_for_sweep", stranded)
		}
		return nil
	}
}

// purgeBlobDropTimeout bounds the blob drops that follow a purge's row delete.
const purgeBlobDropTimeout = 30 * time.Second

// Compile-time pins: *storage.Store satisfies every narrow store seam this
// package declares, so a drift in a store method fails THIS package's build
// instead of surfacing only at the composition root (CONTRIBUTING: interface
// assertions).
var (
	_ PurgeStore         = (*storage.Store)(nil)
	_ PurgeScheduleStore = (*storage.Store)(nil)
)
