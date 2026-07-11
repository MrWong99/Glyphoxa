-- +goose Up

-- The recap Tool wrapper now exists (#372, #297 decision 5): a read-only Tool the
-- Butler drives to summarize the Campaign's most recent ended Voice Session(s) in
-- voice. It joins the auto-Butler's default Grant set — the follow-up 00025 flagged
-- when it deliberately left `recap` out (the Tool did not exist yet). Seeded the
-- same way the other defaults are: as a Campaign-existence invariant in the
-- auto-Butler trigger, so no application call site can forget it. It carries a NULL
-- config — recap takes no scope (campaign is implicit via the active session,
-- ADR-0029).
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
           (butler_id, 'kg_query'),
           (butler_id, 'recap');

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- Backfill: Butlers created before this migration get the recap grant they are now
-- meant to have. Idempotent via the UNIQUE(agent_id, tool_name) key, so re-running
-- is a no-op and a Butler already holding it keeps it.
INSERT INTO tool_agent_grant (agent_id, tool_name)
SELECT id, 'recap' FROM agents WHERE agent_role = 'butler'
ON CONFLICT (agent_id, tool_name) DO NOTHING;

-- +goose Down

-- Restore the 00025 trigger body (dice + the two knowledge grants, no recap), so a
-- future auto-created Butler gets exactly the pre-#372 default set again.
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

-- Remove the backfilled recap grant from existing Butlers, mirroring the Up
-- backfill so the down migration is a clean inverse.
DELETE FROM tool_agent_grant
WHERE tool_name = 'recap'
  AND agent_id IN (SELECT id FROM agents WHERE agent_role = 'butler');
