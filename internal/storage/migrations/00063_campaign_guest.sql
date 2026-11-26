-- +goose Up

-- Campaign Guests (ADR-0065): people a GM lets into ONE Campaign with an
-- explicit set of Guest Rights, without making them Members of the Tenant.
-- This is the lane ADR-0003 reserved and ADR-0056 specified but never built;
-- ADR-0065 replaces 0056's three fixed Player Access Levels with a rights set.

-- campaign_guest binds a user to a Campaign. rights is the NORMALIZED Guest
-- Right vocabulary (auth.GuestRight strings, e.g. 'wiki.read'); an unknown
-- string grants nothing — the auth tier only ever tests for known rights.
-- Keyed by users.id: a guest added by snowflake before their first sign-in
-- gets a users row with an empty name, refreshed from Discord when they do
-- sign in. Removing a guest is a row delete and takes effect on their next
-- request (the workspace is re-resolved per request).
CREATE TABLE campaign_guest (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id   uuid NOT NULL REFERENCES campaign (id) ON DELETE CASCADE,
    user_id       uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    rights        text[] NOT NULL DEFAULT '{}',
    added_by      uuid REFERENCES users (id) ON DELETE SET NULL,
    invitation_id uuid REFERENCES invitation (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT campaign_guest_unique UNIQUE (campaign_id, user_id)
);

CREATE INDEX campaign_guest_user_idx ON campaign_guest (user_id);

-- sessions.guest_campaign_id is the workspace this browser session acts in:
-- NULL = the user's own Tenant, set = acting as a Campaign Guest of that
-- Campaign. It is a SELECTION, never an authorization: every request joins it
-- against campaign_guest, so a stale value (the guest was removed) simply
-- stops resolving. ON DELETE SET NULL drops the selection with the Campaign.
ALTER TABLE sessions
    ADD COLUMN guest_campaign_id uuid REFERENCES campaign (id) ON DELETE SET NULL;

-- planning_thread.created_by scopes a Campaign Guest to their own planning
-- threads (GUEST_RIGHT_PLANNING_USE): NULL = a thread from before this column,
-- which belongs to the GM and is never shown to a guest.
ALTER TABLE planning_thread
    ADD COLUMN created_by uuid REFERENCES users (id) ON DELETE SET NULL;

-- +goose Down

ALTER TABLE planning_thread DROP COLUMN IF EXISTS created_by;
ALTER TABLE sessions DROP COLUMN IF EXISTS guest_campaign_id;
DROP TABLE IF EXISTS campaign_guest;
