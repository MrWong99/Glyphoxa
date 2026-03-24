-- Lore documents: rich-text lore entries attached to campaigns.
CREATE TABLE IF NOT EXISTS mgmt.lore_documents (
    id                  TEXT        PRIMARY KEY,
    campaign_id         TEXT        NOT NULL REFERENCES mgmt.campaigns(id) ON DELETE CASCADE,
    title               TEXT        NOT NULL,
    content_markdown    TEXT        NOT NULL DEFAULT '',
    sort_order          INT         NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_mgmt_lore_documents_campaign
    ON mgmt.lore_documents(campaign_id);

-- Campaign-NPC links: allows NPCs to appear in secondary campaigns.
CREATE TABLE IF NOT EXISTS mgmt.campaign_npcs (
    campaign_id TEXT NOT NULL,
    npc_id      TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (campaign_id, npc_id)
);
