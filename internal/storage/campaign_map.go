package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Maps and Pins persistence (#538, ADR-0060): world Maps as first-class Campaign
// data, with Knowledge Graph Nodes pinned onto them.
//
// Two rules shape everything here. A Pin ALWAYS references a KG Node — Pins are a
// position for something the wiki already knows, not a parallel content model —
// and coordinates are NORMALIZED 0..1, so a re-uploaded or rescaled image keeps
// every pin.
//
// The image lives in the blob seam (ADR-0048); the row carries only its key, and
// deletion goes through the seam rather than an FK cascade.

// CampaignMap is one persisted Map. BlobKey addresses the image in the blob seam;
// WidthPx/HeightPx are the source dimensions, kept for aspect-ratio rendering and
// deliberately NOT used for coordinate maths.
type CampaignMap struct {
	ID         uuid.UUID
	CampaignID uuid.UUID
	Name       string
	BlobKey    string
	WidthPx    int
	HeightPx   int
	// ParentMapID is the Map this one sits inside; AnchorNodeID is the Location
	// Node this Map DEPICTS. Together they give the continent → region → city →
	// building hierarchy GMs actually draw, and let a pin on one map open the map
	// beneath it.
	ParentMapID  uuid.NullUUID
	AnchorNodeID uuid.NullUUID
	GMPrivate    bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NewCampaignMap is the input to CreateMap. The blob is written FIRST and its key
// passed here, so a row never references bytes that do not exist.
type NewCampaignMap struct {
	CampaignID   uuid.UUID
	Name         string
	BlobKey      string
	WidthPx      int
	HeightPx     int
	ParentMapID  uuid.NullUUID
	AnchorNodeID uuid.NullUUID
	GMPrivate    bool
}

// MapPin is one persisted Pin: a normalized position on a Map for a KG Node.
// NodeName and NodeType are joined from kg_node so the Maps tab can draw a pin
// without a second read; LabelOverride replaces the Node's name on this Map only
// ("the back door" for an entry called "Rusty Anchor cellar").
type MapPin struct {
	ID            uuid.UUID
	MapID         uuid.UUID
	CampaignID    uuid.UUID
	NodeID        uuid.UUID
	X, Y          float64
	LabelOverride string
	GMPrivate     bool
	NodeName      string
	NodeType      KGNodeType
	NodeGMPrivate bool
}

// Label is what the Pin shows: its override when set, else the Node's name.
func (p MapPin) Label() string {
	if p.LabelOverride != "" {
		return p.LabelOverride
	}
	return p.NodeName
}

// Hidden reports whether a Pin must be withheld from a player-tier view
// (ADR-0056). A Pin is hidden by its OWN flag or by its Node's: a position that
// points at a GM secret is itself a leak, which mirrors ADR-0008's rule that
// gm_private filtering applies to neighbour expansion and not only to direct
// reads.
func (p MapPin) Hidden() bool { return p.GMPrivate || p.NodeGMPrivate }

const campaignMapColumns = `
	id, campaign_id, name, blob_key, width_px, height_px,
	parent_map_id, anchor_node_id, gm_private, created_at, updated_at`

func scanCampaignMap(row pgx.Row) (CampaignMap, error) {
	var m CampaignMap
	err := row.Scan(
		&m.ID, &m.CampaignID, &m.Name, &m.BlobKey, &m.WidthPx, &m.HeightPx,
		&m.ParentMapID, &m.AnchorNodeID, &m.GMPrivate, &m.CreatedAt, &m.UpdatedAt,
	)
	return m, err
}

// CreateMap inserts a Map and returns the persisted row. The caller writes the
// blob first: a row that references missing bytes is worse than an orphaned blob,
// which the seam's reconciliation can find.
func (s *Store) CreateMap(ctx context.Context, m NewCampaignMap) (CampaignMap, error) {
	row := s.db.QueryRow(ctx,
		`INSERT INTO campaign_map
		     (campaign_id, name, blob_key, width_px, height_px, parent_map_id, anchor_node_id, gm_private)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 RETURNING `+campaignMapColumns,
		m.CampaignID, m.Name, m.BlobKey, m.WidthPx, m.HeightPx,
		m.ParentMapID, m.AnchorNodeID, m.GMPrivate)
	created, err := scanCampaignMap(row)
	if err != nil {
		return CampaignMap{}, fmt.Errorf("storage: create campaign map: %w", err)
	}
	return created, nil
}

// ListMaps returns a Campaign's Maps in a stable display order (name, id).
// GM-facing: gm_private Maps are INCLUDED. The player-tier read is
// [Store.ListPlayerMaps].
func (s *Store) ListMaps(ctx context.Context, campaignID uuid.UUID) ([]CampaignMap, error) {
	return s.listMaps(ctx, campaignID, false)
}

// ListPlayerMaps is the player-tier read (ADR-0056): gm_private Maps are excluded
// in the QUERY, so they are absent from the response rather than merely hidden by
// the UI.
func (s *Store) ListPlayerMaps(ctx context.Context, campaignID uuid.UUID) ([]CampaignMap, error) {
	return s.listMaps(ctx, campaignID, true)
}

func (s *Store) listMaps(ctx context.Context, campaignID uuid.UUID, publicOnly bool) ([]CampaignMap, error) {
	privacy := ""
	if publicOnly {
		privacy = " AND NOT gm_private"
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+campaignMapColumns+`
		   FROM campaign_map
		  WHERE campaign_id = $1`+privacy+`
		  ORDER BY lower(name), id`, campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: list campaign maps for %s: %w", campaignID, err)
	}
	defer rows.Close()

	var out []CampaignMap
	for rows.Next() {
		m, err := scanCampaignMap(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan campaign map: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list campaign maps for %s: %w", campaignID, err)
	}
	return out, nil
}

// GetMap loads one Map scoped to its Campaign (#342). A Map in another Campaign
// yields ErrNotFound.
func (s *Store) GetMap(ctx context.Context, campaignID, id uuid.UUID) (CampaignMap, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+campaignMapColumns+` FROM campaign_map WHERE id = $1 AND campaign_id = $2`,
		id, campaignID)
	m, err := scanCampaignMap(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return CampaignMap{}, ErrNotFound
	}
	if err != nil {
		return CampaignMap{}, fmt.Errorf("storage: get campaign map %s: %w", id, err)
	}
	return m, nil
}

// CampaignMapUpdate is the Map editor's field set. blob_key and dimensions are
// updated separately by the re-upload path, which is what keeps a rescale from
// touching pins.
type CampaignMapUpdate struct {
	ID           uuid.UUID
	CampaignID   uuid.UUID
	Name         string
	ParentMapID  uuid.NullUUID
	AnchorNodeID uuid.NullUUID
	GMPrivate    bool
}

// UpdateMap saves a Map's editor fields, scoped to (id, campaign_id) (#342).
//
// A Map may not become its own parent; deeper cycles are refused by
// [Store.MapAncestors] at read time rather than by a recursive CHECK, since the
// hierarchy is shallow and a GM re-parenting into a loop is a mistake to report,
// not a constraint violation to crash on.
func (s *Store) UpdateMap(ctx context.Context, u CampaignMapUpdate) (CampaignMap, error) {
	if u.ParentMapID.Valid && u.ParentMapID.UUID == u.ID {
		return CampaignMap{}, fmt.Errorf("storage: update campaign map %s: %w", u.ID, ErrInvalidMapParent)
	}
	row := s.db.QueryRow(ctx,
		`UPDATE campaign_map
		    SET name = $3, parent_map_id = $4, anchor_node_id = $5, gm_private = $6, updated_at = now()
		  WHERE id = $1 AND campaign_id = $2
		 RETURNING `+campaignMapColumns,
		u.ID, u.CampaignID, u.Name, u.ParentMapID, u.AnchorNodeID, u.GMPrivate)
	m, err := scanCampaignMap(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return CampaignMap{}, ErrNotFound
	}
	if err != nil {
		return CampaignMap{}, fmt.Errorf("storage: update campaign map %s: %w", u.ID, err)
	}
	return m, nil
}

// ErrInvalidMapParent is returned when a re-parent would make a Map its own
// ancestor.
var ErrInvalidMapParent = errors.New("storage: a map cannot contain itself")

// SetMapImage repoints a Map at new image bytes (#538). Pins are untouched by
// design: coordinates are normalized, so a re-upload at a different resolution
// keeps every pin exactly where the GM put it — which is the whole reason they
// are normalized.
func (s *Store) SetMapImage(ctx context.Context, campaignID, id uuid.UUID, blobKey string, widthPx, heightPx int) (CampaignMap, error) {
	row := s.db.QueryRow(ctx,
		`UPDATE campaign_map
		    SET blob_key = $3, width_px = $4, height_px = $5, updated_at = now()
		  WHERE id = $1 AND campaign_id = $2
		 RETURNING `+campaignMapColumns,
		id, campaignID, blobKey, widthPx, heightPx)
	m, err := scanCampaignMap(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return CampaignMap{}, ErrNotFound
	}
	if err != nil {
		return CampaignMap{}, fmt.Errorf("storage: set map image %s: %w", id, err)
	}
	return m, nil
}

// DeleteMap removes a Map and returns the blob key the caller must then delete
// through the seam (ADR-0048: deletion goes through the seam, not FK cascade).
// Returning the key rather than deleting the blob here keeps storage free of a
// blob dependency, and makes the caller's obligation explicit.
//
// Child Maps are NOT cascaded: parent_map_id is ON DELETE SET NULL, so deleting a
// middle map lifts its children to the top level instead of silently taking a
// subtree with it.
func (s *Store) DeleteMap(ctx context.Context, campaignID, id uuid.UUID) (blobKey string, err error) {
	err = s.db.QueryRow(ctx,
		`DELETE FROM campaign_map WHERE id = $1 AND campaign_id = $2 RETURNING blob_key`,
		id, campaignID).Scan(&blobKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("storage: delete campaign map %s: %w", id, err)
	}
	return blobKey, nil
}

// MapAncestors returns a Map's ancestor chain, nearest parent first, for the
// breadcrumb. It is bounded: a cycle (which the schema permits, since a
// self-reference is only blocked at depth 1) terminates at maxMapDepth rather
// than looping, so a mis-parented Map degrades to a truncated breadcrumb instead
// of hanging the screen.
func (s *Store) MapAncestors(ctx context.Context, campaignID, id uuid.UUID) ([]CampaignMap, error) {
	var out []CampaignMap
	seen := map[uuid.UUID]bool{id: true}
	current, err := s.GetMap(ctx, campaignID, id)
	if err != nil {
		return nil, err
	}
	for range maxMapDepth {
		if !current.ParentMapID.Valid || seen[current.ParentMapID.UUID] {
			break
		}
		parent, err := s.GetMap(ctx, campaignID, current.ParentMapID.UUID)
		if errors.Is(err, ErrNotFound) {
			break
		}
		if err != nil {
			return nil, err
		}
		seen[parent.ID] = true
		out = append(out, parent)
		current = parent
	}
	return out, nil
}

// maxMapDepth bounds the breadcrumb walk. Continent → region → city → district →
// building is five; ten is generous and still finite.
const maxMapDepth = 10
