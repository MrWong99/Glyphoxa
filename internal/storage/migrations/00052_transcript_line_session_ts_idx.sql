-- +goose Up

-- Anchor index for the campaign search's semantic transcript hits (#591
-- review). FirstLineIDAtOrAfter answers "the session's earliest Line at/after
-- a chunk's start" with WHERE voice_session_id = $1 AND ts >= $2 ORDER BY ts,
-- seq LIMIT 1; the existing (voice_session_id, seq) index cannot serve the ts
-- predicate/order, so each anchor lookup would fetch and sort the whole
-- session's lines. This composite lets it be a single index descent. One
-- concern per file (ADR-0031), alongside 00051's search column.
CREATE INDEX transcript_line_session_ts_idx ON transcript_line (voice_session_id, ts, seq);

-- +goose Down

DROP INDEX IF EXISTS transcript_line_session_ts_idx;
