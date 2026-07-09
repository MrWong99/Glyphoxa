package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Transcript-line persistence (#74, ADR-0040): the SSE relay UPSERTs one row per
// rendered transcript Line as it projects bus events — a single human utterance
// or a coalescing Agent reply (keyed UNIQUE (voice_session_id, line_id), updated
// in place across the reply's sentences). This LINE grain is distinct from the
// 3–6-utterance transcript_chunk retrieval/embedding grain (ADR-0011). The
// queries back the Session screen's replay-on-reload and the authoritative
// line_count summary.

// TranscriptLine is one persisted transcript Line. LineID is the relay's stable
// Line.ID; Seq is the relay's monotonic per-session ordering key (Frame.Seq).
type TranscriptLine struct {
	VoiceSessionID uuid.UUID
	CampaignID     uuid.UUID
	LineID         string
	Seq            int64
	Who            string
	Tag            string
	Kind           string
	TS             time.Time
	Text           string
	// SpeakerDiscordUserID is the Discord snowflake of the human who spoke this Line
	// (#278, ADR-0050), or "" for an unattributed utterance / an Agent reply. It
	// round-trips "" ↔ NULL: NULLIF on write, COALESCE on scan.
	SpeakerDiscordUserID string
}

// transcriptLineInsertColumns are the plain column names for INSERT; the SELECT
// list (transcriptLineColumns) mirrors them but COALESCEs speaker_discord_user_id
// to "". BOTH are ORDER-COUPLED with scanTranscriptLine and the INSERT VALUES list
// — update all together. speaker_discord_user_id is NULLIF'd to NULL on write and
// COALESCEd to "" on read ("" ↔ NULL round-trip for an unattributed utterance).
const transcriptLineInsertColumns = `
	voice_session_id, campaign_id, line_id, seq, who, tag, kind, ts, text,
	speaker_discord_user_id`

const transcriptLineColumns = `
	voice_session_id, campaign_id, line_id, seq, who, tag, kind, ts, text,
	COALESCE(speaker_discord_user_id, '')`

func scanTranscriptLine(row pgx.Row) (TranscriptLine, error) {
	var l TranscriptLine
	err := row.Scan(
		&l.VoiceSessionID, &l.CampaignID, &l.LineID, &l.Seq,
		&l.Who, &l.Tag, &l.Kind, &l.TS, &l.Text, &l.SpeakerDiscordUserID,
	)
	return l, err
}

// UpsertTranscriptLine writes (or updates in place) one transcript Line. An Agent
// reply coalesces across its sentences under one line_id, so a re-write of the
// same (voice_session_id, line_id) updates the text/ts rather than inserting a
// new row — keeping COUNT(*) == distinct lines. seq is deliberately NOT updated
// on conflict (#149): it is the replay ordering key, fixed at insert time, so
// ListTranscriptLines (ORDER BY seq) matches the live-view order even when an
// interleaved line landed between a reply's sentences.
func (s *Store) UpsertTranscriptLine(ctx context.Context, l TranscriptLine) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO transcript_line (`+transcriptLineInsertColumns+`)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, ''))
		 ON CONFLICT (voice_session_id, line_id) DO UPDATE
		    SET who = EXCLUDED.who, tag = EXCLUDED.tag,
		        kind = EXCLUDED.kind, ts = EXCLUDED.ts, text = EXCLUDED.text,
		        speaker_discord_user_id = EXCLUDED.speaker_discord_user_id,
		        updated_at = now()`,
		l.VoiceSessionID, l.CampaignID, l.LineID, l.Seq,
		l.Who, l.Tag, l.Kind, l.TS, l.Text, l.SpeakerDiscordUserID)
	if err != nil {
		return fmt.Errorf("storage: upsert transcript line %s/%s: %w", l.VoiceSessionID, l.LineID, err)
	}
	return nil
}

// ListTranscriptLines returns a Voice Session's transcript Lines ordered by seq —
// the replay-on-reload history the Session screen renders for an ended session
// (#74). An empty result is not an error.
func (s *Store) ListTranscriptLines(ctx context.Context, sessionID uuid.UUID) ([]TranscriptLine, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+transcriptLineColumns+`
		   FROM transcript_line
		  WHERE voice_session_id = $1
		  ORDER BY seq`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("storage: list transcript lines for session %s: %w", sessionID, err)
	}
	defer rows.Close()

	var out []TranscriptLine
	for rows.Next() {
		l, err := scanTranscriptLine(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan transcript line: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list transcript lines for session %s: %w", sessionID, err)
	}
	return out, nil
}

// CountTranscriptLines returns the number of persisted Lines for a Voice Session —
// the authoritative line_count the summary records on Stop (#74): rows ==
// distinct lines, so it matches the persisted history. An unknown session is 0.
func (s *Store) CountTranscriptLines(ctx context.Context, sessionID uuid.UUID) (int, error) {
	var n int
	if err := s.db.QueryRow(ctx,
		`SELECT count(*) FROM transcript_line WHERE voice_session_id = $1`, sessionID).
		Scan(&n); err != nil {
		return 0, fmt.Errorf("storage: count transcript lines for session %s: %w", sessionID, err)
	}
	return n, nil
}

// SearchTranscriptLines returns a Campaign's persisted transcript Lines whose text
// matches the query, ranked by relevance (#120, ADR-0011 amendment). This is the
// SINGLE user-facing search path — both the web SearchTranscriptLines RPC and the
// `/glyphoxa search` slash command call exactly this method (AC4: no divergent
// search logic). The match is served by the transcript_line_fts_idx GIN index
// (fts @@ q), not a substring scan, and ranked with ts_rank (a line that mentions
// the term more often ranks higher), then newest-first, then a stable tiebreak.
// The query is scoped by campaign_id, so another Campaign's transcript is NEVER
// returned (AC5). The raw query is sanitized by BuildTSQuery (the same helper the
// KG search uses), so injected tsquery operators can only ever split words; an
// empty result from that yields (nil, nil) — no matches, not an error.
func (s *Store) SearchTranscriptLines(ctx context.Context, campaignID uuid.UUID, query string, limit int) ([]TranscriptLine, error) {
	tsq := BuildTSQuery(query)
	if tsq == "" {
		return nil, nil
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+transcriptLineColumns+`
		   FROM transcript_line, to_tsquery('simple', $2) q
		  WHERE campaign_id = $1 AND fts @@ q
		  ORDER BY ts_rank(fts, q) DESC, ts DESC, voice_session_id, line_id
		  LIMIT $3`, campaignID, tsq, limit)
	if err != nil {
		return nil, fmt.Errorf("storage: search transcript lines for campaign %s: %w", campaignID, err)
	}
	defer rows.Close()

	var out []TranscriptLine
	for rows.Next() {
		l, err := scanTranscriptLine(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan transcript line search row: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: search transcript lines for campaign %s: %w", campaignID, err)
	}
	return out, nil
}
