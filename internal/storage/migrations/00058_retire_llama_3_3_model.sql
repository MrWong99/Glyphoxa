-- +goose Up
-- Groq retired the llama-3.3-70b-versatile model id: every request naming it now
-- fails with HTTP 404, so a campaign whose saved LLM Provider Config still pins
-- it can no longer speak. Clear the pin rather than guessing a successor: an
-- empty model makes the adapter use its own default (llmbuild passes cfg.Model
-- through; groq.DefaultModel is openai/gpt-oss-120b, ADR-0036 amendment
-- 2026-07-13), which keeps tracking future default changes. The GM can pick a
-- model again in the Configuration screen.
UPDATE provider_config
   SET model = '', updated_at = now()
 WHERE model = 'llama-3.3-70b-versatile';

-- +goose Down
-- Irreversible by design: which rows held the retired id is not recorded, and
-- restoring a model id that 404s would only re-break those campaigns.
SELECT 1;
