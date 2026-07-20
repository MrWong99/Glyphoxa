-- +goose Up

-- First-registrar-wins guild binding (#483 hardening; full guild-permission proof
-- is #504). deployment_config.guild_id was tenant-controlled free text with a
-- newest-wins Guild→Tenant read: Tenant B saving victim A's guild_id silently
-- rebound the guild, letting B read A's voice-channel members and hijack A's
-- command routing — a cross-tenant PII leak. A partial UNIQUE index makes the
-- binding exclusive: the first Tenant to register a guild owns it until it moves
-- off (guild_id = '' rows — the unconfigured state — stay unconstrained).
--
-- Defensive dedupe BEFORE the index (this migration must not hard-fail a deploy
-- whose table already carries duplicate guild_id rows): every duplicate except
-- the FIRST registrar (oldest created_at, tenant_id as the deterministic
-- tiebreak) has its guild binding cleared back to the unconfigured state. Those
-- Tenants were squatting on (or victims of) a shared guild under newest-wins
-- semantics; clearing forces an explicit re-save, which the new index then
-- adjudicates first-registrar-wins. NOTE this deliberately flips the previous
-- newest-wins order — the newest binder was the likelier hijacker, the oldest
-- the likelier victim.
UPDATE deployment_config d
   SET guild_id = '', voice_channel_id = '', updated_at = now()
 WHERE d.guild_id <> ''
   AND EXISTS (
       SELECT 1 FROM deployment_config o
        WHERE o.guild_id = d.guild_id
          AND (o.created_at, o.tenant_id) < (d.created_at, d.tenant_id));

CREATE UNIQUE INDEX deployment_config_guild_owner
    ON deployment_config (guild_id)
    WHERE guild_id <> '';

-- +goose Down

DROP INDEX IF EXISTS deployment_config_guild_owner;
