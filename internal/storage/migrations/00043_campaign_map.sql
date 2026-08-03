-- +goose Up

-- Maps and Pins (#538, ADR-0060): world maps as first-class Campaign data, with
-- Knowledge Graph Nodes pinned onto them.
--
-- Before this, `resides_in` was a POINTER, not a position: "which NPCs are in
-- this city", "what is near the party" and "show me the tavern" were all
-- unanswerable, and space is the axis GMs think in most.
--
-- The image lives in the blob seam (ADR-0048), never in the row — campaign_map
-- carries only the key. Deletion goes through the seam too, not an FK cascade, so
-- the row's disappearance is not what frees the bytes.
CREATE TABLE campaign_map (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id uuid NOT NULL REFERENCES campaign (id) ON DELETE CASCADE,
    name        text NOT NULL,
    -- blob_key is t/<tenant>/map/<map_id>/image, validated by blob.ValidateKey
    -- before it ever reaches SQL.
    blob_key    text NOT NULL,
    -- The source image's pixel dimensions. Pins are normalized (below), so these
    -- are for aspect-ratio rendering, not for coordinate maths.
    width_px    integer NOT NULL,
    height_px   integer NOT NULL,
    -- Nesting: continent → region → city → building. parent_map_id is the map this
    -- one sits inside; anchor_node_id is the Location Node this map DEPICTS, which
    -- is what lets a pin on the world map open the city map beneath it.
    parent_map_id  uuid NULL REFERENCES campaign_map (id) ON DELETE SET NULL,
    anchor_node_id uuid NULL,
    gm_private  boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    -- The composite-FK target a Pin references, so a Pin provably cannot span
    -- campaigns without a trigger (the ADR-0008 pattern).
    CONSTRAINT campaign_map_id_campaign_unique UNIQUE (id, campaign_id),
    -- The anchor Node must be in the SAME Campaign — same declarative pattern.
    --
    -- ON DELETE SET NULL (anchor_node_id): the column list is MANDATORY here. A
    -- bare SET NULL on a composite FK nulls EVERY referencing column, which would
    -- include campaign_id — a NOT NULL column, so deleting an anchored Node would
    -- fail the whole delete. Naming the column confines the null to the anchor,
    -- which is the only part that lost its referent.
    CONSTRAINT campaign_map_anchor_fk FOREIGN KEY (anchor_node_id, campaign_id)
        REFERENCES kg_node (id, campaign_id) ON DELETE SET NULL (anchor_node_id),
    CONSTRAINT campaign_map_dimensions_positive CHECK (width_px > 0 AND height_px > 0)
);

CREATE INDEX campaign_map_campaign_idx ON campaign_map (campaign_id);
CREATE INDEX campaign_map_parent_idx ON campaign_map (parent_map_id);
CREATE INDEX campaign_map_anchor_idx ON campaign_map (anchor_node_id);

-- map_pin is a POSITION attached to something the wiki already knows. A Pin
-- always references a KG Node: Pins are not a parallel content model, which is
-- what makes "localize objects, NPCs or places" fall out for free and keeps one
-- source of truth. Pinning something with no entry means creating the Node first.
--
-- Coordinates are NORMALIZED 0..1, so re-uploading or rescaling a map image keeps
-- every pin exactly where the GM put it.
CREATE TABLE map_pin (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    map_id         uuid NOT NULL,
    campaign_id    uuid NOT NULL,
    node_id        uuid NOT NULL,
    x              double precision NOT NULL,
    y              double precision NOT NULL,
    label_override text NOT NULL DEFAULT '',
    gm_private     boolean NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    -- Both endpoints carry campaign_id in composite FKs, so a Pin cannot join a
    -- Map and a Node from different Campaigns (ADR-0008's no-trigger pattern).
    CONSTRAINT map_pin_map_fk FOREIGN KEY (map_id, campaign_id)
        REFERENCES campaign_map (id, campaign_id) ON DELETE CASCADE,
    -- Deleting a Node removes its Pins: a position with nothing at it is not
    -- information.
    CONSTRAINT map_pin_node_fk FOREIGN KEY (node_id, campaign_id)
        REFERENCES kg_node (id, campaign_id) ON DELETE CASCADE,
    -- One Pin per Node per Map. The same Node legitimately appears on the city
    -- map AND the tavern floor plan, which is why the key is (map, node) rather
    -- than node alone.
    CONSTRAINT map_pin_map_node_unique UNIQUE (map_id, node_id),
    CONSTRAINT map_pin_normalized CHECK (x >= 0 AND x <= 1 AND y >= 0 AND y <= 1)
);

CREATE INDEX map_pin_map_idx ON map_pin (map_id);
CREATE INDEX map_pin_node_idx ON map_pin (node_id);
CREATE INDEX map_pin_campaign_idx ON map_pin (campaign_id);

-- +goose Down

DROP TABLE IF EXISTS map_pin;
DROP TABLE IF EXISTS campaign_map;
