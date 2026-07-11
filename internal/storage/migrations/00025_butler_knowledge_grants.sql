-- +goose Up

-- The auto-Butler joins the live Voice Session (#299, #297 decision 1): its
-- default Tool Grant set grows from `dice` alone to the read-only knowledge Tools
-- that make it a useful in-voice assistant — `transcript_search` and `kg_query`
-- (both landed in #296). They are seeded the same way the dice grant is: as a
-- Campaign-existence invariant in the auto-Butler trigger, so no application call
-- site can forget to grant them. Each carries a NULL config — `kg_query`'s
-- nil-config default is campaign scope (already gm_private-filtered, ADR-0008),
-- and `transcript_search` takes no scope (ADR-0029 / S3).
--
-- NOT included: `recap`. #297 decision 5 wants a read-only recap Tool wrapper, but
-- that Tool does not exist yet (only the recap service does) — granting a
-- non-existent Tool would declare nothing to the LLM at best and confuse the grant
-- editor at worst. It joins the default set when the Tool wrapper lands
-- (follow-up).
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION create_campaign_butler() RETURNS trigger AS $$
DECLARE
    butler_id uuid;
BEGIN
    INSERT INTO agents (campaign_id, agent_role, name, address_only)
    VALUES (NEW.id, 'butler', 'Glyphoxa', true)
    RETURNING id INTO butler_id;

    INSERT INTO tool_agent_grant (agent_id, tool_name)
    VALUES (butler_id, 'dice'),
           (butler_id, 'transcript_search'),
           (butler_id, 'kg_query');

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- Backfill: Butlers created before this migration (e.g. the demo seed's) get the
-- two knowledge grants they are now meant to have. Idempotent via the
-- UNIQUE(agent_id, tool_name) key, so re-running is a no-op and a Butler already
-- holding one keeps it.
INSERT INTO tool_agent_grant (agent_id, tool_name)
SELECT id, 'transcript_search' FROM agents WHERE agent_role = 'butler'
ON CONFLICT (agent_id, tool_name) DO NOTHING;

INSERT INTO tool_agent_grant (agent_id, tool_name)
SELECT id, 'kg_query' FROM agents WHERE agent_role = 'butler'
ON CONFLICT (agent_id, tool_name) DO NOTHING;

-- +goose Down

-- Restore the 00013 trigger body (dice grant only), so a future auto-created
-- Butler gets exactly the pre-#299 default set again.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION create_campaign_butler() RETURNS trigger AS $$
DECLARE
    butler_id uuid;
BEGIN
    INSERT INTO agents (campaign_id, agent_role, name, address_only)
    VALUES (NEW.id, 'butler', 'Glyphoxa', true)
    RETURNING id INTO butler_id;

    INSERT INTO tool_agent_grant (agent_id, tool_name)
    VALUES (butler_id, 'dice');

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- Remove the backfilled knowledge grants from existing Butlers, mirroring the Up
-- backfill so the down migration is a clean inverse.
DELETE FROM tool_agent_grant
WHERE tool_name IN ('transcript_search', 'kg_query')
  AND agent_id IN (SELECT id FROM agents WHERE agent_role = 'butler');
