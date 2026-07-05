-- +goose Up

-- Per-Agent Tool Grants (ADR-0029): an explicit permission for one Agent to
-- invoke one named Tool, with an optional per-grant scope/config. This is the DB
-- home of the in-memory Grant{ToolName, Config?} the live loop already consumes
-- (#113): grants hydrate from these rows into the identical GrantSet shape, so
-- tool availability becomes data-driven instead of a compiled-in constant.
--
-- config is untyped jsonb (ADR-0029: the grant Config is `any`) — nil for a
-- grant that carries no narrowing (dice), a scope blob for a future Tool granted
-- differently per Agent (remember_knowledge "only about yourself" vs
-- campaign-wide). The scope reaches the Tool handler at execution time and is
-- enforced THERE, never by the LLM.
--
-- UNIQUE(agent_id, tool_name): an Agent grants a Tool at most once. ON DELETE
-- CASCADE ties a grant to its Agent's lifetime — deleting an Agent drops its
-- grants with no explicit cleanup code (mirrors the agents FK chain in 00001).

CREATE TABLE tool_agent_grant (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id    uuid NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    tool_name   text NOT NULL,
    config      jsonb,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (agent_id, tool_name)
);

CREATE INDEX tool_agent_grant_agent_idx ON tool_agent_grant (agent_id);

-- The auto-Butler's default grant is `dice` only (ADR-0009 Q14). 00002 deferred
-- seeding it to "when that table and the grant-seeding path land" — that is now.
-- Extend the trigger function (CREATE OR REPLACE) so every future auto-created
-- Butler also gets its dice grant in the same DB invariant that creates it, so
-- no application call site can forget to. The grant is a Campaign-existence
-- invariant exactly as the Butler row is.
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

-- Backfill: existing Butlers (created before this migration) get the dice grant
-- they were always meant to have. Idempotent via the UNIQUE key.
INSERT INTO tool_agent_grant (agent_id, tool_name)
SELECT id, 'dice' FROM agents WHERE agent_role = 'butler'
ON CONFLICT (agent_id, tool_name) DO NOTHING;

-- +goose Down

-- Restore the 00002 trigger function (no grant insert) BEFORE dropping the
-- table it references, so the function never dangles against a missing table.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION create_campaign_butler() RETURNS trigger AS $$
BEGIN
    INSERT INTO agents (campaign_id, agent_role, name, address_only)
    VALUES (NEW.id, 'butler', 'Glyphoxa', true);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TABLE IF EXISTS tool_agent_grant;
