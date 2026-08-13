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

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/portraitgen"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Node portraits (#590): the third consumer of the image seam, on the Maps
// draft-review lifecycle. GenerateNodePortrait SPENDS MONEY AND STORES NOTHING
// (the campaign_map_generate.go pair's posture — a mutation on the wire because
// spending the tenant's provider quota is a side effect whether or not a row
// moves); SetNodePortrait is the one door bytes enter through, generated and
// uploaded alike, so "nothing hits the row before the GM applies" is true by
// construction.
type nodePortraits struct {
	store  nodePortraitStore
	blobs  blob.Store
	active *activeCampaignSource
	// tenant resolves the request's Tenant, which the blob key is prefixed with
	// (ADR-0048 makes the tenant prefix mandatory).
	tenant func(ctx context.Context) (uuid.UUID, bool)
	// gen backs the generated-portrait draft flow. Nil in a composition with no
	// image provider, which the handler reports rather than panics on.
	gen PortraitGenerator
}

// nodePortraitStore is the narrow portrait surface the module needs;
// *storage.Store satisfies it. Every method is campaign-scoped (#342).
type nodePortraitStore interface {
	SetNodePortrait(ctx context.Context, campaignID, nodeID uuid.UUID, blobKey string) (storage.KGNode, string, error)
}

// PortraitGenerator is the portrait-drafting engine the handler drives;
// *portraitgen.Engine satisfies it. Nil leaves generation unavailable (a
// composition with no image provider), reported as CodeUnavailable rather than
// a panic.
type PortraitGenerator interface {
	Generate(ctx context.Context, campaign storage.Campaign, in portraitgen.Input) (portraitgen.Result, error)
}

// maxPortraitPromptChars bounds the GM's extra direction, matching portraitgen's
// own rune cap: a higher bound here would accept characters the prompt builder
// silently drops, which is worse than refusing them.
const maxPortraitPromptChars = portraitgen.MaxPromptRunes

// GenerateNodePortrait drafts a portrait from an entry's public prose (#590).
func (s *nodePortraits) GenerateNodePortrait(
	ctx context.Context,
	req *connect.Request[managementv1.GenerateNodePortraitRequest],
) (*connect.Response[managementv1.GenerateNodePortraitResponse], error) {
	if s.gen == nil {
		return nil, connect.NewError(connect.CodeUnavailable,
			errors.New("portrait generation is unavailable in this mode"))
	}
	id, err := uuid.Parse(req.Msg.GetNodeId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid entry id"))
	}
	// The prompt is OPTIONAL here (the entry's own prose is the prompt), unlike
	// the map flow where the GM's words are the only material.
	prompt := strings.TrimSpace(req.Msg.GetPrompt())
	if len(prompt) > maxPortraitPromptChars {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("prompt is too long"))
	}

	c, err := s.resolveCampaign(ctx, "GenerateNodePortrait")
	if err != nil {
		return nil, err
	}

	res, err := s.gen.Generate(ctx, c, portraitgen.Input{NodeID: id, Prompt: prompt})
	if err != nil {
		return nil, mapGenerateErr("GenerateNodePortrait", "portrait generation", err)
	}
	return connect.NewResponse(&managementv1.GenerateNodePortraitResponse{
		ImageBytes:  res.Data,
		ContentType: res.ContentType,
		Model:       res.Model,
		Prompt:      res.Prompt,
	}), nil
}

