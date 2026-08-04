package rpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// campaignMaps is the Maps and Pins feature module (#538, ADR-0060): world Maps
// with Knowledge Graph Nodes pinned onto them.
//
// The image never travels through the read RPCs — it is written once here, to the
// blob seam, and fetched from the plain HTTP mount. A Map row is only ever created
// AFTER its bytes land, so a row never references bytes that do not exist. The
// reverse — a blob with no row — is merely invisible and finite, and each write
// path deletes its own orphan inline; there is no background reconciliation for
// map keys, so the inline delete is the cleanup, not a fast path in front of one.
type campaignMaps struct {
	store  campaignMapStore
	blobs  blob.Store
	active *activeCampaignSource
	// tenant resolves the request's Tenant, which the blob key is prefixed with
	// (ADR-0048 makes the tenant prefix mandatory).
	tenant func(ctx context.Context) (uuid.UUID, bool)
	// gen and suggest back the generated-map draft flow (#541). Both nil in a
	// composition with no image provider, which the handlers report rather than
	// panic on. They store NOTHING — see campaign_map_generate.go.
	gen     MapGenerator
	suggest MapPinSuggester
}

// campaignMapStore is the narrow Map/Pin surface the module needs; *storage.Store
// satisfies it. Every method is campaign-scoped (#342).
type campaignMapStore interface {
	CreateMap(ctx context.Context, m storage.NewCampaignMap) (storage.CampaignMap, error)
	ListMaps(ctx context.Context, campaignID uuid.UUID) ([]storage.CampaignMap, error)
	GetMap(ctx context.Context, campaignID, id uuid.UUID) (storage.CampaignMap, error)
	UpdateMap(ctx context.Context, u storage.CampaignMapUpdate) (storage.CampaignMap, error)
	SetMapImage(ctx context.Context, campaignID, id uuid.UUID, blobKey string, widthPx, heightPx int) (storage.CampaignMap, error)
	DeleteMap(ctx context.Context, campaignID, id uuid.UUID) (string, error)
	MapAncestors(ctx context.Context, campaignID, id uuid.UUID) ([]storage.CampaignMap, error)

	CreatePin(ctx context.Context, n storage.NewMapPin) (storage.MapPin, error)
	ListPins(ctx context.Context, campaignID, mapID uuid.UUID) ([]storage.MapPin, error)
	UpdatePin(ctx context.Context, u storage.MapPinUpdate) (storage.MapPin, error)
	DeletePin(ctx context.Context, campaignID, id uuid.UUID) error
	UnpinnedNodes(ctx context.Context, campaignID, mapID uuid.UUID) ([]storage.KGNode, error)
}

