-- +goose Up

-- Aspects join the fulltext index (#542 follow-up). Without this, moving approved
-- facts out of kg_node.body and into kg_node_aspect would make every fact approved
-- after that slice UNFINDABLE: kg_node.fts (00011) is a generated tsvector over
-- name + body only, so both the GM wiki search and the Butler's kg_query would
-- match on the entry's NAME alone and silently lose the facts themselves.
--
-- A generated column cannot read another table, so the aspect text is denormalised
-- onto kg_node and kept current by a trigger on kg_node_aspect. A TRIGGER rather
-- than application writes is deliberate here: ADR-0008 prefers declarative
-- integrity, and search correctness must not depend on every future writer of the
-- aspect table (the Campaign Bundle importer is the next one) remembering to
-- refresh a cache.
--
-- TWO vectors, because Aspects are per-fact private (that is the whole point of
-- the table). fts_public excludes gm_private aspect text, so a prompt-facing
-- search cannot MATCH on a GM secret — returning "Bart" for a query about the
-- bribe would leak the secret through the hit itself, even though the row's
-- aspect payload correctly withholds it. The public/GM split therefore lives in
-- SQL, exactly like the row-level gm_private filter it parallels (#450).
ALTER TABLE kg_node ADD COLUMN aspect_text        text NOT NULL DEFAULT '';
ALTER TABLE kg_node ADD COLUMN aspect_text_public text NOT NULL DEFAULT '';

DROP INDEX IF EXISTS kg_node_fts_idx;
ALTER TABLE kg_node DROP COLUMN fts;

ALTER TABLE kg_node ADD COLUMN fts tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('simple', name), 'A') ||
    setweight(to_tsvector('simple', body), 'B') ||
    setweight(to_tsvector('simple', aspect_text), 'B')) STORED;

ALTER TABLE kg_node ADD COLUMN fts_public tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('simple', name), 'A') ||
    setweight(to_tsvector('simple', body), 'B') ||
    setweight(to_tsvector('simple', aspect_text_public), 'B')) STORED;

CREATE INDEX kg_node_fts_idx ON kg_node USING gin (fts);
CREATE INDEX kg_node_fts_public_idx ON kg_node USING gin (fts_public);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION kg_node_sync_aspect_text() RETURNS trigger AS $$
DECLARE
    new_id uuid := NULL;
    old_id uuid := NULL;
BEGIN
    -- NEW is unassigned on DELETE and OLD on INSERT, so each is read only where it
    -- exists; `IN (NULL, x)` then matches exactly x.
    IF TG_OP <> 'DELETE' THEN new_id := NEW.node_id; END IF;
    IF TG_OP <> 'INSERT' THEN old_id := OLD.node_id; END IF;

    -- BOTH endpoints are refreshed, not just NEW: an UPDATE that moves a row
    -- between Nodes would otherwise leave the OLD Node's cached text claiming a
    -- fact it no longer holds, and that ghost would keep matching searches. No
    -- current writer moves a row, but the Campaign Bundle importer is the next
    -- writer of this table and should not have to know that.
    UPDATE kg_node n
       SET aspect_text = COALESCE((
               SELECT string_agg(a.key || ' ' || a.value, ' ' ORDER BY a.position, a.id)
                 FROM kg_node_aspect a
                WHERE a.node_id = n.id), ''),
           aspect_text_public = COALESCE((
               SELECT string_agg(a.key || ' ' || a.value, ' ' ORDER BY a.position, a.id)
                 FROM kg_node_aspect a
                WHERE a.node_id = n.id AND NOT a.gm_private), '')
     WHERE n.id IN (new_id, old_id);
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER kg_node_aspect_fts_sync
    AFTER INSERT OR UPDATE OR DELETE ON kg_node_aspect
    FOR EACH ROW EXECUTE FUNCTION kg_node_sync_aspect_text();

-- Backfill any rows that already exist. In production 00041 and 00042 ship in the
-- same release so the table is born empty — but a branch-deployed test env, or a
-- Down/Up cycle, would otherwise leave existing aspects unindexed until the next
-- write touched their Node.
UPDATE kg_node n
   SET aspect_text = COALESCE((
           SELECT string_agg(a.key || ' ' || a.value, ' ' ORDER BY a.position, a.id)
             FROM kg_node_aspect a WHERE a.node_id = n.id), ''),
       aspect_text_public = COALESCE((
           SELECT string_agg(a.key || ' ' || a.value, ' ' ORDER BY a.position, a.id)
             FROM kg_node_aspect a WHERE a.node_id = n.id AND NOT a.gm_private), '')
 WHERE EXISTS (SELECT 1 FROM kg_node_aspect a WHERE a.node_id = n.id);

-- +goose Down

DROP TRIGGER IF EXISTS kg_node_aspect_fts_sync ON kg_node_aspect;
DROP FUNCTION IF EXISTS kg_node_sync_aspect_text();
DROP INDEX IF EXISTS kg_node_fts_public_idx;
DROP INDEX IF EXISTS kg_node_fts_idx;
ALTER TABLE kg_node DROP COLUMN IF EXISTS fts_public;
ALTER TABLE kg_node DROP COLUMN IF EXISTS fts;
ALTER TABLE kg_node DROP COLUMN IF EXISTS aspect_text_public;
ALTER TABLE kg_node DROP COLUMN IF EXISTS aspect_text;

ALTER TABLE kg_node ADD COLUMN fts tsvector GENERATED ALWAYS AS (
    setweight(to_tsvector('simple', name), 'A') ||
    setweight(to_tsvector('simple', body), 'B')) STORED;
CREATE INDEX kg_node_fts_idx ON kg_node USING gin (fts);
