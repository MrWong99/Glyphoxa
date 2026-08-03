-- +goose Up

-- kg_node_aspect is the per-fact visibility layer on a Knowledge Graph Node
-- (#542, ADR-0008 third amendment): an ordered list of (key, value) rows, each
-- with its OWN gm_private flag. Before this, gm_private was all-or-nothing per
-- Node, so an NPC with public facts and one secret forced the GM to either leak
-- the secret into every prompt or split the character into two entries and lose
-- the graph.
--
-- kg_node.body is deliberately RETAINED as the free-form remainder: nothing
-- migrates, every existing entry keeps working, and a Node with no Aspects
-- renders exactly as it did before.
--
-- Same-campaign integrity uses the composite-FK pattern ADR-0008 established for
-- kg_edge: the row carries campaign_id and references kg_node (id, campaign_id),
-- so an Aspect provably cannot span campaigns without a trigger. ON DELETE
-- CASCADE reaps a Node's Aspects with it.
CREATE TABLE kg_node_aspect (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    node_id     uuid NOT NULL,
    campaign_id uuid NOT NULL,
    position    integer NOT NULL,
    key         text NOT NULL,
    value       text NOT NULL DEFAULT '',
    gm_private  boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT kg_node_aspect_node_fk FOREIGN KEY (node_id, campaign_id)
        REFERENCES kg_node (id, campaign_id) ON DELETE CASCADE
);

-- Every read is "this Node's aspects in author order" — the prompt-facing
-- neighbourhood read joins one of these per returned Node, so the index carries
-- the ordering too and the aggregate never sorts.
CREATE INDEX kg_node_aspect_node_idx ON kg_node_aspect (node_id, position, id);

-- Campaign-wide reads (the Campaign Bundle export, ADR-0053) scope by campaign.
CREATE INDEX kg_node_aspect_campaign_idx ON kg_node_aspect (campaign_id);

-- +goose Down

DROP TABLE IF EXISTS kg_node_aspect;
