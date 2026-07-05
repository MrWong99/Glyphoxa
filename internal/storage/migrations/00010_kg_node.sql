-- +goose Up

-- kg_node is the Knowledge Graph's typed-Node table (ADR-0008 v1.0): the
-- structured wiki / GM notes for one Campaign. The full 7-value node_type enum
-- ships now so later KG slices (#129 all types, #132 Edges) need no schema
-- change; node_type is IMMUTABLE after create (no UPDATE path). body holds the
-- Node's prose; gm_private hides a Node from any NPC's Hot Context (#126). The
-- UNIQUE (id, campaign_id) is the composite-FK target #132's Edges reference so
-- an Edge's endpoints are provably same-Campaign without a trigger (ADR-0008
-- 2026-07-04 amendment).
CREATE TYPE kg_node_type AS ENUM
  ('character','npc','location','faction','item','plot_thread','note');

CREATE TABLE kg_node (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id  uuid NOT NULL REFERENCES campaign (id) ON DELETE CASCADE,
    node_type    kg_node_type NOT NULL,
    name         text NOT NULL,
    body         text NOT NULL DEFAULT '',
    gm_private   boolean NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT kg_node_id_campaign_unique UNIQUE (id, campaign_id)
);

-- Nodes are always read scoped to their Campaign (list, prompt injection).
CREATE INDEX kg_node_campaign_idx ON kg_node (campaign_id);

-- +goose Down

DROP TABLE IF EXISTS kg_node;
DROP TYPE IF EXISTS kg_node_type;
