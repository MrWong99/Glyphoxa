package rpc

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/imagegen"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/mapgen"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Generated maps (#541, ADR-0060): the draft-review half of the Maps surface.
//
// Both handlers here SPEND MONEY AND STORE NOTHING, which is an unusual pair and
// the reason they live in their own file rather than beside the CRUD.
//
//   - GenerateMapImage returns bytes that exist only in the GM's browser. Saving
//     is the ordinary CreateMap call; discarding is closing a dialog. "Nothing
//     hits campaign_map before the GM applies" is therefore true by construction,
//     not by discipline.
//   - SuggestMapPins returns ids of entries that ALREADY EXIST. It never invents a
//     Node and never returns a coordinate: coordinates from a language model are
//     not trustworthy, and putting them into the spatial layer would poison every
//     later feature that reads it. The GM drags each suggestion into place through
//     the existing CreatePin path.
//
// They are mutations on the wire (CSRF-guarded) despite writing nothing, exactly
// like GeneratePersona — spending the tenant's provider quota is a side effect
// whether or not a row moves.

// MapGenerator is the map-drafting engine the handler drives; *mapgen.Engine
// satisfies it. Nil leaves generation unavailable (a composition with no image
// provider), reported as CodeUnavailable rather than a panic.
type MapGenerator interface {
	Generate(ctx context.Context, campaign storage.Campaign, in mapgen.Input) (mapgen.Result, error)
}

// MapPinSuggester proposes existing entries for a Map; *mapgen.Engine satisfies
// it. Nil leaves suggestions unavailable.
type MapPinSuggester interface {
	SuggestPins(ctx context.Context, campaign storage.Campaign, anchorNodeID uuid.UUID, candidates []storage.KGNode) ([]uuid.UUID, error)
}

// maxMapPromptChars bounds the GM's prompt.
//
// It matches mapgen's own rune cap rather than the assist surface's 4000: a
// higher bound here would accept 4000 characters and silently drop half of them
// in the prompt builder, which is worse than refusing them.
const maxMapPromptChars = mapgen.MaxPromptRunes

// GenerateMapImage drafts a map image from the GM's prompt (#541).
func (s *campaignMaps) GenerateMapImage(
	ctx context.Context,
	req *connect.Request[managementv1.GenerateMapImageRequest],
) (*connect.Response[managementv1.GenerateMapImageResponse], error) {
	if s.gen == nil {
		return nil, connect.NewError(connect.CodeUnavailable,
			errors.New("map generation is unavailable in this mode"))
	}
	prompt := strings.TrimSpace(req.Msg.GetPrompt())
	if prompt == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("prompt must not be empty"))
	}
	if len(prompt) > maxMapPromptChars {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("prompt is too long"))
	}
	anchor, err := optionalUUID(req.Msg.GetAnchorNodeId(), "anchor entry id")
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	c, err := s.resolveCampaign(ctx, "GenerateMapImage")
	if err != nil {
		return nil, err
	}

	res, err := s.gen.Generate(ctx, c, mapgen.Input{Prompt: prompt, AnchorNodeID: anchor})
	if err != nil {
		return nil, mapGenerateErr("GenerateMapImage", err)
	}
	return connect.NewResponse(&managementv1.GenerateMapImageResponse{
		ImageBytes:  res.Data,
		ContentType: res.ContentType,
		Model:       res.Model,
		Prompt:      res.Prompt,
	}), nil
}

