-- +goose Up

-- Butler planning chat (#592, ADR-0062): the Campaign's Butler reachable as a
-- GM-only chat from the Campaign screen. This migration is the feature's whole
-- schema footprint:
--
--   1. planning_thread / planning_message — persisted planning threads
--      (ADR-0062 decides persist-and-own-the-obligations, not the ephemeral
--      Recap posture). Campaign-scoped child data following the 00047 pattern:
--      UNIQUE (id, campaign_id) on the parent so the child's composite FK makes
--      cross-campaign messages structurally impossible, and ON DELETE CASCADE
--      so the campaign deletion sweep needs zero code (00043's discipline).
--   2. agents.chat_llm_provider_config_id — the second, chat-scoped LLM slot
--      (ADR-0062: a GM upgrading chat quality must not re-price the live voice
--      loop). Nullable, ON DELETE SET NULL like its two siblings in 00001.
--   3. provider_component 'chat_llm' — the tenant-level Configuration slot the
--      chat resolution ladder reads between the Butler's own slot and the
--      voice 'llm' config. Same ALTER TYPE pattern as 00028's 'image': safe in
--      a transaction because nothing in THIS migration stores the new value.
--   4. tool_agent_grant.surface — ADR-0062 requires chat Tool Grants as
--      SEPARATE rows from voice grants (ADR-0029), and the 00013 key was
--      (agent_id, tool_name). 'voice' is the default so every existing row and
--      every existing caller keeps its meaning; the unique key gains surface.
--   5. The auto-Butler trigger seeds the chat tool belt (ADR-0062:
--      search_transcripts / find_node / appearances_of / propose_knowledge /
--      recap) as a Campaign-existence invariant, exactly like the voice
--      defaults in 00025/00027, with an idempotent backfill for existing
--      Butlers. propose_knowledge carries the Butler's campaign-wide write
--      scope (ADR-0052) in the grant config, enforced in the handler.

CREATE TABLE planning_thread (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id uuid NOT NULL REFERENCES campaign (id) ON DELETE CASCADE,
    title       text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT planning_thread_id_campaign_unique UNIQUE (id, campaign_id)
);

CREATE INDEX planning_thread_campaign_idx ON planning_thread (campaign_id);

-- seq is the thread-local message order (1, 2, 3, …) the exchange loop appends
-- under; UNIQUE (thread_id, seq) both enforces it and indexes the ORDER BY seq
-- read. role is the two-value chat vocabulary — tool-call intermediates are
-- deliberately NOT persisted (ADR-0062: world knowledge re-arrives through the
-- tool belt on the next exchange; only the prose turns are the thread).
CREATE TABLE planning_message (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    thread_id   uuid NOT NULL,
    campaign_id uuid NOT NULL,
    seq         bigint NOT NULL,
    role        text NOT NULL CHECK (role IN ('user', 'assistant')),
    content     text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (thread_id, campaign_id)
        REFERENCES planning_thread (id, campaign_id) ON DELETE CASCADE,
    CONSTRAINT planning_message_thread_seq_unique UNIQUE (thread_id, seq)
);

ALTER TABLE agents
    ADD COLUMN chat_llm_provider_config_id uuid REFERENCES provider_config (id) ON DELETE SET NULL;

ALTER TYPE provider_component ADD VALUE IF NOT EXISTS 'chat_llm';

ALTER TABLE tool_agent_grant
    ADD COLUMN surface text NOT NULL DEFAULT 'voice' CHECK (surface IN ('voice', 'chat'));

ALTER TABLE tool_agent_grant
    DROP CONSTRAINT tool_agent_grant_agent_id_tool_name_key;

ALTER TABLE tool_agent_grant
    ADD CONSTRAINT tool_agent_grant_agent_tool_surface_key UNIQUE (agent_id, tool_name, surface);

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

    INSERT INTO tool_agent_grant (agent_id, tool_name, surface, config)
    VALUES (butler_id, 'search_transcripts', 'chat', NULL),
           (butler_id, 'find_node', 'chat', NULL),
           (butler_id, 'appearances_of', 'chat', NULL),
           (butler_id, 'propose_knowledge', 'chat', '{"scope": "campaign"}'::jsonb),
           (butler_id, 'recap', 'chat', NULL);

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- Backfill: existing Butlers get the chat tool belt they are now meant to have.
-- Idempotent via the new UNIQUE (agent_id, tool_name, surface) key.
INSERT INTO tool_agent_grant (agent_id, tool_name, surface, config)
SELECT a.id, t.tool_name, 'chat', t.config
  FROM agents a
 CROSS JOIN (VALUES
        ('search_transcripts', NULL::jsonb),
        ('find_node', NULL::jsonb),
        ('appearances_of', NULL::jsonb),
        ('propose_knowledge', '{"scope": "campaign"}'::jsonb),
        ('recap', NULL::jsonb)
      ) AS t (tool_name, config)
 WHERE a.agent_role = 'butler'
ON CONFLICT (agent_id, tool_name, surface) DO NOTHING;

-- +goose Down

-- Restore the 00027 trigger body (voice defaults only) BEFORE the grant surface
-- column goes, so the function never references a dropped column.
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

DELETE FROM tool_agent_grant WHERE surface = 'chat';

ALTER TABLE tool_agent_grant
    DROP CONSTRAINT tool_agent_grant_agent_tool_surface_key;

ALTER TABLE tool_agent_grant
    ADD CONSTRAINT tool_agent_grant_agent_id_tool_name_key UNIQUE (agent_id, tool_name);

ALTER TABLE tool_agent_grant
    DROP COLUMN surface;

ALTER TABLE agents
    DROP COLUMN IF EXISTS chat_llm_provider_config_id;

-- 'chat_llm' stays in the provider_component enum on down-migration: Postgres
-- cannot remove an enum value, and an orphaned value is harmless (00028 set the
-- precedent). Rows using it are removed so nothing references it.
DELETE FROM provider_config WHERE component = 'chat_llm';

DROP TABLE IF EXISTS planning_message;
DROP TABLE IF EXISTS planning_thread;
