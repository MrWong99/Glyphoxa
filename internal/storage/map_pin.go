package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Pin persistence (#538, ADR-0060). A Pin is a normalized position on a Map for a
// Knowledge Graph Node — never a content record of its own.

// Postgres SQLSTATEs the Pin writes translate into domain errors.
const (
	pgUniqueViolation     = "23505"
	pgForeignKeyViolation = "23503"
	pgCheckViolation      = "23514"
)

// ErrInvalidPin is returned when a Pin's coordinates fall outside the normalized
// 0..1 range the schema enforces.
var ErrInvalidPin = errors.New("storage: pin coordinates must be within 0..1")

// NewMapPin is the input to CreatePin. Coordinates are normalized 0..1 and the DB
// CHECK enforces it, so an out-of-range pin is refused rather than stored and
// silently clamped at render time.
type NewMapPin struct {
	MapID         uuid.UUID
	CampaignID    uuid.UUID
	NodeID        uuid.UUID
	X, Y          float64
	LabelOverride string
	GMPrivate     bool
}

// mapPinJoinColumns selects a Pin plus the joined Node fields the Maps tab needs
// to draw it — its name, its type (for the palette), and its privacy (which the
// Pin inherits). The join is inner: the composite FK guarantees the Node exists in
// the same Campaign.
const mapPinJoinColumns = `
	p.id, p.map_id, p.campaign_id, p.node_id, p.x, p.y, p.label_override, p.gm_private,
	n.name, n.node_type, n.gm_private`

func scanMapPin(row pgx.Row) (MapPin, error) {
	var p MapPin
	err := row.Scan(
		&p.ID, &p.MapID, &p.CampaignID, &p.NodeID, &p.X, &p.Y, &p.LabelOverride, &p.GMPrivate,
		&p.NodeName, &p.NodeType, &p.NodeGMPrivate,
	)
	return p, err
}

// CreatePin pins a Node onto a Map. A duplicate (map, node) yields ErrConflict —
// one Pin per Node per Map, though the same Node may be pinned on several Maps
// (the city map AND the tavern floor plan). A Node or Map from another Campaign
// is refused by the composite FKs and yields ErrNotFound.
func (s *Store) CreatePin(ctx context.Context, n NewMapPin) (MapPin, error) {
	var id uuid.UUID
	err := s.db.QueryRow(ctx,
		`INSERT INTO map_pin (map_id, campaign_id, node_id, x, y, label_override, gm_private)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id`,
		n.MapID, n.CampaignID, n.NodeID, n.X, n.Y, n.LabelOverride, n.GMPrivate).Scan(&id)
	if code, ok := pgErrCode(err); ok {
		switch code {
		case pgUniqueViolation:
			// One Pin per Node per Map — the GM is re-pinning something already here.
			return MapPin{}, ErrConflict
		case pgForeignKeyViolation:
			// A Map or Node that does not exist IN THIS CAMPAIGN: the composite FKs are
			// what make a cross-campaign pin impossible without a trigger (ADR-0060).
			return MapPin{}, ErrNotFound
		case pgCheckViolation:
			// Coordinates outside 0..1 — refused rather than stored and silently clamped
			// at render time.
			return MapPin{}, ErrInvalidPin
		}
	}
	if err != nil {
		return MapPin{}, fmt.Errorf("storage: create map pin: %w", err)
	}
	return s.GetPin(ctx, n.CampaignID, id)
}

// GetPin loads one Pin with its joined Node fields, scoped to its Campaign.
func (s *Store) GetPin(ctx context.Context, campaignID, id uuid.UUID) (MapPin, error) {
	row := s.db.QueryRow(ctx,
		`SELECT `+mapPinJoinColumns+`
		   FROM map_pin p JOIN kg_node n ON n.id = p.node_id
		  WHERE p.id = $1 AND p.campaign_id = $2`, id, campaignID)
	p, err := scanMapPin(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return MapPin{}, ErrNotFound
	}
	if err != nil {
		return MapPin{}, fmt.Errorf("storage: get map pin %s: %w", id, err)
	}
	return p, nil
}

// ListPins returns a Map's Pins in a stable order. GM-facing: every Pin is
// included, whatever its own or its Node's privacy. [Store.ListPlayerPins] is the
// player-tier read.
func (s *Store) ListPins(ctx context.Context, campaignID, mapID uuid.UUID) ([]MapPin, error) {
	return s.listPins(ctx, campaignID, mapID, false)
}

// ListPlayerPins is the player-tier read (ADR-0056): a Pin is excluded IN THE
// QUERY when it is gm_private OR its Node is. A position that points at a GM
// secret is itself a leak — knowing "something is here" and what it is called is
// most of the secret — so the Node's flag propagates, mirroring ADR-0008's rule
// that gm_private filtering applies to expansion and not only to direct reads.
func (s *Store) ListPlayerPins(ctx context.Context, campaignID, mapID uuid.UUID) ([]MapPin, error) {
	return s.listPins(ctx, campaignID, mapID, true)
}

func (s *Store) listPins(ctx context.Context, campaignID, mapID uuid.UUID, publicOnly bool) ([]MapPin, error) {
	privacy := ""
	if publicOnly {
		privacy = " AND NOT p.gm_private AND NOT n.gm_private"
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+mapPinJoinColumns+`
		   FROM map_pin p JOIN kg_node n ON n.id = p.node_id
		  WHERE p.map_id = $1 AND p.campaign_id = $2`+privacy+`
		  ORDER BY lower(COALESCE(NULLIF(p.label_override, ''), n.name)), p.id`, mapID, campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: list map pins for map %s: %w", mapID, err)
	}
	defer rows.Close()

	var out []MapPin
	for rows.Next() {
		p, err := scanMapPin(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan map pin: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list map pins for map %s: %w", mapID, err)
	}
	return out, nil
}

// MapPinUpdate moves or relabels a Pin. Position and presentation are one write
// because dragging a pin and renaming it are the same act to the GM: adjusting
// what this Map says about that entry.
type MapPinUpdate struct {
	ID            uuid.UUID
	CampaignID    uuid.UUID
	X, Y          float64
	LabelOverride string
	GMPrivate     bool
}

// UpdatePin saves a Pin's position and presentation, scoped to its Campaign
// (#342). Out-of-range coordinates are refused by the DB CHECK.
func (s *Store) UpdatePin(ctx context.Context, u MapPinUpdate) (MapPin, error) {
	tag, err := s.db.Exec(ctx,
		`UPDATE map_pin
		    SET x = $3, y = $4, label_override = $5, gm_private = $6, updated_at = now()
		  WHERE id = $1 AND campaign_id = $2`,
		u.ID, u.CampaignID, u.X, u.Y, u.LabelOverride, u.GMPrivate)
	if code, ok := pgErrCode(err); ok && code == pgCheckViolation {
		return MapPin{}, ErrInvalidPin
	}
	if err != nil {
		return MapPin{}, fmt.Errorf("storage: update map pin %s: %w", u.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return MapPin{}, ErrNotFound
	}
	return s.GetPin(ctx, u.CampaignID, u.ID)
}

// DeletePin unpins a Node from a Map, scoped to its Campaign.
func (s *Store) DeletePin(ctx context.Context, campaignID, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM map_pin WHERE id = $1 AND campaign_id = $2`, id, campaignID)
	if err != nil {
		return fmt.Errorf("storage: delete map pin %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UnpinnedNodes returns the Campaign's Nodes that are NOT pinned on a given Map,
// restricted to the types worth placing (#538): Locations, NPCs and Items are
// things that occupy space; Factions, Plot threads and Notes are not.
//
// It backs the Maps tab's "unpinned entries" tray, which is what makes placing the
// world a drag rather than a form.
func (s *Store) UnpinnedNodes(ctx context.Context, campaignID, mapID uuid.UUID) ([]KGNode, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+kgNodeColumns+`
		   FROM kg_node
		  WHERE campaign_id = $1
		    AND node_type IN ('location', 'npc', 'item', 'character')
		    AND NOT EXISTS (
		          SELECT 1 FROM map_pin p WHERE p.node_id = kg_node.id AND p.map_id = $2)
		  ORDER BY node_type, lower(name), id`, campaignID, mapID)
	if err != nil {
		return nil, fmt.Errorf("storage: list unpinned nodes for map %s: %w", mapID, err)
	}
	defer rows.Close()

	var out []KGNode
	for rows.Next() {
		n, err := scanKGNode(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan unpinned node: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list unpinned nodes for map %s: %w", mapID, err)
	}
	return out, nil
}

// NodePins returns every Pin for one Node across all Maps in the Campaign — "where
// is this?", answered from the entry rather than from a Map. It is what makes a
// Node's position discoverable without opening every Map in turn.
//
// GM-facing: gm_private Pins, Pins on gm_private Maps and Pins on gm_private Nodes
// are ALL included. Anything prompt- or player-facing must use
// [Store.PlayerNodePins] — the two are separate functions rather than one with a
// flag the caller might forget, because forgetting here means a Character NPC
// telling the table where the GM's secret is.
func (s *Store) NodePins(ctx context.Context, campaignID, nodeID uuid.UUID) ([]MapPin, error) {
	return s.nodePins(ctx, campaignID, nodeID, false)
}

// PlayerNodePins is [Store.NodePins] with the privacy filter pushed into SQL: a
// gm_private Pin, a Pin whose Node is gm_private, and a Pin on a gm_private Map
// are all invisible. It is the read the prompt-facing spatial Tools consult
// (#539), so a GM secret cannot reach an NPC's answer even by mistake.
func (s *Store) PlayerNodePins(ctx context.Context, campaignID, nodeID uuid.UUID) ([]MapPin, error) {
	return s.nodePins(ctx, campaignID, nodeID, true)
}

func (s *Store) nodePins(ctx context.Context, campaignID, nodeID uuid.UUID, publicOnly bool) ([]MapPin, error) {
	privacy := ""
	if publicOnly {
		// The Map join is only needed for the filter, so it is added only with it.
		privacy = ` AND NOT p.gm_private AND NOT n.gm_private
		            AND EXISTS (SELECT 1 FROM campaign_map m
		                         WHERE m.id = p.map_id AND NOT m.gm_private)`
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+mapPinJoinColumns+`
		   FROM map_pin p JOIN kg_node n ON n.id = p.node_id
		  WHERE p.node_id = $1 AND p.campaign_id = $2`+privacy+`
		  ORDER BY p.map_id, p.id`, nodeID, campaignID)
	if err != nil {
		return nil, fmt.Errorf("storage: list pins for node %s: %w", nodeID, err)
	}
	defer rows.Close()

	var out []MapPin
	for rows.Next() {
		p, err := scanMapPin(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan node pin: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list pins for node %s: %w", nodeID, err)
	}
	return out, nil
}