// mapGenerateErr maps a generation failure onto its Connect code.
//
// ErrImageTooLarge is InvalidArgument, matching what putImage already returns for
// an oversize UPLOAD — one failure, one code, one sentence, whichever door the
// bytes came through. It is emphatically NOT Unavailable: Unavailable invites a
// retry, and retrying re-bills the identical oversize generation.
func mapGenerateErr(op string, err error) *connect.Error {
	switch {
	case errors.Is(err, mapgen.ErrNotConfigured):
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("no image provider key is configured — save a Gemini key in Configuration"))
	case errors.Is(err, llmbuild.ErrNoPlatformKeyEntitlement):
		return connect.NewError(connect.CodeFailedPrecondition, llmbuild.ErrNoPlatformKeyEntitlement)
	case errors.Is(err, imagegen.ErrImageTooLarge):
		return connect.NewError(connect.CodeInvalidArgument,
			errors.New("the generated image came back too large to store — try a simpler prompt"))
	case errors.Is(err, storage.ErrNotFound):
		return connect.NewError(connect.CodeNotFound,
			errors.New("that entry is not in this campaign, or it is GM private"))
	default:
		slog.Default().Error(op+": map generation failed", "err", err)
		return connect.NewError(connect.CodeUnavailable,
			errors.New("map generation failed — check the image provider configuration and try again"))
	}
}

// SuggestMapPins asks which of the campaign's existing entries belong on a Map.
func (s *campaignMaps) SuggestMapPins(
	ctx context.Context,
	req *connect.Request[managementv1.SuggestMapPinsRequest],
) (*connect.Response[managementv1.SuggestMapPinsResponse], error) {
	if s.suggest == nil {
		return nil, connect.NewError(connect.CodeUnavailable,
			errors.New("pin suggestions are unavailable in this mode"))
	}
	mapID, err := uuid.Parse(req.Msg.GetMapId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid map id"))
	}
	c, err := s.resolveCampaign(ctx, "SuggestMapPins")
	if err != nil {
		return nil, err
	}

	m, err := s.store.GetMap(ctx, c.ID, mapID)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("map not found"))
	}
	if err != nil {
		slog.Default().Error("SuggestMapPins: get map failed", "map_id", mapID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if !m.AnchorNodeID.Valid {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this map has no anchor entry — set one so there is prose to read"))
	}

	// The candidate set is exactly what the Maps tab already offers: entries not
	// yet pinned here. Suggesting something already on the map is noise, and
	// suggesting something that does not exist is the auto-create this slice
	// refuses (ADR-0052).
	all, err := s.store.UnpinnedNodes(ctx, c.ID, mapID)
	if err != nil {
		slog.Default().Error("SuggestMapPins: unpinned nodes failed", "map_id", mapID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	// gm_private entries are dropped BEFORE the prompt is built. The seed prose is
	// already public-only, and sending the NAMES of the GM's secrets to the
	// provider alongside it would reopen the same channel one step to the left —
	// "The smugglers' cellar" in a candidate list is the secret. The GM can still
	// pin a private entry by hand; it is only never suggested.
	candidates := make([]storage.KGNode, 0, len(all))
	for _, n := range all {
		if n.GMPrivate {
			continue
		}
		candidates = append(candidates, n)
	}
	if len(candidates) == 0 {
		return connect.NewResponse(&managementv1.SuggestMapPinsResponse{}), nil
	}

	picked, err := s.suggest.SuggestPins(ctx, c, m.AnchorNodeID.UUID, candidates)
	if err != nil {
		return nil, mapGenerateErr("SuggestMapPins", err)
	}

	byID := make(map[uuid.UUID]storage.KGNode, len(candidates))
	for _, n := range candidates {
		byID[n.ID] = n
	}
	out := &managementv1.SuggestMapPinsResponse{}
	seen := map[uuid.UUID]bool{}
	for _, id := range picked {
		n, ok := byID[id]
		// A model that names an id outside the candidate set is ignored rather than
		// trusted — the response is a filter over entries the GM could already have
		// pinned, never a way to reach anything else.
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		out.Suggestions = append(out.Suggestions, &managementv1.SuggestedPin{
			NodeId:   n.ID.String(),
			Name:     n.Name,
			NodeType: toProtoNodeType(n.Type),
		})
	}
	return connect.NewResponse(out), nil
}
