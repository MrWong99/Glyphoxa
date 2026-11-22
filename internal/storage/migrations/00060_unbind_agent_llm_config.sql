-- +goose Up
-- A Tenant now chooses its voice LLM between provider slots (#702): its newest
-- 'llm' Provider Config is the LLM in effect, and that is what the
-- Configuration screen writes, shows and health-checks. An Agent bound to one
-- specific LLM row outranks that choice for the whole Voice Session (the
-- primary Agent's bound row picks the adapter), so on such a campaign the
-- switch would be a silent no-op while the screen named the other provider.
--
-- Only the demo seed (`glyphoxa seed`) ever wrote this binding — no RPC or
-- screen sets it, campaign edits merely preserve it, bundle import nulls it —
-- and it always pointed at the Tenant's one LLM row, i.e. the row the Tenant
-- fallback resolves to anyway. Clearing it changes nothing for existing data
-- and lets seeded campaigns follow the Tenant's choice like every web-created
-- one. The provider_config rows, the voice (TTS) binding and the Butler's
-- chat-scoped slot (chat_llm_provider_config_id, ADR-0062) are untouched.
UPDATE agents
   SET llm_provider_config_id = NULL
 WHERE llm_provider_config_id IS NOT NULL;

-- +goose Down
-- Irreversible by design: which Agents were bound is not recorded, and
-- re-binding would only restore the silent no-op described above.
SELECT 1;
