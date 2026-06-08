-- +goose Up

-- Core persistence schema for Glyphoxa v2.
--
-- Scope (task #8): tenant, campaign, agents (polymorphic Butler/Character NPC),
-- provider_config (BYOK, encrypted creds), and a transcript-chunk skeleton with
-- the pgvector/tsvector machinery ADR-0011 requires. The full Knowledge Graph
-- (Nodes/Edges, ADR-0008) and the control-plane tables (members, guilds,
-- voice_sessions — task #6) are intentionally NOT built here. Columns that
-- reach into those out-of-set tables are nullable stub UUIDs with no FK, marked
-- "SEAM (#6)"; the constraint is added when those tables land.

CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pgcrypto; -- gen_random_uuid()

-- ── Enums ────────────────────────────────────────────────────────────────────

-- Agent Role (ADR-0009): one polymorphic agents table, archetype via this enum.
CREATE TYPE agent_role AS ENUM ('butler', 'character');

-- Component (ADR-0004): a Provider category a Provider Config binds to.
CREATE TYPE provider_component AS ENUM ('llm', 'stt', 'tts', 'embeddings', 's2s');

-- ── tenant ───────────────────────────────────────────────────────────────────
-- Top-level isolation boundary; owns Campaigns, Provider Configs, (Members &
-- Guilds — task #6).
CREATE TABLE tenant (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- ── campaign ─────────────────────────────────────────────────────────────────
-- A persistent TTRPG game owned by a Tenant and GM'd by one Member.
CREATE TABLE campaign (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenant (id) ON DELETE CASCADE,
    -- The GM is a Member (Member Role 'gm'). The members table is task #6, so
    -- this is a bare nullable UUID for now. SEAM (#6): becomes
    -- "NOT NULL REFERENCES member(id)".
    gm_member_id  uuid,
    name          text NOT NULL,
    -- TTRPG ruleset (e.g. "dnd5e"); free text in v1.0.
    system        text NOT NULL DEFAULT '',
    -- Campaign Language: selects phonetic scheme + STT/TTS hint (BCP-47-ish).
    language      text NOT NULL DEFAULT 'en',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX campaign_tenant_idx ON campaign (tenant_id);

-- ── provider_config ──────────────────────────────────────────────────────────
-- Tenant-scoped, BYOK, encrypted (ADR-0004). Credentials are AES-GCM ciphertext
-- in `credentials_ciphertext` (app-secret env var key); write-only after save,
-- with `credentials_last4` the only plaintext kept for display. No per-campaign
-- overrides, no spend caps in MVP. Created before `agents` because an Agent's
-- voice/llm columns reference it.
CREATE TABLE provider_config (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenant (id) ON DELETE CASCADE,
    component     provider_component NOT NULL,
    -- Provider identifier within the component's 2-provider matrix, e.g.
    -- "anthropic"/"ollama" (llm), "elevenlabs"/"coqui" (tts).
    provider      text NOT NULL,
    -- Selected model/voice id from the provider's list endpoint.
    model         text NOT NULL DEFAULT '',
    credentials_ciphertext  bytea NOT NULL,
    credentials_last4       text  NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX provider_config_tenant_component_idx
    ON provider_config (tenant_id, component);

-- ── agents ───────────────────────────────────────────────────────────────────
-- Polymorphic per ADR-0009: Butler and Character NPC share one table and one
-- orchestrator/address-detection code path. Both are Campaign-scoped (the
-- glossary line calling the Butler "one per Tenant" is the stale outlier;
-- ADR-0009 + CONTEXT relationships + the dialogue all say per-Campaign).
CREATE TABLE agents (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id   uuid NOT NULL REFERENCES campaign (id) ON DELETE CASCADE,
    agent_role    agent_role NOT NULL,
    name          text NOT NULL,
    -- Persona: markdown personality/backstory/speech style injected into prompts.
    persona       text NOT NULL DEFAULT '',
    -- Voice (ADR-0022/0023): TTS provider + voice-id. Held as JSONB so the
    -- {provider, voice_id, …} shape can evolve without a migration; the bound
    -- TTS Provider Config is resolved via voice_provider_config_id.
    voice                     jsonb NOT NULL DEFAULT '{}'::jsonb,
    voice_provider_config_id  uuid REFERENCES provider_config (id) ON DELETE SET NULL,
    -- LLM Provider Config this Agent reasons with (nullable). Picking a tenant
    -- default when null is a #6 concern: this schema has no is_default marker,
    -- so no fallback is implied here.
    llm_provider_config_id    uuid REFERENCES provider_config (id) ON DELETE SET NULL,
    -- Address-Only (ADR-0024): reachable only by explicit name/alias. Butler
    -- defaults true; Character NPC defaults false. Set per-row at insert.
    address_only  boolean NOT NULL DEFAULT false,
    -- Extra name/aliases for Address Detection beyond `name`.
    aliases       text[] NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX agents_campaign_idx ON agents (campaign_id);

-- Exactly one Butler per Campaign (ADR-0009). Partial unique index leaves
-- Character NPCs uncapped.
CREATE UNIQUE INDEX agents_one_butler_per_campaign
    ON agents (campaign_id)
    WHERE agent_role = 'butler';

-- Default Tool Grant set for an auto-created Butler is `dice` only (ADR-0009
-- Q14 amendment); transcript_search / rules_lookup join the set as those tools
-- are built. Tool Grants are a join table owned by the tool/control-plane work
-- (ADR-0029, task #6), not built here.

-- ── transcript_chunk ─────────────────────────────────────────────────────────
-- Storage unit for Transcripts is the chunk (3–6 utterances), ADR-0011.
-- Embeddings are async/eventually-consistent: rows insert with embedding NULL,
-- a background worker UPDATEs it. Retrieval filters WHERE embedding IS NOT NULL;
-- the HNSW index is partial on non-null embeddings. v1.0 user-facing search is
-- tsvector-only (the `fts` generated column + GIN index below).
--
-- Default embedding model: Ollama nomic-embed-text, 768-dim (ADR-0011).
CREATE TABLE transcript_chunk (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id   uuid NOT NULL REFERENCES campaign (id) ON DELETE CASCADE,
    -- A Transcript belongs to a Voice Session. The voice_sessions table is
    -- task #6. SEAM (#6): becomes "NOT NULL REFERENCES voice_session(...)".
    voice_session_id  uuid,
    -- Concatenated text of the 3–6 utterances in this chunk.
    content       text NOT NULL,
    -- Discord Users who spoke in this chunk (snowflakes), for attribution.
    speaker_discord_user_ids  text[] NOT NULL DEFAULT '{}',
    -- Agents that participated; hard filter for NPC-knowledge retrieval
    -- (ANN on participated set) vs campaign-wide topical context (ADR-0011).
    participated_agent_ids    uuid[] NOT NULL DEFAULT '{}',
    -- Async embedding: NULL until the background worker fills it.
    embedding     vector(768),
    -- v1.0 fulltext search (ADR-0008/0011): generated tsvector over content.
    fts           tsvector GENERATED ALWAYS AS (to_tsvector('simple', content)) STORED,
    started_at    timestamptz NOT NULL DEFAULT now(),
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX transcript_chunk_campaign_idx ON transcript_chunk (campaign_id);

-- tsvector fulltext index (ADR-0008/0011).
CREATE INDEX transcript_chunk_fts_idx ON transcript_chunk USING gin (fts);

-- Partial HNSW index on non-null embeddings only (ADR-0011). Cosine distance —
-- nomic-embed-text vectors are compared by cosine similarity.
CREATE INDEX transcript_chunk_embedding_hnsw_idx
    ON transcript_chunk USING hnsw (embedding vector_cosine_ops)
    WHERE embedding IS NOT NULL;

-- +goose Down

-- Drop in dependency order (dependents first): agents references both
-- provider_config and campaign, so it must go before either.
DROP TABLE IF EXISTS transcript_chunk;
DROP TABLE IF EXISTS agents;
DROP TABLE IF EXISTS provider_config;
DROP TABLE IF EXISTS campaign;
DROP TABLE IF EXISTS tenant;

DROP TYPE IF EXISTS provider_component;
DROP TYPE IF EXISTS agent_role;

-- Extensions are left installed: dropping them is database-global and may be
-- shared. goose Down reverses this migration's tables/types; the extensions are
-- harmless to leave and other databases on the cluster may depend on them.
