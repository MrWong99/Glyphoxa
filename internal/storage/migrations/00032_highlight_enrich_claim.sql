-- +goose Up
-- Race-proof Highlight image-enrichment claim (#406): a nullable timestamp the
-- enrich job sets to claim a Highlight before it spends on generation, so two
-- concurrent enrich jobs for the same Highlight run the provider Generate AT MOST
-- once (a conditional UPDATE claims iff still imageless and no fresh claim within
-- the lease). It is DELIBERATELY absent from `highlightColumns` and never scanned
-- onto the wire (toProtoHighlight omits it) — the marker never leaks into an RPC
-- response. NULL means unclaimed; a stale claim (older than the enrich lease) is
-- reclaimable, so a crashed worker never strands a Highlight imageless.
ALTER TABLE highlight ADD COLUMN image_enrich_claimed_at timestamptz;

-- +goose Down
ALTER TABLE highlight DROP COLUMN IF EXISTS image_enrich_claimed_at;