// SetNodePortrait applies portrait bytes — generated or uploaded — to an entry.
//
// Blob FIRST, row second (the CreateMap rule): a row referencing missing bytes
// renders as a broken portrait with no way to fix it, whereas a blob with no
// row is invisible and dropped inline below. A NEW key per write, not an
// overwrite, so an in-flight portrait request never sees a torn blob and
// updated_at stays a sound cache validator (the ReplaceMapImage ritual).
func (s *nodePortraits) SetNodePortrait(
	ctx context.Context,
	req *connect.Request[managementv1.SetNodePortraitRequest],
) (*connect.Response[managementv1.SetNodePortraitResponse], error) {
	m := req.Msg
	id, err := uuid.Parse(m.GetNodeId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid entry id"))
	}
	if len(m.GetImageBytes()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("no image was provided"))
	}
	if !strings.HasPrefix(m.GetContentType(), "image/") {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("%q is not an image", m.GetContentType()))
	}
	if s.blobs == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("portraits are unavailable in this mode"))
	}
	c, err := s.resolveCampaign(ctx, "SetNodePortrait")
	if err != nil {
		return nil, err
	}
	tenantID, ok := s.tenantID(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("no tenant"))
	}

	key, err := blob.Key(tenantID, "node", uuid.New(), "portrait")
	if err != nil {
		slog.Default().Error("SetNodePortrait: blob key", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	if err := s.putPortrait(ctx, key, m.GetContentType(), m.GetImageBytes()); err != nil {
		return nil, err
	}

	updated, oldKey, err := s.store.SetNodePortrait(ctx, c.ID, id, key)
	if err != nil {
		// Drop the orphaned bytes here and now — the campaign sweep walks kg_node
		// rows, and this write repointed none — so this best-effort delete IS the
		// cleanup. Best-effort because the row failure is what the caller must
		// hear about.
		if derr := s.blobs.Delete(ctx, key); derr != nil {
			slog.Default().Warn("SetNodePortrait: could not drop orphaned blob", "key", key, "err", derr)
		}
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
		}
		slog.Default().Error("SetNodePortrait: store update failed", "node_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	// The row now points at the new bytes, so the old ones are unreachable. An
	// entry that had NO portrait supersedes nothing, and deleting "" is
	// ErrInvalidKey — a warning about leaked bytes that never existed.
	if oldKey != "" {
		if derr := s.blobs.Delete(ctx, oldKey); derr != nil {
			slog.Default().Warn("SetNodePortrait: could not drop superseded blob", "key", oldKey, "err", derr)
		}
	}
	return connect.NewResponse(&managementv1.SetNodePortraitResponse{Node: toProtoNode(updated)}), nil
}

// ClearNodePortrait removes an entry's portrait and releases its bytes.
func (s *nodePortraits) ClearNodePortrait(
	ctx context.Context,
	req *connect.Request[managementv1.ClearNodePortraitRequest],
) (*connect.Response[managementv1.ClearNodePortraitResponse], error) {
	id, err := uuid.Parse(req.Msg.GetNodeId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid entry id"))
	}
	c, err := s.resolveCampaign(ctx, "ClearNodePortrait")
	if err != nil {
		return nil, err
	}

	updated, oldKey, err := s.store.SetNodePortrait(ctx, c.ID, id, "")
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
		}
		slog.Default().Error("ClearNodePortrait: store update failed", "node_id", id, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	// A failed blob delete leaves reclaimable bytes, not a broken entry — a
	// warning, not a failed request the GM would retry against an already-clear
	// row. Clearing an entry that had no portrait is an ordinary no-op.
	if s.blobs != nil && oldKey != "" {
		if derr := s.blobs.Delete(ctx, oldKey); derr != nil {
			slog.Default().Warn("ClearNodePortrait: could not drop portrait blob", "key", oldKey, "err", derr)
		}
	}
	return connect.NewResponse(&managementv1.ClearNodePortraitResponse{Node: toProtoNode(updated)}), nil
}

// resolveCampaign is the shared active-campaign resolution + error mapping.
func (s *nodePortraits) resolveCampaign(ctx context.Context, op string) (storage.Campaign, error) {
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

func (s *nodePortraits) tenantID(ctx context.Context) (uuid.UUID, bool) {
	if s.tenant == nil {
		return uuid.Nil, false
	}
	return s.tenant(ctx)
}

// putPortrait writes the bytes through the blob seam, translating its size cap
// into an actionable argument error rather than an opaque internal one.
func (s *nodePortraits) putPortrait(ctx context.Context, key, contentType string, data []byte) error {
	err := s.blobs.Put(ctx, key, contentType, bytes.NewReader(data), int64(len(data)))
	switch {
	case errors.Is(err, blob.ErrTooLarge):
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("that image is too large (max %d MiB)", blob.MaxSize>>20))
	case err != nil:
		slog.Default().Error("node portrait: blob put failed", "key", key, "err", err)
		return connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return nil
}
