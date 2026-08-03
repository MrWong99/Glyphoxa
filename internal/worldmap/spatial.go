package worldmap

import (
	"context"
	"fmt"
	"math"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/kgvocab"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
)

// The spatial Tool adapter (#539, ADR-0060): the storage side of locate_entity and
// whats_nearby.
//
// Every read here filters gm_private Maps, Pins and Nodes IN THE QUERY, on the same
// seam-not-call-site principle kgfacts.PromptKG follows — what these Tools return
// is spoken aloud at the table, so a private Pin surfacing here is a secret told.

// SpatialStore is the storage surface the adapter needs; *storage.Store satisfies
// it. Note what is absent: there is no write anywhere on it.
type SpatialStore interface {
	ListPlayerMaps(ctx context.Context, campaignID uuid.UUID) ([]storage.CampaignMap, error)
	ListPlayerPins(ctx context.Context, campaignID, mapID uuid.UUID) ([]storage.MapPin, error)
	NodePins(ctx context.Context, campaignID, nodeID uuid.UUID) ([]storage.MapPin, error)
	PinsNear(ctx context.Context, campaignID, mapID uuid.UUID, x, y, radius float64, publicOnly bool, limit int) ([]storage.MapPin, error)
	GetPartyMarker(ctx context.Context, campaignID, sessionID uuid.UUID) (storage.PartyMarker, error)
	SearchPublicNodes(ctx context.Context, campaignID uuid.UUID, query string, limit int) ([]storage.KGNode, error)
	AgentLinkedNode(ctx context.Context, agentID uuid.UUID) (storage.KGNode, bool, error)
}

// SpatialAdapter implements [tool.SpatialReader] over storage. The Campaign always
// comes from the turn's live session, never from LLM arguments, so a spatial query
// can never cross Campaigns (ADR-0029).
type SpatialAdapter struct{ store SpatialStore }

// NewSpatialAdapter builds the adapter. A nil store is a wiring bug, not a runtime
// condition.
func NewSpatialAdapter(store SpatialStore) *SpatialAdapter {
	if store == nil {
		panic("worldmap: NewSpatialAdapter: nil store")
	}
	return &SpatialAdapter{store: store}
}

// ErrNoActiveSession is returned when a spatial Tool runs outside a live turn.
var ErrNoActiveSession = fmt.Errorf("worldmap: no active voice session")

// Locate implements [tool.SpatialReader]. It resolves the name through the
// PROMPT-FACING node search (gm_private excluded before the limit), then reports
// every Map that node is publicly pinned on.
//
// Resolving through the public search rather than an exact match is deliberate:
// an NPC asked about "the anchor" should find "The Rusty Anchor", and the search
// is already tuned for that. A gm_private entry simply never resolves.
func (a *SpatialAdapter) Locate(ctx context.Context, _ string, name string) ([]tool.Place, error) {
	id, ok := session.FromContext(ctx)
	if !ok {
		return nil, ErrNoActiveSession
	}

	nodes, err := a.store.SearchPublicNodes(ctx, id.CampaignID, name, 1)
	if err != nil {
		return nil, fmt.Errorf("worldmap: locate %q: %w", name, err)
	}
	if len(nodes) == 0 {
		return nil, nil
	}
	node := nodes[0]

	pins, err := a.store.NodePins(ctx, id.CampaignID, node.ID)
	if err != nil {
		return nil, fmt.Errorf("worldmap: locate %q: pins: %w", name, err)
	}
	// NodePins is the GM-facing read (it backs the entry editor too), so the
	// visibility filter is applied here — and the Map's own privacy with it, since
	// naming a secret map is as much a leak as naming a secret pin.
	visible, err := a.publicMapNames(ctx, id.CampaignID)
	if err != nil {
		return nil, err
	}

	var out []tool.Place
	for _, p := range pins {
		if p.Hidden() {
			continue
		}
		mapName, ok := visible[p.MapID]
		if !ok {
			continue
		}
		out = append(out, tool.Place{
			Name:    p.Label(),
			Kind:    kgvocab.NodeTypeLabel(string(p.NodeType)),
			MapName: mapName,
		})
	}
	return out, nil
}

