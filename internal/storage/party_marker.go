package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Party Marker persistence (#540, ADR-0060): where the party currently is, per
// VOICE SESSION.
//
// Session-scoped rather than campaign-scoped for the same reason every voice
// event is (ADR-0057): the Voice Instance pool is shared, so two concurrent
// sessions must never see each other's position. A campaign-level column would be
// exactly that leak.

// PartyMarker is a Voice Session's current position. MapID zero means no marker is
// set — the state every session starts in.
//
// A marker is either AT a Pin (PinID set) or at a free position between pins
// (X/Y set): the party crossing the moor is as real a position as the party in the
// tavern, and forcing a Pin for it would mean inventing wiki entries for empty
// ground.
type PartyMarker struct {
	MapID   uuid.NullUUID
	PinID   uuid.NullUUID
	X, Y    *float64
	MapName string
	// PinLabel and PinNodeID describe the Pin when the marker is at one.
	PinLabel     string
	PinNodeID    uuid.NullUUID
	MapGMPrivate bool
	PinHidden    bool
}

// Set reports whether a marker is placed at all.
func (m PartyMarker) Set() bool { return m.MapID.Valid }

// SetPartyMarker places (or clears) a Voice Session's marker. A zero mapID clears
// it entirely; a set mapID with no pin and no coordinates means "somewhere on this
// map", which is a legitimate answer while the GM is still deciding.
//
// The write is scoped to (id, campaign_id): a session from another Campaign
// matches nothing and yields ErrNotFound.
func (s *Store) SetPartyMarker(ctx context.Context, campaignID, sessionID uuid.UUID, mapID, pinID uuid.NullUUID, x, y *float64) error {
	tag, err := s.db.Exec(ctx,
		`UPDATE voice_sessions
		    SET current_map_id = $3, current_pin_id = $4, current_x = $5, current_y = $6
		  WHERE id = $1 AND campaign_id = $2`,
		sessionID, campaignID, mapID, pinID, x, y)
	if err != nil {
		return fmt.Errorf("storage: set party marker for session %s: %w", sessionID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetPartyMarker reads a Voice Session's marker, joined to the Map and Pin it
// names so a caller can render or describe it without further reads.
//
// A marker whose Map or Pin has been deleted reads as UNSET rather than dangling:
// the FKs are ON DELETE SET NULL, so the row degrades to "no marker" on its own.
func (s *Store) GetPartyMarker(ctx context.Context, campaignID, sessionID uuid.UUID) (PartyMarker, error) {
	row := s.db.QueryRow(ctx,
		`SELECT v.current_map_id, v.current_pin_id, v.current_x, v.current_y,
		        COALESCE(m.name, ''), COALESCE(m.gm_private, false),
		        COALESCE(NULLIF(p.label_override, ''), n.name, ''),
		        p.node_id, COALESCE(p.gm_private, false) OR COALESCE(n.gm_private, false)
		   FROM voice_sessions v
		   LEFT JOIN campaign_map m ON m.id = v.current_map_id
		   LEFT JOIN map_pin p      ON p.id = v.current_pin_id
		   LEFT JOIN kg_node n      ON n.id = p.node_id
		  WHERE v.id = $1 AND v.campaign_id = $2`, sessionID, campaignID)

	var pm PartyMarker
	err := row.Scan(
		&pm.MapID, &pm.PinID, &pm.X, &pm.Y,
		&pm.MapName, &pm.MapGMPrivate, &pm.PinLabel, &pm.PinNodeID, &pm.PinHidden,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PartyMarker{}, ErrNotFound
	}
	if err != nil {
		return PartyMarker{}, fmt.Errorf("storage: get party marker for session %s: %w", sessionID, err)
	}
	return pm, nil
}

// ResolveMapByName finds a Map in a Campaign by case-insensitive name — what the
// /where slash command resolves its argument through. Exactly one match is
// required: zero or many are refused so the GM fixes the ambiguity rather than the
// party being silently teleported to whichever row sorted first.
func (s *Store) ResolveMapByName(ctx context.Context, campaignID uuid.UUID, name string) (CampaignMap, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+campaignMapColumns+`
		   FROM campaign_map
		  WHERE campaign_id = $1 AND lower(name) = lower(btrim($2))`, campaignID, name)
	if err != nil {
		return CampaignMap{}, fmt.Errorf("storage: resolve map by name: %w", err)
	}
	defer rows.Close()

	var found []CampaignMap
	for rows.Next() {
		m, err := scanCampaignMap(rows)
		if err != nil {
			return CampaignMap{}, fmt.Errorf("storage: scan resolved map: %w", err)
		}
		found = append(found, m)
	}
	if err := rows.Err(); err != nil {
		return CampaignMap{}, fmt.Errorf("storage: resolve map by name: %w", err)
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return CampaignMap{}, ErrNotFound
	default:
		return CampaignMap{}, ErrConflict
	}
}

// ResolvePinByLabel finds a Pin on a Map by its displayed label (its override, or
// its Node's name), case-insensitively. Same one-match rule as ResolveMapByName,
// for the same reason.
func (s *Store) ResolvePinByLabel(ctx context.Context, campaignID, mapID uuid.UUID, label string) (MapPin, error) {
	rows, err := s.db.Query(ctx,
		`SELECT `+mapPinJoinColumns+`
		   FROM map_pin p JOIN kg_node n ON n.id = p.node_id
		  WHERE p.map_id = $1 AND p.campaign_id = $2
		    AND lower(COALESCE(NULLIF(p.label_override, ''), n.name)) = lower(btrim($3))`,
		mapID, campaignID, label)
	if err != nil {
		return MapPin{}, fmt.Errorf("storage: resolve pin by label: %w", err)
	}
	defer rows.Close()

	var found []MapPin
	for rows.Next() {
		p, err := scanMapPin(rows)
		if err != nil {
			return MapPin{}, fmt.Errorf("storage: scan resolved pin: %w", err)
		}
		found = append(found, p)
	}
	if err := rows.Err(); err != nil {
		return MapPin{}, fmt.Errorf("storage: resolve pin by label: %w", err)
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return MapPin{}, ErrNotFound
	default:
		return MapPin{}, ErrConflict
	}
}

// PinsNear returns the Pins within `radius` (in normalized map units) of a point
// on a Map, nearest first, excluding any Pin at the exact origin — the spatial
// Tool's "what is around us" read (#539).
//
// publicOnly applies the same composed visibility as [Store.ListPlayerPins]: a Pin
// is excluded when it or its Node is gm_private. Every PROMPT-facing caller passes
// true, because a spatial answer that names a GM secret leaks it just as surely as
// a fact would.
//
// Distance is Euclidean in normalized space, so it is an aspect-ratio-agnostic
// approximation of real proximity — good enough for "what is near us", and honest
// about not being a survey.
func (s *Store) PinsNear(ctx context.Context, campaignID, mapID uuid.UUID, x, y, radius float64, publicOnly bool, limit int) ([]MapPin, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("storage: pins near: limit must be > 0, got %d", limit)
	}
	privacy := ""
	if publicOnly {
		privacy = " AND NOT p.gm_private AND NOT n.gm_private"
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+mapPinJoinColumns+`
		   FROM map_pin p JOIN kg_node n ON n.id = p.node_id
		  WHERE p.map_id = $1 AND p.campaign_id = $2`+privacy+`
		    AND sqrt(power(p.x - $3, 2) + power(p.y - $4, 2)) <= $5
		  ORDER BY sqrt(power(p.x - $3, 2) + power(p.y - $4, 2)), p.id
		  LIMIT $6`, mapID, campaignID, x, y, radius, limit)
	if err != nil {
		return nil, fmt.Errorf("storage: pins near on map %s: %w", mapID, err)
	}
	defer rows.Close()

	var out []MapPin
	for rows.Next() {
		p, err := scanMapPin(rows)
		if err != nil {
			return nil, fmt.Errorf("storage: scan nearby pin: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: pins near on map %s: %w", mapID, err)
	}
	return out, nil
}
