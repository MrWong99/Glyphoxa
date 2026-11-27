-- +goose Up

-- invitation_refusal (ADR-0065 review hardening): "this link must no longer
-- admit this Discord User". Written when what the link gave them is taken away
-- again — an Access Grant revoked, a Campaign Guest removed — because the link
-- itself usually stays live (a multi-use link has other people still to admit)
-- and would otherwise let the removed person straight back in. Redemption
-- refuses a (link, person) pair found here and counts no use.
--
-- Per link on purpose: a revoke / removal writes a row for every link of that
-- scope existing at the time, so a link created AFTERWARDS admits them again,
-- and so does an explicit grant / add by Discord User ID. Rows are never
-- cleared (they are inert while the person is admitted). Keyed by snowflake
-- like access_grant, so it does not depend on a users row. Rows go with their
-- invitation.
CREATE TABLE invitation_refusal (
    invitation_id   uuid NOT NULL REFERENCES invitation (id) ON DELETE CASCADE,
    discord_user_id text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (invitation_id, discord_user_id)
);

-- deployment_settings.platform_admin_ids: the Platform Admin snowflakes
-- (GLYPHOXA_OPERATOR_IDS) as the web tier last booted with them. The env list
-- stays the only place they are CONFIGURED (ADR-0065); this is a read-only
-- copy for the processes that are never given it. A voice worker has no
-- GLYPHOXA_OPERATOR_IDS, yet on an `allowlist` deployment it must tell an
-- operator who is a Platform Admin (GM on Discord without an Access Grant)
-- from one whose grant was revoked. NULL = never recorded (no web boot on this
-- schema yet): a reader that depends on it then requires nothing, exactly the
-- behaviour before this column.
ALTER TABLE deployment_settings ADD COLUMN platform_admin_ids text[];

-- +goose Down

ALTER TABLE deployment_settings DROP COLUMN IF EXISTS platform_admin_ids;
DROP TABLE IF EXISTS invitation_refusal;
