package highlight

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/blob"
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
	ListSessionCandidateClipKeys(ctx context.Context, voiceSessionID uuid.UUID) ([]string, error)
	DeleteSessionCandidates(ctx context.Context, voiceSessionID uuid.UUID) (int, error)
}

// PurgeScheduleStore is the storage surface the boot-time purge backstop needs:
// the ended Voice Sessions that still hold candidate highlights but have no purge
// job scheduled. *storage.Store satisfies it.
type PurgeScheduleStore interface {
	ListSessionsNeedingCandidatePurge(ctx context.Context, purgeKind string) ([]uuid.UUID, error)
}

// SweepMissingCandidatePurges is the boot-time retention backstop (ADR-0051, the
// #184 "rows are the source of truth, reconcile on boot" spirit / ADR-0043): a
// crash between a session ending and Finalize scheduling its purge would strand
// that session's candidate highlights forever (their 7-day horizon never enqueued).
// At boot — before serving — this finds every ended session with candidates and NO
// pending/running/done purge job and enqueues one, 7 days out. It complements, not
// replaces, Finalize's per-session scheduling. A per-session enqueue failure logs
// and the sweep continues (the next boot retries), so one bad row never stalls the
// rest; a store-list failure is returned (a boot-time diagnostic).
func SweepMissingCandidatePurges(ctx context.Context, store PurgeScheduleStore, enqueue JobEnqueuer, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	ids, err := store.ListSessionsNeedingCandidatePurge(ctx, JobKindPurgeCandidates)
	if err != nil {
		return fmt.Errorf("highlight purge sweep: list sessions needing purge: %w", err)
	}
	for _, id := range ids {
		payload := purgePayload{VoiceSessionID: id}
		if err := enqueue.Enqueue(ctx, JobKindPurgeCandidates, payload, time.Now().Add(purgeDelay)); err != nil {
			log.Error("highlight purge sweep: enqueue backstop purge", "err", err, "voice_session", id)
			continue
		}
	}
	if len(ids) > 0 {
		log.Warn("scheduled backstop candidate purge for orphaned ended sessions", "count", len(ids))
	}
	return nil
}

// PurgeHandler builds the background-job handler for JobKindPurgeCandidates. It
// is at-least-once and idempotent (ADR-0049): it drops each remaining candidate's
// blob FIRST (through the seam, ADR-0048) and only then deletes the rows, so a
// crash between the two leaves a re-runnable job (the surviving rows still list
// their keys). Promoted highlights are untouched — the storage sweep filters to
// status='candidate'. A re-run after a clean purge lists nothing and deletes
// nothing. The returned func matches the jobs runner's HandlerFunc.
func PurgeHandler(store PurgeStore, blobs blob.Store, log *slog.Logger) func(context.Context, json.RawMessage) error {
	if log == nil {
		log = slog.Default()
	}
	return func(ctx context.Context, payload json.RawMessage) error {
		var p purgePayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("highlight purge: decode payload: %w", err)
		}

		keys, err := store.ListSessionCandidateClipKeys(ctx, p.VoiceSessionID)
		if err != nil {
			return fmt.Errorf("highlight purge: list candidate clip keys: %w", err)
		}
		// Blob first (ADR-0048): drop each clip through the seam before removing the
		// rows, so a mid-sweep crash never orphans a blob whose row is already gone.
		for _, k := range keys {
			if err := blobs.Delete(ctx, k); err != nil {
				return fmt.Errorf("highlight purge: delete clip %q: %w", k, err)
			}
		}
		n, err := store.DeleteSessionCandidates(ctx, p.VoiceSessionID)
		if err != nil {
			return fmt.Errorf("highlight purge: delete candidate rows: %w", err)
		}
		if n > 0 {
			log.Info("purged candidate highlights", "voice_session", p.VoiceSessionID, "count", n)
		}
		return nil
	}
}
