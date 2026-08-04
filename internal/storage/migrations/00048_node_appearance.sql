-- +goose Up

-- The Appearances index (#545, ADR-0008 amendment): where each Knowledge Graph
-- Node was mentioned in play.
--
-- "When did we last see that ogre" is the question a GM asks constantly and the
-- wiki cannot answer, because a Node records what a thing IS and never where it
-- came up. The obvious modelling answer — an Edge from the Node to something —
-- is the wrong one twice over: an Edge is world truth an NPC's Hot Context walks
-- (ADR-0008), so mentions would silently become facts the NPC recites, and the
-- edge type vocabulary is closed for exactly that reason.
--
-- So this is a RETRIEVAL index, deliberately outside the graph. Nothing here is
-- ever read by prompt assembly; it exists to send the GM to a transcript line.
--
-- The row is (Node, Line): one appearance per mention, keyed so re-indexing the
-- same session is idempotent rather than duplicating.
CREATE TABLE node_appearance (
    node_id          uuid NOT NULL,
    campaign_id      uuid NOT NULL,
    voice_session_id uuid NOT NULL REFERENCES voice_sessions (id) ON DELETE CASCADE,
    -- The relay's stable per-session Line key ("u:<n>" / "a:<turn>"), NOT a
    -- surrogate id. transcript_line's id column exists in SQL but is unreachable
    -- from Go: storage.TranscriptLine has no ID field and its insert/scan column
    -- lists are order-coupled, so adding one would touch every caller of a
    -- load-bearing table for no gain. (voice_session_id, line_id) is already
    -- UNIQUE there, so it is a real key.
    line_id          text NOT NULL,
    -- When the line was spoken, denormalised so the "last seen" read needs no
    -- join to sort.
    at               timestamptz NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),

    -- One appearance per Node per Line. Re-indexing a session is then an
    -- ON CONFLICT DO NOTHING, which is what makes the job safely re-runnable.
    PRIMARY KEY (node_id, voice_session_id, line_id),

    -- Same-campaign integrity, declarative, the ADR-0008 pattern: an appearance
    -- provably cannot join a Node and a session from different Campaigns.
    CONSTRAINT node_appearance_node_fk FOREIGN KEY (node_id, campaign_id)
        REFERENCES kg_node (id, campaign_id) ON DELETE CASCADE,
    -- The retraction guarantee (#437, ADR-0040): a Line that is deleted takes its
    -- appearances with it. A GM who retracts something said at the table must not
    -- find it still listed under an entry.
    CONSTRAINT node_appearance_line_fk FOREIGN KEY (voice_session_id, line_id)
        REFERENCES transcript_line (voice_session_id, line_id) ON DELETE CASCADE
);

-- The one read this table exists for: an entry's appearances, most recent first.
CREATE INDEX node_appearance_node_at_idx ON node_appearance (node_id, at DESC);
-- Re-indexing and session deletion both work session-at-a-time.
CREATE INDEX node_appearance_session_idx ON node_appearance (voice_session_id);

-- +goose Down

DROP TABLE IF EXISTS node_appearance;
