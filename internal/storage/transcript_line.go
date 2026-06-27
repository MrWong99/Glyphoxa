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
}

const transcriptLineColumns = `
	voice_session_id, campaign_id, line_id, seq, who, tag, kind, ts, text`

func scanTranscriptLine(row pgx.Row) (TranscriptLine, error) {
	var l TranscriptLine
	err := row.Scan(
		&l.VoiceSessionID, &l.CampaignID, &l.LineID, &l.Seq,
		&l.Who, &l.Tag, &l.Kind, &l.TS, &l.Text,
	)
	return l, err
}

// UpsertTranscriptLine writes (or updates in place) one transcript Line. An Agent
// reply coalesces across its sentences under one line_id, so a re-write of the
// same (voice_session_id, line_id) updates the text/seq/ts rather than inserting
// a new row — keeping COUNT(*) == distinct lines.
func (s *Store) UpsertTranscriptLine(ctx context.Context, l TranscriptLine) error {
	_, err := s.db.Exec(ctx,
		`INSERT INTO transcript_line (`+transcriptLineColumns+`)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 ON CONFLICT (voice_session_id, line_id) DO UPDATE
		    SET seq = EXCLUDED.seq, who = EXCLUDED.who, tag = EXCLUDED.tag,
		        kind = EXCLUDED.kind, ts = EXCLUDED.ts, text = EXCLUDED.text,
		        updated_at = now()`,
		l.VoiceSessionID, l.CampaignID, l.LineID, l.Seq,
		l.Who, l.Tag, l.Kind, l.TS, l.Text)
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
