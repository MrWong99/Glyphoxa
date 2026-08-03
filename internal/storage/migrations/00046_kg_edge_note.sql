-- +goose Up

-- Edge notes and disposition (#546, ADR-0008): a relation carries texture.
--
-- `knows` today carries nothing. "Knows and owes money to" versus "knows and
-- despises" is the difference between a flat NPC and a live one — and Edges are
-- already strictly directional with no auto-inverse (ADR-0008 amendment), which is
-- exactly what makes ASYMMETRIC feelings expressible: a secretly hostile ally is
-- one edge with a negative disposition and no matching return edge.
--
-- Defaulted columns on the existing table; no new table, and the
-- UNIQUE (from, to, type) key is untouched.
ALTER TABLE kg_edge ADD COLUMN note text NOT NULL DEFAULT '';

-- disposition is -2..+2: hostile, cold, neutral, warm, devoted. A small closed
-- range rather than a free integer, because it drives an edge colour and a single
-- prompt clause — a scale nobody can calibrate is worse than none.
ALTER TABLE kg_edge ADD COLUMN disposition smallint NOT NULL DEFAULT 0;

ALTER TABLE kg_edge ADD CONSTRAINT kg_edge_disposition_range
    CHECK (disposition >= -2 AND disposition <= 2);

-- +goose Down

ALTER TABLE kg_edge DROP CONSTRAINT IF EXISTS kg_edge_disposition_range;
ALTER TABLE kg_edge DROP COLUMN IF EXISTS disposition;
ALTER TABLE kg_edge DROP COLUMN IF EXISTS note;
