-- +goose Up

-- transcript_line backs the Session screen's transcript replay-on-reload (#74,
-- ADR-0040): one row per rendered transcript Line — a single human utterance or
-- a coalesced Agent reply — DISTINCT from the 3–6-utterance transcript_chunk
-- retrieval/embedding grain (ADR-0011). The SSE relay (internal/transcript)
-- UPSERTs a row as it projects each bus event into a Line; an Agent reply
-- coalesces across its sentences under one stable line_id, so the write is keyed
-- UNIQUE (voice_session_id, line_id) and updated in place. A Voice Session's
-- line_count (00006 comment) is COUNT(*) of these rows, so rows == distinct
-- lines and the summary count matches the persisted history.
CREATE TABLE transcript_line (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    voice_session_id  uuid NOT NULL REFERENCES voice_sessions (id) ON DELETE CASCADE,
    campaign_id       uuid NOT NULL REFERENCES campaign (id) ON DELETE CASCADE,
    -- The relay's stable Line.ID: "u:<n>" for a human utterance, "a:<turn>" for a
    -- coalescing Agent reply. Unique per session so re-emits UPSERT in place.
    line_id           text NOT NULL,
    -- Frame.Seq: the relay's monotonic per-session sequence, the ordering key for
    -- ordered replay (ORDER BY seq).
    seq               bigint NOT NULL,
    -- Rendered fields mirroring the relay's Line: speaker label, optional pill
    -- tag, kind ∈ {gm,player,npc,butler} (ADR-0039), instant, and text.
    who               text NOT NULL,
    tag               text NOT NULL DEFAULT '',
    kind              text NOT NULL,
    ts                timestamptz NOT NULL,
    text              text NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (voice_session_id, line_id)
);

-- Ordered replay of a session's lines (ServeSnapshot for an ended session).
CREATE INDEX transcript_line_session_seq_idx ON transcript_line (voice_session_id, seq);

-- +goose Down

DROP TABLE IF EXISTS transcript_line;