// ListMaps returns the active campaign's Maps. GM-facing: gm_private Maps are
// included, because every caller of this surface is the operator (ADR-0041).
func (s *campaignMaps) ListMaps(
	ctx context.Context,
	_ *connect.Request[managementv1.ListMapsRequest],
) (*connect.Response[managementv1.ListMapsResponse], error) {
	c, err := s.resolveCampaign(ctx, "ListMaps")
	if err != nil {
		return nil, err
	}
	maps, err := s.store.ListMaps(ctx, c.ID)
	if err != nil {
		slog.Default().Error("ListMaps: store list failed", "campaign_id", c.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	out := &managementv1.ListMapsResponse{Maps: make([]*managementv1.Map, 0, len(maps))}
	for _, m := range maps {
		out.Maps = append(out.Maps, toProtoMap(m))
	}
	return connect.NewResponse(out), nil
}

// GetMapView returns one Map with everything the Maps tab draws: its Pins, its
// ancestor breadcrumb, its direct children, and the entries not yet pinned on it.
// One call rather than five, because they are one screen.
func (s *campaignMaps) GetMapView(
	ctx context.Context,
	req *connect.Request[managementv1.GetMapViewRequest],
) (*connect.Response[managementv1.GetMapViewResponse], error) {
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid map id"))
	}
	c, err := s.resolveCampaign(ctx, "GetMapView")
	if err != nil {
		return nil, err
	}

	m, err := s.store.GetMap(ctx, c.ID, id)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("map not found"))
	}
	if err != nil {
		slog.Default().Error("GetMapView: load map failed", "map_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	pins, err := s.store.ListPins(ctx, c.ID, id)
	if err != nil {
		slog.Default().Error("GetMapView: list pins failed", "map_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	ancestors, err := s.store.MapAncestors(ctx, c.ID, id)
	if err != nil {
		slog.Default().Error("GetMapView: ancestors failed", "map_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	unpinned, err := s.store.UnpinnedNodes(ctx, c.ID, id)
	if err != nil {
		slog.Default().Error("GetMapView: unpinned failed", "map_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	// Children come from the campaign's Map list rather than a dedicated query: a
	// campaign has a handful of Maps, and the list read is already indexed.
	all, err := s.store.ListMaps(ctx, c.ID)
	if err != nil {
		slog.Default().Error("GetMapView: list maps failed", "campaign_id", c.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	out := &managementv1.GetMapViewResponse{Map: toProtoMap(m)}
	for _, p := range pins {
		out.Pins = append(out.Pins, toProtoPin(p))
	}
	for _, a := range ancestors {
		out.Breadcrumb = append(out.Breadcrumb, toProtoMap(a))
	}
	for _, child := range all {
		if child.ParentMapID.Valid && child.ParentMapID.UUID == id {
			out.Children = append(out.Children, toProtoMap(child))
		}
	}
	for _, n := range unpinned {
		out.Unpinned = append(out.Unpinned, toProtoNode(n))
	}
	return connect.NewResponse(out), nil
}

// maxMapNameRunes bounds a Map name, matching the KG name cap's spirit.
const maxMapNameRunes = 200

// CreateMap writes the image to the blob seam and then creates the row.
//
// Blob FIRST, row second: a row referencing missing bytes renders as a broken map
// with no way to fix it, whereas a blob with no row is invisible and reclaimable
// by the seam's reconciliation. When the two can disagree, disagree in the
// recoverable direction.
func (s *campaignMaps) CreateMap(
	ctx context.Context,
	req *connect.Request[managementv1.CreateMapRequest],
) (*connect.Response[managementv1.CreateMapResponse], error) {
	m := req.Msg
	name := strings.TrimSpace(m.GetName())
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name must not be empty"))
	}
	if len([]rune(name)) > maxMapNameRunes {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("name is too long (max %d characters)", maxMapNameRunes))
	}
	if err := validateImage(m.GetImageBytes(), m.GetContentType(), m.GetWidthPx(), m.GetHeightPx()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	if s.blobs == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("map images are unavailable in this mode"))
	}
	c, err := s.resolveCampaign(ctx, "CreateMap")
	if err != nil {
		return nil, err
	}
	tenantID, ok := s.tenantID(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no tenant"))
	}

	parent, err := optionalUUID(m.GetParentMapId(), "parent map id")
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	anchor, err := optionalUUID(m.GetAnchorNodeId(), "anchor entry id")
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	// The blob key needs the row's id, and the id is assigned by the insert — so the
	// key is built on a client-side uuid the insert then adopts.
	mapID := uuid.New()
	key, err := blob.Key(tenantID, "map", mapID, "image")
	if err != nil {
		slog.Default().Error("CreateMap: blob key", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if err := s.putImage(ctx, key, m.GetContentType(), m.GetImageBytes()); err != nil {
		return nil, err
	}

	created, err := s.store.CreateMap(ctx, storage.NewCampaignMap{
		CampaignID:   c.ID,
		Name:         name,
		BlobKey:      key,
		WidthPx:      int(m.GetWidthPx()),
		HeightPx:     int(m.GetHeightPx()),
		ParentMapID:  parent,
		AnchorNodeID: anchor,
		GMPrivate:    m.GetGmPrivate(),
	})
	if err != nil {
		// Drop the orphaned bytes here and now. There is no background reconciliation
		// that would find them later — the campaign sweep walks campaign_map rows, and
		// this insert produced none — so this best-effort delete IS the cleanup. It is
		// best-effort because the row failure is what the caller must hear about.
		if derr := s.blobs.Delete(ctx, key); derr != nil {
			slog.Default().Warn("CreateMap: could not drop orphaned blob", "key", key, "err", derr)
		}
		// A parent Map or anchor Node that is not in THIS campaign is the operator
		// naming something absent, not a server fault.
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound,
				errors.New("parent map or anchor entry not found in this campaign"))
		}
		slog.Default().Error("CreateMap: store create failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.CreateMapResponse{Map: toProtoMap(created)}), nil
}

// UpdateMap saves a Map's editor fields.
func (s *campaignMaps) UpdateMap(
	ctx context.Context,
	req *connect.Request[managementv1.UpdateMapRequest],
) (*connect.Response[managementv1.UpdateMapResponse], error) {
	m := req.Msg
	id, err := uuid.Parse(m.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid map id"))
	}
	name := strings.TrimSpace(m.GetName())
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name must not be empty"))
	}
	// The same cap CreateMap enforces. A bound applied only on the create path is a
	// bound the rename path removes.
	if len([]rune(name)) > maxMapNameRunes {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("name is too long (max %d characters)", maxMapNameRunes))
	}
	parent, err := optionalUUID(m.GetParentMapId(), "parent map id")
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	anchor, err := optionalUUID(m.GetAnchorNodeId(), "anchor entry id")
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	c, err := s.resolveCampaign(ctx, "UpdateMap")
	if err != nil {
		return nil, err
	}
	updated, err := s.store.UpdateMap(ctx, storage.CampaignMapUpdate{
		ID: id, CampaignID: c.ID, Name: name,
		ParentMapID: parent, AnchorNodeID: anchor, GMPrivate: m.GetGmPrivate(),
	})
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound,
			errors.New("map, parent map or anchor entry not found in this campaign"))
	case errors.Is(err, storage.ErrInvalidMapParent):
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a map cannot contain itself"))
	case err != nil:
		slog.Default().Error("UpdateMap: store update failed", "map_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.UpdateMapResponse{Map: toProtoMap(updated)}), nil
}

// ReplaceMapImage repoints a Map at new bytes and drops the old blob.
//
// Pins are deliberately untouched: coordinates are normalized, so a re-upload at a
// different resolution keeps every pin exactly where the GM put it. That is the
// whole reason they are normalized.
func (s *campaignMaps) ReplaceMapImage(
	ctx context.Context,
	req *connect.Request[managementv1.ReplaceMapImageRequest],
) (*connect.Response[managementv1.ReplaceMapImageResponse], error) {
	m := req.Msg
	id, err := uuid.Parse(m.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid map id"))
	}
	if err := validateImage(m.GetImageBytes(), m.GetContentType(), m.GetWidthPx(), m.GetHeightPx()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if s.blobs == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("map images are unavailable in this mode"))
	}
	c, err := s.resolveCampaign(ctx, "ReplaceMapImage")
	if err != nil {
		return nil, err
	}
	tenantID, ok := s.tenantID(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no tenant"))
	}

	existing, err := s.store.GetMap(ctx, c.ID, id)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("map not found"))
	}
	if err != nil {
		slog.Default().Error("ReplaceMapImage: load map failed", "map_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}

	// A NEW key per upload, not an overwrite: the old bytes stay readable until the
	// row points elsewhere, so an in-flight image request never sees a torn blob —
	// and it makes updated_at a sound cache validator.
	key, err := blob.Key(tenantID, "map", uuid.New(), "image")
	if err != nil {
		slog.Default().Error("ReplaceMapImage: blob key", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if err := s.putImage(ctx, key, m.GetContentType(), m.GetImageBytes()); err != nil {
		return nil, err
	}

	updated, err := s.store.SetMapImage(ctx, c.ID, id, key, int(m.GetWidthPx()), int(m.GetHeightPx()))
	if err != nil {
		if derr := s.blobs.Delete(ctx, key); derr != nil {
			slog.Default().Warn("ReplaceMapImage: could not drop orphaned blob", "key", key, "err", derr)
		}
		slog.Default().Error("ReplaceMapImage: store update failed", "map_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	// The row now points at the new bytes, so the old ones are unreachable.
	if derr := s.blobs.Delete(ctx, existing.BlobKey); derr != nil {
		slog.Default().Warn("ReplaceMapImage: could not drop superseded blob", "key", existing.BlobKey, "err", derr)
	}
	return connect.NewResponse(&managementv1.ReplaceMapImageResponse{Map: toProtoMap(updated)}), nil
}

// DeleteMap removes the row, then its image THROUGH THE SEAM (ADR-0048: deletion
// goes through the seam, not FK cascade). Its Pins go with the row by cascade —
// a position with no map is not information.
func (s *campaignMaps) DeleteMap(
	ctx context.Context,
	req *connect.Request[managementv1.DeleteMapRequest],
) (*connect.Response[managementv1.DeleteMapResponse], error) {
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid map id"))
	}
	c, err := s.resolveCampaign(ctx, "DeleteMap")
	if err != nil {
		return nil, err
	}

	key, err := s.store.DeleteMap(ctx, c.ID, id)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("map not found"))
	}
	if err != nil {
		slog.Default().Error("DeleteMap: store delete failed", "map_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	// A failed blob delete leaves reclaimable bytes, not a broken map — so it is a
	// warning, not a failed request the GM would retry against an already-gone row.
	if s.blobs != nil {
		if err := s.blobs.Delete(ctx, key); err != nil {
			slog.Default().Warn("DeleteMap: could not drop image blob", "key", key, "err", err)
		}
	}
	return connect.NewResponse(&managementv1.DeleteMapResponse{}), nil
}

// CreatePin pins a Node onto a Map.
func (s *campaignMaps) CreatePin(
	ctx context.Context,
	req *connect.Request[managementv1.CreatePinRequest],
) (*connect.Response[managementv1.CreatePinResponse], error) {
	m := req.Msg
	mapID, err := uuid.Parse(m.GetMapId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid map id"))
	}
	nodeID, err := uuid.Parse(m.GetNodeId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid entry id"))
	}
	if err := validateCoords(m.GetX(), m.GetY()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	c, err := s.resolveCampaign(ctx, "CreatePin")
	if err != nil {
		return nil, err
	}

	pin, err := s.store.CreatePin(ctx, storage.NewMapPin{
		MapID: mapID, CampaignID: c.ID, NodeID: nodeID,
		X: m.GetX(), Y: m.GetY(),
		LabelOverride: strings.TrimSpace(m.GetLabelOverride()),
		GMPrivate:     m.GetGmPrivate(),
	})
	switch {
	case errors.Is(err, storage.ErrConflict):
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("that entry is already pinned on this map"))
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("map or entry not found"))
	case errors.Is(err, storage.ErrInvalidPin):
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("pin position must be within the map"))
	case err != nil:
		slog.Default().Error("CreatePin: store create failed", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.CreatePinResponse{Pin: toProtoPin(pin)}), nil
}

// UpdatePin moves or relabels a Pin.
func (s *campaignMaps) UpdatePin(
	ctx context.Context,
	req *connect.Request[managementv1.UpdatePinRequest],
) (*connect.Response[managementv1.UpdatePinResponse], error) {
	m := req.Msg
	id, err := uuid.Parse(m.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid pin id"))
	}
	if err := validateCoords(m.GetX(), m.GetY()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	c, err := s.resolveCampaign(ctx, "UpdatePin")
	if err != nil {
		return nil, err
	}
	pin, err := s.store.UpdatePin(ctx, storage.MapPinUpdate{
		ID: id, CampaignID: c.ID, X: m.GetX(), Y: m.GetY(),
		LabelOverride: strings.TrimSpace(m.GetLabelOverride()),
		GMPrivate:     m.GetGmPrivate(),
	})
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("pin not found"))
	case errors.Is(err, storage.ErrInvalidPin):
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("pin position must be within the map"))
	case err != nil:
		slog.Default().Error("UpdatePin: store update failed", "pin_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&managementv1.UpdatePinResponse{Pin: toProtoPin(pin)}), nil
}

// DeletePin unpins a Node from a Map.
func (s *campaignMaps) DeletePin(
	ctx context.Context,
	req *connect.Request[managementv1.DeletePinRequest],
) (*connect.Response[managementv1.DeletePinResponse], error) {
	id, err := uuid.Parse(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid pin id"))
	}
	c, err := s.resolveCampaign(ctx, "DeletePin")
	if err != nil {
		return nil, err
	}
	switch err := s.store.DeletePin(ctx, c.ID, id); {
	case err == nil:
		return connect.NewResponse(&managementv1.DeletePinResponse{}), nil
	case errors.Is(err, storage.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("pin not found"))
	default:
		slog.Default().Error("DeletePin: store delete failed", "pin_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
}

// resolveCampaign is the shared active-campaign resolution + error mapping.
func (s *campaignMaps) resolveCampaign(ctx context.Context, op string) (storage.Campaign, error) {
	c, err := s.active.resolve(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return storage.Campaign{}, connect.NewError(connect.CodeNotFound, errors.New("no active campaign"))
		}
		slog.Default().Error(op+": get active campaign failed", "err", err)
		return storage.Campaign{}, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return c, nil
}

func (s *campaignMaps) tenantID(ctx context.Context) (uuid.UUID, bool) {
	if s.tenant == nil {
		return uuid.Nil, false
	}
	return s.tenant(ctx)
}

// putImage writes the bytes through the blob seam, translating its size cap into
// an actionable argument error rather than an opaque internal one.
func (s *campaignMaps) putImage(ctx context.Context, key, contentType string, data []byte) error {
	err := s.blobs.Put(ctx, key, contentType, bytes.NewReader(data), int64(len(data)))
	switch {
	case errors.Is(err, blob.ErrTooLarge):
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("that image is too large (max %d MiB)", blob.MaxSize>>20))
	case err != nil:
		slog.Default().Error("map image: blob put failed", "key", key, "err", err)
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return nil
}

// validateImage rejects an empty, non-image or dimensionless upload BEFORE any
// byte reaches the blob seam.
func validateImage(data []byte, contentType string, widthPx, heightPx int32) error {
	if len(data) == 0 {
		return errors.New("no image was uploaded")
	}
	if !strings.HasPrefix(contentType, "image/") {
		return fmt.Errorf("%q is not an image", contentType)
	}
	if widthPx <= 0 || heightPx <= 0 {
		return errors.New("the image's dimensions are missing")
	}
	return nil
}

// validateCoords enforces the normalized range client-side of the DB CHECK, so the
// GM gets a sentence instead of a constraint violation.
func validateCoords(x, y float64) error {
	if x < 0 || x > 1 || y < 0 || y > 1 {
		return errors.New("pin position must be within the map")
	}
	return nil
}

// optionalUUID parses an optional id field: empty means unset, anything
// unparsable is an error rather than a silently ignored value.
func optionalUUID(s, label string) (uuid.NullUUID, error) {
	if strings.TrimSpace(s) == "" {
		return uuid.NullUUID{}, nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.NullUUID{}, fmt.Errorf("invalid %s", label)
	}
	return uuid.NullUUID{UUID: id, Valid: true}, nil
}

func toProtoMap(m storage.CampaignMap) *managementv1.Map {
	pm := &managementv1.Map{
		Id:        m.ID.String(),
		Name:      m.Name,
		WidthPx:   int32(m.WidthPx),  //nolint:gosec // CHECK-constrained positive, image-sized
		HeightPx:  int32(m.HeightPx), //nolint:gosec // CHECK-constrained positive, image-sized
		GmPrivate: m.GMPrivate,
		UpdatedAt: timestamppb.New(m.UpdatedAt),
	}
	if m.ParentMapID.Valid {
		pm.ParentMapId = m.ParentMapID.UUID.String()
	}
	if m.AnchorNodeID.Valid {
		pm.AnchorNodeId = m.AnchorNodeID.UUID.String()
	}
	return pm
}

func toProtoPin(p storage.MapPin) *managementv1.Pin {
	return &managementv1.Pin{
		Id:            p.ID.String(),
		MapId:         p.MapID.String(),
		NodeId:        p.NodeID.String(),
		X:             p.X,
		Y:             p.Y,
		LabelOverride: p.LabelOverride,
		GmPrivate:     p.GMPrivate,
		NodeName:      p.NodeName,
		NodeType:      toProtoNodeType(p.NodeType),
		NodeGmPrivate: p.NodeGMPrivate,
	}
}
