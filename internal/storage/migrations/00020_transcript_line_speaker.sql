-- +goose Up

-- Persist speaker identity on transcript Lines (#278, E4, ADR-0050). The Speaker
-- Lanes (#277) attribute each human STTFinal to a Discord User; ADR-0040's line
-- grain records who said each Line so replay/search can attribute it (#281).
-- NULLABLE: an unattributed utterance (empty SpeakerID) persists NULL, so the
-- pre-#278 anonymous behavior is byte-identical. NO index — no query filters on
-- it yet (the column is written now, consumed by #281 later).
ALTER TABLE transcript_line ADD COLUMN speaker_discord_user_id text;

-- +goose Down

ALTER TABLE transcript_line DROP COLUMN speaker_discord_user_id;
