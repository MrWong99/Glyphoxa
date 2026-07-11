package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Session Highlights persistence (#308, Epic 8, ADR-0051): one highlight row per
// detected epic moment. A row is born 'candidate' with a 7-day purge horizon;
// only an explicit GM promotion flips it to 'promoted' (kept indefinitely). The
// audio clip lives behind the blob seam (ADR-0048): clip_key reconstructs the
// blob.Key and deletion goes through blob.Delete, never a DB cascade to the blob.
//
// Read/promote/delete are tenant-scoped (single-operator today, ADR-0039, but the
// WHERE tenant_id guard is the seam the multi-tenant future keys off). The two
// sweep helpers — DeleteSessionCandidates (the 7-day purge job) and
// ListCampaignHighlightClipKeys (the campaign hard-delete blob sweep) — scope by
// session/campaign instead: their callers carry no tenant, only the owning id.

// Highlight status values (CHECK-constrained in the schema).
const (
	// HighlightCandidate: freshly detected, subject to the 7-day purge.
	HighlightCandidate = "candidate"
	// HighlightPromoted: an explicit GM keep; never purged.
	HighlightPromoted = "promoted"
)

// Highlight is one persisted Session Highlight row (#308).
type Highlight struct {
	ID              uuid.UUID
	TenantID        uuid.UUID
	VoiceSessionID  uuid.UUID
	CampaignID      uuid.UUID
	Status          string
	StartsAt        time.Time
	EndsAt          time.Time
	Score           float64
	Excerpt         string
	Reason          string
	SpeakerIDs      []string
	ClipKey         string
	ClipContentType string
	ClipSizeBytes   int64
	CreatedAt       time.Time
	PromotedAt      *time.Time
}

const highlightColumns = `
	id, tenant_id, voice_session_id, campaign_id, status,
	starts_at, ends_at, score, excerpt, reason, speaker_ids,
	clip_key, clip_content_type, clip_size_bytes, created_at, promoted_at`

func scanHighlight(row pgx.Row) (Highlight, error) {
	var (
		h  Highlight
		pr *time.Time
	)
	err := row.Scan(
		&h.ID, &h.TenantID, &h.VoiceSessionID, &h.CampaignID, &h.Status,
		&h.StartsAt, &h.EndsAt, &h.Score, &h.Excerpt, &h.Reason, &h.SpeakerIDs,
		&h.ClipKey, &h.ClipContentType, &h.ClipSizeBytes, &h.CreatedAt, &pr,
	)
	h.PromotedAt = pr
	return h, err
}

// CreateHighlight inserts one detected highlight as a 'candidate' (the Saver's
// worker calls it after the clip is stored, #308). id/status/clip metadata are
// caller-supplied so the row and its blob.Key agree; empty SpeakerIDs are stored
// as the empty array (never NULL). A blank Status defaults to 'candidate'.
func (s *Store) CreateHighlight(ctx context.Context, h Highlight) error {
	if h.ID == uuid.Nil {
		h.ID = uuid.New()
	}
	if h.Status == "" {
		h.Status = HighlightCandidate
	}
	if h.ClipContentType == "" {
		h.ClipContentType = "audio/wav"
	}
	speakers := h.SpeakerIDs
	if speakers == nil {
		speakers = []string{}
	}
	_, err := s.db.Exec(ctx,
		`INSERT INTO highlight
		   (id, tenant_id, voice_session_id, campaign_id, status,
		    starts_at, ends_at, score, excerpt, reason, speaker_ids,
		    clip_key, clip_content_type, clip_size_bytes)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		h.ID, h.TenantID, h.VoiceSessionID, h.CampaignID, h.Status,
		h.StartsAt, h.EndsAt, h.Score, h.Excerpt, h.Reason, speakers,
		h.ClipKey, h.ClipContentType, h.ClipSizeBytes)
	if err != nil {
		return fmt.Errorf("storage: create highlight: %w", err)
	}
	return nil
}

// GetHighlight loads one highlight by id within the tenant, or ErrNotFound. The
// tenant guard means a foreign-tenant id reads as absent (never leaked).
func (s *Store) GetHighlight(ctx context.Context, tenantID, id uuid.UUID) (Highlight, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+highlightColumns+` FROM highlight WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	h, err := scanHighlight(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Highlight{}, ErrNotFound
	}
	if err != nil {
		return Highlight{}, fmt.Errorf("storage: get highlight %s: %w", id, err)
	}
	return h, nil
}

// ListHighlights returns a Voice Session's highlights within the tenant, newest
// moment first (starts_at DESC, id DESC for a stable tie-break). It backs the GM
// session-end review UI (#309). An empty result is not an error.
func (s *Store) ListHighlights(ctx context.Context, tenantID, voiceSessionID uuid.UUID) ([]Highlight, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+highlightColumns+`
		   FROM highlight
		  WHERE tenant_id = $1 AND voice_session_id = $2
		  ORDER BY starts_at DESC, id DESC`, tenantID, voiceSessionID)
	if err != nil {
		return nil, fmt.Errorf("storage: list highlights: %w", err)
	}
	defer rows.Close()

	var out []Highlight
	for rows.Next() {
		h, err := scanHighlight(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan highlight: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list highlights: %w", err)
	}
	return out, nil
}

// PromoteHighlight flips a candidate to 'promoted' and stamps promoted_at within
// the tenant, returning the updated row (#309 GM keep). It is idempotent: a
// re-promote keeps the ORIGINAL promoted_at (COALESCE) so the audit trail of WHEN
// the GM first kept it survives. A missing id (or foreign tenant) yields
// ErrNotFound. It deliberately does NOT enqueue enrichment — that is #311's hook.
func (s *Store) PromoteHighlight(ctx context.Context, tenantID, id uuid.UUID) (Highlight, error) {
	row := s.db.QueryRow(ctx,
		`UPDATE highlight
		    SET status = 'promoted', promoted_at = COALESCE(promoted_at, now())
		  WHERE id = $1 AND tenant_id = $2
		 RETURNING `+highlightColumns, id, tenantID)
	h, err := scanHighlight(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Highlight{}, ErrNotFound
	}
	if err != nil {
		return Highlight{}, fmt.Errorf("storage: promote highlight %s: %w", id, err)
	}
	return h, nil
}

// DeleteHighlight removes one highlight within the tenant and returns its
// clip_key so the caller can drop the blob through the seam (ADR-0048). A missing
// id (or foreign tenant) yields ErrNotFound. The blob delete is the caller's
// responsibility and runs BEFORE this in the RPC (blob-then-row), but the key is
// returned so a delete driven off a prior read still has it.
func (s *Store) DeleteHighlight(ctx context.Context, tenantID, id uuid.UUID) (string, error) {
	var clipKey string
	err := s.db.QueryRow(ctx,
		`DELETE FROM highlight WHERE id = $1 AND tenant_id = $2 RETURNING clip_key`, id, tenantID).
		Scan(&clipKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("storage: delete highlight %s: %w", id, err)
	}
	return clipKey, nil
}

// ListSessionCandidateClipKeys returns the clip_key of every remaining CANDIDATE
// highlight for a Voice Session — the blob keys the 7-day purge job drops through
// the seam BEFORE it deletes the rows (blob-first, ADR-0048). Promoted rows are
// excluded (they are kept). Session-scoped (the purge payload carries no tenant).
func (s *Store) ListSessionCandidateClipKeys(ctx context.Context, voiceSessionID uuid.UUID) ([]string, error) {
	return s.scanClipKeys(ctx,
		`SELECT clip_key FROM highlight WHERE voice_session_id = $1 AND status = 'candidate'`,
		voiceSessionID)
}

// DeleteSessionCandidates removes every remaining CANDIDATE highlight row for a
// Voice Session (the 7-day purge, #308/ADR-0051), returning how many rows were
// deleted. Promoted rows are untouched. Idempotent: a second run deletes nothing.
// The caller drops the blobs first via ListSessionCandidateClipKeys.
func (s *Store) DeleteSessionCandidates(ctx context.Context, voiceSessionID uuid.UUID) (int, error) {
	tag, err := s.db.Exec(ctx,
		`DELETE FROM highlight WHERE voice_session_id = $1 AND status = 'candidate'`, voiceSessionID)
	if err != nil {
		return 0, fmt.Errorf("storage: delete session candidates: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// SessionPurgeCandidate is an ended Voice Session the boot backstop must schedule a
// candidate purge for: its id plus the ended_at the 7-day horizon anchors to
// (ADR-0051), so a session that ended long ago is purged immediately rather than
// 7 days after boot.
type SessionPurgeCandidate struct {
	VoiceSessionID uuid.UUID
	EndedAt        time.Time
}

// ListSessionsNeedingCandidatePurge returns the ENDED Voice Sessions (id + ended_at)
// that still hold at least one 'candidate' highlight but have NO purge job of the
// given kind in a live state (pending/running/done) — the sessions whose 7-day purge
// was never scheduled because a crash landed between the session ending and the
// Saver's Finalize enqueue (#308, ADR-0051). It is the input to the boot-time
// backstop sweep. ended_at is returned so the sweep anchors the horizon at session
// END (ended_at+7d), not boot time. A job is matched on its payload's
// voice_session_id (the purge payload's only field); 'dead' jobs are treated as
// absent so a permanently-failed purge is re-scheduled. Session-scoped, carries no
// tenant (the sweep is process-wide, ADR-0049).
func (s *Store) ListSessionsNeedingCandidatePurge(ctx context.Context, purgeKind string) ([]SessionPurgeCandidate, error) {
	rows, err := s.db.Query(ctx,
		`SELECT DISTINCT h.voice_session_id, vs.ended_at
		   FROM highlight h
		   JOIN voice_sessions vs ON vs.id = h.voice_session_id
		  WHERE h.status = 'candidate'
		    AND vs.ended_at IS NOT NULL
		    AND NOT EXISTS (
		          SELECT 1 FROM job j
		           WHERE j.kind = $1
		             AND j.status IN ('pending','running','done')
		             AND j.payload->>'voice_session_id' = h.voice_session_id::text
		        )`, purgeKind)
	if err != nil {
		return nil, fmt.Errorf("storage: list sessions needing candidate purge: %w", err)
	}
	defer rows.Close()

	var out []SessionPurgeCandidate
	for rows.Next() {
		var c SessionPurgeCandidate
		if err := rows.Scan(&c.VoiceSessionID, &c.EndedAt); err != nil {
			return nil, fmt.Errorf("storage: scan session needing purge: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list sessions needing candidate purge: %w", err)
	}
	return out, nil
}

// ListCampaignHighlightClipKeys returns the clip_key of EVERY highlight
// (candidate and promoted) in a Campaign — the blob keys a campaign hard-delete
// sweeps through the seam BEFORE the row cascade removes them (ADR-0048). No
// status filter: a campaign delete takes its highlights with it, kept or not.
func (s *Store) ListCampaignHighlightClipKeys(ctx context.Context, campaignID uuid.UUID) ([]string, error) {
	return s.scanClipKeys(ctx,
		`SELECT clip_key FROM highlight WHERE campaign_id = $1`, campaignID)
}

// scanClipKeys runs a single-column clip_key query and collects the results.
func (s *Store) scanClipKeys(ctx context.Context, sql string, arg uuid.UUID) ([]string, error) {
	rows, err := s.db.Query(ctx, sql, arg)
	if err != nil {
		return nil, fmt.Errorf("storage: list highlight clip keys: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("storage: scan highlight clip key: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list highlight clip keys: %w", err)
	}
	return out, nil
}
