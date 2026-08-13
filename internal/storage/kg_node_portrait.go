package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Node portrait persistence (#590): the blob-key half of the portrait feature,
// following campaign_map's split — the bytes live in the blob seam (ADR-0048),
// the row carries only the key, and the RPC layer owns writing/releasing the
// bytes around these row mutations.

// SetNodePortrait repoints a Node at new portrait bytes (” clears it) and
// stamps updated_at = now(), which is the portrait URL's cache validator — a
// portrait write always mints a NEW blob key (ReplaceMapImage's ritual), so a
// stale validator can never serve stale bytes.
//
// It returns the updated Node (Aspects included, GM read) and the PREVIOUS
// portrait blob key (” when there was none) so the caller can release the
// superseded bytes through the seam. The write is scoped to (id, campaign_id)
// (#342); a Node in another Campaign yields ErrNotFound.
//
// The embedding columns are deliberately untouched: a portrait changes no
// embedded text. The updated_at bump can at worst make an in-flight embedworker
// write miss its stale-guard and re-embed next pass (#300) — correct, merely a
// re-run.
func (s *Store) SetNodePortrait(ctx context.Context, campaignID, nodeID uuid.UUID, blobKey string) (KGNode, string, error) {
	// The locked subquery in FROM (#590 review) is load-bearing twice over.
	// Against a RACE: under READ COMMITTED a plain snapshot read would hand two
	// racing writes the SAME old key — one blob deleted twice, the other leaked
	// permanently (no row, sweep, or reconciliation ever names it again); FOR
	// UPDATE makes the second writer wait and re-read the first writer's key.
	// And against ORDERING: the lock must sit in FROM, where it is evaluated as
	// the update's join input — a `WITH old AS (SELECT … FOR UPDATE)` referenced
	// from RETURNING runs AFTER the same command updated the row, and Postgres
	// skips locking a self-updated tuple, so `old` came back empty every time
	// (the CI integration suite caught exactly that).
	// The subquery's columns are aliased so every unqualified name in RETURNING
	// still resolves to kg_node alone.
	row := s.db.QueryRow(ctx,
		`UPDATE kg_node SET portrait_blob_key = $3, updated_at = now()
		   FROM (SELECT id AS locked_id, portrait_blob_key AS old_key
		           FROM kg_node
		          WHERE id = $1 AND campaign_id = $2
		            FOR UPDATE) old
		  WHERE kg_node.id = old.locked_id
		 RETURNING `+kgNodeColumnsAspects(false)+`, old.old_key`,
		nodeID, campaignID, blobKey)

	var n KGNode
	var aspects []byte
	var oldKey string
	err := row.Scan(
		&n.ID, &n.CampaignID, &n.Type, &n.Name, &n.Body, &n.GMPrivate, &n.AgentID,
		&n.CreatedAt, &n.UpdatedAt, &n.PortraitBlobKey, &aspects, &oldKey,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return KGNode{}, "", ErrNotFound
	}
	if err != nil {
		return KGNode{}, "", fmt.Errorf("storage: set kg node portrait %s: %w", nodeID, err)
	}
	if err := decodeAspects(aspects, &n.Aspects); err != nil {
		return KGNode{}, "", err
	}
	return n, oldKey, nil
}

// NodePortrait returns a Node's portrait blob key (” when it has none) and its
// updated_at — exactly what the plain-HTTP portrait serve needs (#590): the key
// to fetch and the cache validator to serve with. Campaign-scoped; a Node
// outside the campaign is ErrNotFound.
func (s *Store) NodePortrait(ctx context.Context, campaignID, nodeID uuid.UUID) (string, time.Time, error) {
	var key string
	var updatedAt time.Time
	err := s.db.QueryRow(ctx,
		`SELECT portrait_blob_key, updated_at FROM kg_node
		  WHERE id = $1 AND campaign_id = $2`, nodeID, campaignID).Scan(&key, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, ErrNotFound
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("storage: kg node portrait %s: %w", nodeID, err)
	}
	return key, updatedAt, nil
}

// ListCampaignPortraitKeys returns every non-empty portrait blob key in a
// Campaign — the campaign hard delete's sweep input (#590), mirroring
// ListCampaignMapBlobKeys: captured BEFORE the delete, because the row cascade
// removes the only records that name the keys (ADR-0048).
func (s *Store) ListCampaignPortraitKeys(ctx context.Context, campaignID uuid.UUID) ([]string, error) {
	rows, err := s.db.Query(ctx,
		`SELECT portrait_blob_key FROM kg_node
		  WHERE campaign_id = $1 AND portrait_blob_key <> ''`, campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: list campaign portrait keys %s: %w", campaignID, err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("storage: scan campaign portrait key: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list campaign portrait keys %s: %w", campaignID, err)
	}
	return keys, nil
}

// PortraitSeedContext returns the PUBLIC material a generated portrait's prompt
// may be seeded from (#590): the Node itself with its public Aspects only.
//
// Public-only, filtered in the QUERY, on the same seam-not-call-site principle
// MapSeedContext follows, and for the same reason: a portrait is an artefact
// the GM shows the table, so seeding its prompt from gm_private prose or facts
// launders a secret into a picture that cannot be filtered afterwards.
//
// A gm_private Node yields ErrNotFound — a secret character has no public
// depiction to generate. The GM can still upload a portrait for one by hand.
func (s *Store) PortraitSeedContext(ctx context.Context, campaignID, nodeID uuid.UUID) (KGNode, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+kgNodeColumnsAspects(true)+`
		   FROM kg_node
		  WHERE id = $1 AND campaign_id = $2 AND NOT gm_private`, nodeID, campaignID)
	node, err := scanKGNodeAspects(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return KGNode{}, ErrNotFound
	}
	if err != nil {
		return KGNode{}, fmt.Errorf("storage: portrait seed context %s: %w", nodeID, err)
	}
	return node, nil
}