// Nearby implements [tool.SpatialReader]. The origin is the Party Marker when the
// GM has set one, else the calling Agent's OWN pinned Node — an NPC that knows
// where it stands can answer "what is around" even before the party is placed.
//
// With neither, there is no origin and the honest answer is nothing.
func (a *SpatialAdapter) Nearby(ctx context.Context, agentID string, radius float64, limit int) ([]tool.Place, error) {
	id, ok := session.FromContext(ctx)
	if !ok {
		return nil, ErrNoActiveSession
	}

	mapID, x, y, ok, err := a.origin(ctx, id.CampaignID, id.SessionID, agentID)
	if err != nil || !ok {
		return nil, err
	}

	// publicOnly=true is not optional: what this returns is spoken at the table.
	pins, err := a.store.PinsNear(ctx, id.CampaignID, mapID, x, y, radius, true, limit+1)
	if err != nil {
		return nil, fmt.Errorf("worldmap: nearby: %w", err)
	}
	visible, err := a.publicMapNames(ctx, id.CampaignID)
	if err != nil {
		return nil, err
	}
	mapName, mapVisible := visible[mapID]
	if !mapVisible {
		// The party is on a map the table does not know about; saying what is on it
		// would leak the map itself.
		return nil, nil
	}

	out := make([]tool.Place, 0, limit)
	for _, p := range pins {
		d := math.Hypot(p.X-x, p.Y-y)
		// Skip whatever sits exactly at the origin — "you are near yourself" is noise.
		if d == 0 {
			continue
		}
		if len(out) >= limit {
			break
		}
		out = append(out, tool.Place{
			Name:     p.Label(),
			Kind:     kgvocab.NodeTypeLabel(string(p.NodeType)),
			MapName:  mapName,
			Distance: d,
		})
	}
	return out, nil
}

// origin resolves the point a "what is nearby" query measures from.
func (a *SpatialAdapter) origin(ctx context.Context, campaignID, sessionID uuid.UUID, agentID string) (mapID uuid.UUID, x, y float64, ok bool, err error) {
	marker, err := a.store.GetPartyMarker(ctx, campaignID, sessionID)
	if err != nil {
		return uuid.Nil, 0, 0, false, fmt.Errorf("worldmap: nearby: marker: %w", err)
	}
	if marker.Set() && !marker.MapGMPrivate {
		mx, my := 0.5, 0.5 // "somewhere on this map" when the GM set no finer position
		if marker.X != nil && marker.Y != nil {
			mx, my = *marker.X, *marker.Y
		}
		if marker.PinID.Valid {
			// A pin's own coordinates are the better origin when the marker is at one.
			pins, perr := a.store.ListPlayerPins(ctx, campaignID, marker.MapID.UUID)
			if perr != nil {
				return uuid.Nil, 0, 0, false, fmt.Errorf("worldmap: nearby: pins: %w", perr)
			}
			for _, p := range pins {
				if p.ID == marker.PinID.UUID {
					mx, my = p.X, p.Y
					break
				}
			}
		}
		return marker.MapID.UUID, mx, my, true, nil
	}

	// No marker: fall back to the calling Agent's own pinned Node.
	aid, perr := uuid.Parse(agentID)
	if perr != nil || aid == uuid.Nil {
		return uuid.Nil, 0, 0, false, nil
	}
	own, linked, err := a.store.AgentLinkedNode(ctx, aid)
	if err != nil {
		return uuid.Nil, 0, 0, false, fmt.Errorf("worldmap: nearby: own node: %w", err)
	}
	if !linked {
		return uuid.Nil, 0, 0, false, nil
	}
	pins, err := a.store.NodePins(ctx, campaignID, own.ID)
	if err != nil {
		return uuid.Nil, 0, 0, false, fmt.Errorf("worldmap: nearby: own pins: %w", err)
	}
	for _, p := range pins {
		if p.Hidden() {
			continue
		}
		return p.MapID, p.X, p.Y, true, nil
	}
	return uuid.Nil, 0, 0, false, nil
}

// publicMapNames is the id→name index of the Maps the table may know about.
func (a *SpatialAdapter) publicMapNames(ctx context.Context, campaignID uuid.UUID) (map[uuid.UUID]string, error) {
	maps, err := a.store.ListPlayerMaps(ctx, campaignID)
	if err != nil {
		return nil, fmt.Errorf("worldmap: list maps: %w", err)
	}
	out := make(map[uuid.UUID]string, len(maps))
	for _, m := range maps {
		out[m.ID] = m.Name
	}
	return out, nil
}

// Compile-time assertion that the adapter satisfies the Tool seam.
var _ tool.SpatialReader = (*SpatialAdapter)(nil)
