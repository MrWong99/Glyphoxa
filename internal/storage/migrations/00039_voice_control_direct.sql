-- +goose Up

-- GM directive relay (ADR-0059): the 'direct' control verb rides the existing
-- voice_session_controls queue (kind='direct', agent_id + say_text carrying the
-- directive text — '' clears). direct_turns bounds how many committed Agent
-- turns the directive applies to; 0 means sticky until cleared, replaced, or
-- session end. Text-vocabulary column additions only — the kind column is
-- app-validated text (00038), so no enum change is needed.
ALTER TABLE voice_session_controls
    ADD COLUMN direct_turns integer NOT NULL DEFAULT 0;

-- +goose Down

ALTER TABLE voice_session_controls
    DROP COLUMN IF EXISTS direct_turns;
