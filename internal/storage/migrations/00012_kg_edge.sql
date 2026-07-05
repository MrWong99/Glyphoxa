-- +goose Up

-- kg_edge is the Knowledge Graph's typed directional Edge table (ADR-0008 v1.0 +
-- 2026-07-04 amendment, #132): one-way relationships between two same-Campaign
-- Nodes. UNIQUE(from,to,type) makes a relationship idempotent; the same-Campaign
-- constraint is declarative — each endpoint FK is composite (id, campaign_id)
-- against kg_node's UNIQUE(id, campaign_id), so an Edge cannot span campaigns and
-- there is NO direct edge→campaign FK and NO trigger. ON DELETE CASCADE from both
-- endpoints cleans up a Node's incident Edges when it is deleted. No auto-inverse:
-- a mutual relationship is two rows.
CREATE TYPE kg_edge_type AS ENUM ('resides_in','member_of','owns','knows',
  'enemy_of','ally_of','parent_of','participated_in','mentioned_in');

CREATE TABLE kg_edge (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    campaign_id   uuid NOT NULL,
    from_node_id  uuid NOT NULL,
    to_node_id    uuid NOT NULL,
    edge_type     kg_edge_type NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT kg_edge_unique  UNIQUE (from_node_id, to_node_id, edge_type),
    CONSTRAINT kg_edge_no_self CHECK (from_node_id <> to_node_id),
    CONSTRAINT kg_edge_from_fk FOREIGN KEY (from_node_id, campaign_id)
        REFERENCES kg_node (id, campaign_id) ON DELETE CASCADE,
    CONSTRAINT kg_edge_to_fk   FOREIGN KEY (to_node_id, campaign_id)
        REFERENCES kg_node (id, campaign_id) ON DELETE CASCADE
);

CREATE INDEX kg_edge_from_idx ON kg_edge (from_node_id);
CREATE INDEX kg_edge_to_idx   ON kg_edge (to_node_id);

-- The NPC-Node ↔ Character NPC Agent link (ADR-0008 amendment / ADR-0009): the
-- wiki side carries the link so the polymorphic agents table stays untouched.
-- UNIQUE so an Agent voices at most one Node; ON DELETE SET NULL so deleting the
-- Agent clears the link (the Node survives); the CHECK keeps the link NPC-only.
ALTER TABLE kg_node
    ADD COLUMN agent_id uuid REFERENCES agents (id) ON DELETE SET NULL,
    ADD CONSTRAINT kg_node_agent_unique  UNIQUE (agent_id),
    ADD CONSTRAINT kg_node_agent_npc_only CHECK (node_type = 'npc' OR agent_id IS NULL);

-- +goose Down

ALTER TABLE kg_node
    DROP CONSTRAINT IF EXISTS kg_node_agent_npc_only,
    DROP CONSTRAINT IF EXISTS kg_node_agent_unique,
    DROP COLUMN IF EXISTS agent_id;
DROP TABLE IF EXISTS kg_edge;
DROP TYPE IF EXISTS kg_edge_type;
