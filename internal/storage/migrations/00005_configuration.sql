-- +goose Up

-- Backs the Configuration screen's save/mask/Replace flow (#68; ADR-0004 BYOK /
-- ADR-0039 single-operator). Two additive changes:
--
--   1. A UNIQUE key on provider_config (tenant_id, component, provider) so saving
--      a BYOK key UPSERTs the matching row — the operator replacing a key, or the
--      first real key overwriting the seed's "env" placeholder — instead of
--      inserting duplicates. The seed's three rows ((llm,groq), (tts,elevenlabs),
--      (stt,elevenlabs)) are already distinct on this key, so it is safe to add.
--
--   2. deployment_config: the Discord integration the screen also holds — the
--      single deployment Bot token (a write-only secret, sealed at rest with the
--      same app secret as provider_config) plus the non-secret Guild / Voice
--      channel IDs. The Bot is deployment-shared (one token regardless of Tenant —
--      CONTEXT.md), so it is NOT a Provider Config (ADR-0004 is per-Component,
--      Tenant-scoped). It is keyed by tenant_id for the single operator now and
--      de-tenants when multi-deploy lands (ADR-0039 thin pass-through).

CREATE UNIQUE INDEX provider_config_tenant_component_provider_key
    ON provider_config (tenant_id, component, provider);

CREATE TABLE deployment_config (
    tenant_id  uuid PRIMARY KEY REFERENCES tenant (id) ON DELETE CASCADE,
    -- Discord Bot token, write-only after save: AES-GCM ciphertext (empty until
    -- saved); last4 is the only plaintext kept for display (ADR-0004).
    discord_bot_token_ciphertext  bytea NOT NULL DEFAULT '',
    discord_bot_token_last4       text  NOT NULL DEFAULT '',
    -- Non-secret Discord IDs the screen edits as plain text.
    guild_id          text NOT NULL DEFAULT '',
    voice_channel_id  text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- +goose Down

DROP TABLE IF EXISTS deployment_config;
DROP INDEX IF EXISTS provider_config_tenant_component_provider_key;
