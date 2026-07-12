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
	// ImageKey / ImageContentType / ImageSizeBytes carry the AI-generated scene
	// (#311, Epic 8, ADR-0004 amendment): the enrichment job stores an image
	// behind the blob seam (ADR-0048) and lands it here. ImageKey == "" means no
	// image yet (unenriched, unconfigured, or a failed generation — the row stays
	// intact without media). ImageKey is NEVER exposed on the wire (clip_key
	// posture): the image is served through GET /highlights/{id}/image.
	ImageKey         string
	ImageContentType string
	ImageSizeBytes   int64
	CreatedAt        time.Time
	PromotedAt       *time.Time
}

const highlightColumns = `
	id, tenant_id, voice_session_id, campaign_id, status,
	starts_at, ends_at, score, excerpt, reason, speaker_ids,
	clip_key, clip_content_type, clip_size_bytes,
	image_key, image_content_type, image_size_bytes, created_at, promoted_at`

func scanHighlight(row pgx.Row) (Highlight, error) {
	var (
		h  Highlight
		pr *time.Time
	)
	err := row.Scan(
		&h.ID, &h.TenantID, &h.VoiceSessionID, &h.CampaignID, &h.Status,
		&h.StartsAt, &h.EndsAt, &h.Score, &h.Excerpt, &h.Reason, &h.SpeakerIDs,
		&h.ClipKey, &h.ClipContentType, &h.ClipSizeBytes,
		&h.ImageKey, &h.ImageContentType, &h.ImageSizeBytes, &h.CreatedAt, &pr,
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

// SetHighlightImage lands an AI-generated image on a Highlight (#311): the
// enrichment job (ADR-0049) stores the image behind the blob seam (ADR-0048) and
// then records its key, MIME type, and size here. It is tenant-free (the handler
// carries no tenant — the id scopes the row, and image_key derives from the
// tenant baked into the blob key) and returns ErrNotFound if the row is gone (a
// Highlight deleted between the job's GetHighlight and this write — the handler
// compensates by deleting the just-stored blob). Idempotent at the row level: a
// re-run overwrites the same deterministic key with the same fields.
func (s *Store) SetHighlightImage(ctx context.Context, id uuid.UUID, imageKey, contentType string, sizeBytes int64) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE highlight
		    SET image_key = $2, image_content_type = $3, image_size_bytes = $4
		  WHERE id = $1`,
		id, imageKey, contentType, sizeBytes)
	if err != nil {
		return fmt.Errorf("storage: set highlight image %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
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

// ListCampaignHighlightClipKeys returns EVERY blob key a campaign hard-delete
// must sweep through the seam BEFORE the row cascade removes the highlights
// (ADR-0048): each highlight's clip_key AND its image_key when non-empty (#311 —
// a promoted, enriched Highlight owns two blobs). No status filter: a campaign
// delete takes its highlights with it, kept or not. clip_key is always present;
// image_key is UNION ALL'd only where set, so unenriched rows contribute one key.
func (s *Store) ListCampaignHighlightClipKeys(ctx context.Context, campaignID uuid.UUID) ([]string, error) {
	return s.scanClipKeys(ctx,
		`SELECT clip_key FROM highlight WHERE campaign_id = $1
		 UNION ALL
		 SELECT image_key FROM highlight WHERE campaign_id = $1 AND image_key <> ''`, campaignID)
}

// HighlightEnrichTarget is a promoted, still-imageless Highlight the boot
// reconciliation sweep must (re)enqueue image enrichment for (#406): the id plus
// the tenant that owns it, exactly the enrich job payload's two fields (the sweep
// carries no ambient tenant — it is process-wide, ADR-0049).
type HighlightEnrichTarget struct {
	HighlightID uuid.UUID
	TenantID    uuid.UUID
}

// ListPromotedHighlightsNeedingEnrichment returns every PROMOTED Highlight with an
// empty image_key and NO enrich job of the given kind in a live state
// (pending/running/done) — the promoted Highlights whose image enrichment was
// never enqueued (a crash between promote-commit and the enqueue) or whose only
// enqueue was lost (#406). It is the (a) half of the boot reconciliation sweep,
// mirroring ListSessionsNeedingCandidatePurge. A 'done' job counts as satisfied so
// an unconfigured/failed-permanent enrichment (the handler returns nil and leaves
// the row imageless by design) is NOT re-swept every boot; 'dead' is treated as
// absent so a genuinely dead-lettered enrichment is re-scheduled. A job is matched
// on its payload's highlight_id. Process-wide, carries no tenant.
func (s *Store) ListPromotedHighlightsNeedingEnrichment(ctx context.Context, enrichKind string) ([]HighlightEnrichTarget, error) {
	rows, err := s.db.Query(ctx,
		`SELECT h.id, h.tenant_id
		   FROM highlight h
		  WHERE h.status = 'promoted'
		    AND h.image_key = ''
		    AND NOT EXISTS (
		          SELECT 1 FROM job j
		           WHERE j.kind = $1
		             AND j.status IN ('pending','running','done')
		             AND j.payload->>'highlight_id' = h.id::text
		        )`, enrichKind)
	if err != nil {
		return nil, fmt.Errorf("storage: list promoted highlights needing enrichment: %w", err)
	}
	defer rows.Close()

	var out []HighlightEnrichTarget
	for rows.Next() {
		var t HighlightEnrichTarget
		if err := rows.Scan(&t.HighlightID, &t.TenantID); err != nil {
			return nil, fmt.Errorf("storage: scan enrich target: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list promoted highlights needing enrichment: %w", err)
	}
	return out, nil
}

// TryClaimHighlightEnrich atomically claims the image enrichment of a Highlight
// (#406): a conditional UPDATE that stamps image_enrich_claimed_at iff the row is
// still imageless AND no claim newer than ttl is held. It reports whether THIS
// caller won (RowsAffected == 1). A false-no-error means a live worker holds the
// claim or the row was enriched meanwhile. The lease (ttl) makes a crashed
// claimant's claim reclaimable, so a Highlight is never stranded imageless. The
// column is never scanned onto the wire, so the marker cannot leak into an RPC
// response. Tenant-free (the id scopes the row, like SetHighlightImage).
func (s *Store) TryClaimHighlightEnrich(ctx context.Context, id uuid.UUID, ttl time.Duration) (bool, error) {
	// Single-clock lease (#421): the stamp AND the expiry cutoff are both DB now(),
	// so an app-clock skewed AHEAD of the DB can no longer shorten the effective TTL
	// and reclaim a still-LIVE winner mid-generation (a double Generate). The app
	// contributes only the TTL magnitude, never a wall-clock reading. make_interval's
	// secs is double precision, so a sub-second TTL survives.
	tag, err := s.db.Exec(ctx,
		`UPDATE highlight
		    SET image_enrich_claimed_at = now()
		  WHERE id = $1
		    AND image_key = ''
		    AND (image_enrich_claimed_at IS NULL
		         OR image_enrich_claimed_at < now() - make_interval(secs => $2))`,
		id, ttl.Seconds())
	if err != nil {
		return false, fmt.Errorf("storage: claim highlight enrich %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// ReleaseHighlightEnrichClaim clears a Highlight's enrichment claim (#406) so a
// retry (or a later re-promotion) can re-claim without waiting out the ttl. It is
// idempotent — clearing an already-null claim is a no-op — and tenant-free.
func (s *Store) ReleaseHighlightEnrichClaim(ctx context.Context, id uuid.UUID) error {
	if _, err := s.db.Exec(ctx,
		`UPDATE highlight SET image_enrich_claimed_at = NULL WHERE id = $1`, id); err != nil {
		return fmt.Errorf("storage: release highlight enrich claim %s: %w", id, err)
	}
	return nil
}

// HighlightsExist reports which of the given Highlight ids still have a row,
// returning a set (present ids map to true; absent ids are simply not in the map).
// It is the membership half of the boot orphan-image sweep's anti-join (#421): the
// sweep enumerates image blobs THROUGH the blob seam (blob.Store.List), extracts
// each blob's Highlight id, then asks this which of them still exist — the blobs
// whose id is absent are the delete-vs-enrich orphans. Keeping the anti-join in Go
// (not a SELECT against the blob table) is what keeps the orphan sweep working
// across a blob-backend swap (ADR-0048). An empty input runs no query. Process-wide,
// carries no tenant (the ids are globally unique PKs).
func (s *Store) HighlightsExist(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	present := make(map[uuid.UUID]bool, len(ids))
	if len(ids) == 0 {
		return present, nil
	}
	rows, err := s.db.Query(ctx,
		`SELECT id FROM highlight WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("storage: highlights exist: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("storage: scan highlight id: %w", err)
		}
		present[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: highlights exist: %w", err)
	}
	return present, nil
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
