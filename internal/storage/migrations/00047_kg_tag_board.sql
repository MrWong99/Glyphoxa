-- +goose Up

-- Free-form Node tags and saved prep boards (#543, ADR-0008).
--
-- WHY TAGS ARE NOT NEW NODE TYPES: the seven types are a SCHEMA. They carry
-- edge-validity rules (resides_in → Location, member_of → Faction) and prompt
-- semantics, and node_type is immutable after create — so keeping the vocabulary
-- closed is right. Tags are ORGANIZATION, and organization should not require a
-- migration and an enum edit every time a GM invents a category ("seafaring",
-- "act two", "needs a voice").
--
-- Tags and boards are GM organization ONLY: neither enters a prompt and neither
-- affects AgentNodeFacts. That keeps the fact budget untouched and stops tags
-- quietly becoming a second, unvalidated type system inside NPC context.
CREATE TABLE kg_node_tag (
    node_id     uuid NOT NULL,
    campaign_id uuid NOT NULL,
    -- Create-on-use, campaign-scoped, autocompleted from what already exists.
    tag         text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (node_id, tag),
    CONSTRAINT kg_node_tag_node_fk FOREIGN KEY (node_id, campaign_id)
        REFERENCES kg_node (id, campaign_id) ON DELETE CASCADE,
    CONSTRAINT kg_node_tag_not_blank CHECK (btrim(tag) <> '')
);

-- The two reads: a campaign's tag vocabulary (for chips and autocomplete), and
-- "which entries carry this tag" (for filtering the list and the graph).
CREATE INDEX kg_node_tag_campaign_tag_idx ON kg_node_tag (campaign_id, tag);

-- A prep board is a named, ordered set of entries the GM pins for one session —
-- the "tonight: the harbour heist" text file they already keep outside the tool.
CREATE TABLE kg_board (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id uuid NOT NULL REFERENCES campaign (id) ON DELETE CASCADE,
    name        text NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    -- The composite-FK target kg_board_node references, so a board entry cannot
    -- span campaigns without a trigger (the ADR-0008 pattern).
    CONSTRAINT kg_board_id_campaign_unique UNIQUE (id, campaign_id)
);

CREATE INDEX kg_board_campaign_idx ON kg_board (campaign_id);

CREATE TABLE kg_board_node (
    board_id    uuid NOT NULL,
    node_id     uuid NOT NULL,
    campaign_id uuid NOT NULL,
    position    integer NOT NULL,
    PRIMARY KEY (board_id, node_id),
    CONSTRAINT kg_board_node_board_fk FOREIGN KEY (board_id, campaign_id)
        REFERENCES kg_board (id, campaign_id) ON DELETE CASCADE,
    -- Deleting an entry removes it from every board: a board row pointing at
    -- nothing is not a reminder.
    CONSTRAINT kg_board_node_node_fk FOREIGN KEY (node_id, campaign_id)
        REFERENCES kg_node (id, campaign_id) ON DELETE CASCADE
);

CREATE INDEX kg_board_node_board_idx ON kg_board_node (board_id, position);

-- +goose Down

DROP TABLE IF EXISTS kg_board_node;
DROP TABLE IF EXISTS kg_board;
DROP TABLE IF EXISTS kg_node_tag;
