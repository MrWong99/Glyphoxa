-- +goose Up

-- Rollover tape opt-in and per-Speaker consent (#306, ADR-0051). tape_armed is the
-- Campaign-level GM opt-in that arms the feature; default false — capture is
-- hard-disabled without it (ADR-0051 "default OFF"). Appended LAST in
-- campaignColumns/scanCampaign (column-order coupling), so any new column follows
-- it in both places.
ALTER TABLE campaign ADD COLUMN tape_armed boolean NOT NULL DEFAULT false;

-- tape_consent records the individual, revocable consent each human participant
-- gives once per Campaign (ADR-0051: "every human participant, individually").
-- Presence between the disclosure buttons and the live tape: a row's presence
-- means the Speaker consented; deleting it revokes. Cascades away when the
-- Campaign is deleted (#265 semantics).
CREATE TABLE tape_consent (
    campaign_id     uuid NOT NULL REFERENCES campaign(id) ON DELETE CASCADE,
    discord_user_id text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (campaign_id, discord_user_id)
);

-- +goose Down

DROP TABLE tape_consent;
ALTER TABLE campaign DROP COLUMN tape_armed;
