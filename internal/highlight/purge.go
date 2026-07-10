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
