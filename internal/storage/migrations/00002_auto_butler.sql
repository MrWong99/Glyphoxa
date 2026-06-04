-- +goose Up

-- Auto-Butler per Campaign (ADR-0009): "Each Campaign auto-creates its own
-- Butler on creation with hardcoded defaults (name 'Glyphoxa')." The unique
-- index in 00001 enforces *at most one* Butler per Campaign; this trigger
-- guarantees *exactly one* by creating it the instant a Campaign row appears,
-- so no application code path can forget to. The GM edits the auto-created
-- Butler post-creation.
--
-- Why a trigger and not application code: campaign creation will eventually live
-- in the control-plane (#6), but the Butler is an invariant of a Campaign's
-- existence, not of any one creation path. Enforcing it in the database means a
-- Campaign inserted by a seed script, a test, or #6's handler all get their
-- Butler identically — the invariant can't drift per call site.
--
-- Defaults (ADR-0009 + Q14 amendment):
--   name        = 'Glyphoxa'
--   agent_role  = 'butler'
--   address_only = true   (the Butler is Address-Only by default, ADR-0024)
-- Tool Grants are NOT seeded here: the grants join table is owned by the tool /
-- control-plane work (ADR-0029, #6). The as-built default grant is `dice` only
-- (Q14); it is attached when that table and the grant-seeding path land.

-- +goose StatementBegin
CREATE FUNCTION create_campaign_butler() RETURNS trigger AS $$
BEGIN
    INSERT INTO agents (campaign_id, agent_role, name, address_only)
    VALUES (NEW.id, 'butler', 'Glyphoxa', true);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER campaign_auto_butler
    AFTER INSERT ON campaign
    FOR EACH ROW
    EXECUTE FUNCTION create_campaign_butler();

-- +goose Down

DROP TRIGGER IF EXISTS campaign_auto_butler ON campaign;
DROP FUNCTION IF EXISTS create_campaign_butler();
