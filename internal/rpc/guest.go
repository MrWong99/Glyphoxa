package rpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/search"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// Campaign Guest handler hardening (ADR-0065). The policy layer's guest gate
// (auth/guest_gate.go) decides whether a guest may call a procedure AT ALL;
// the helpers here decide what a handler a guest did reach may hand back. A
// guest's request carries the HOST Tenant in its context, so "tenant-scoped"
// is not enough for them: every guest-reachable handler scopes to the guest
// Campaign and withholds what the guest's rights do not cover. For a Tenant's
// own operator every helper is a no-op.

// privateNodeReader lists a Campaign's gm_private Node ids;
// *storage.Store satisfies it.
type privateNodeReader interface {
	PrivateNodeIDs(ctx context.Context, campaignID uuid.UUID) (map[uuid.UUID]struct{}, error)
}

// seesSecrets reports whether the caller may see gm_private material: every
// GM, and a Campaign Guest holding wiki.secrets. It is the one test behind
// every "withheld without wiki.secrets" rule, including the private Maps and
// Pins a guest with only maps.read must not see.
func seesSecrets(ctx context.Context) bool {
	return !auth.GuestLacks(ctx, auth.RightWikiSecrets)
}

// errWithheldUnavailable is the fail-closed answer when a guest without
// wiki.secrets reaches a handler whose composition has no way to learn which
// Nodes are private: refuse rather than serve unfiltered.
var errWithheldUnavailable = errors.New("rpc: private-entry filter is not wired; refusing to serve a guest unfiltered")

// withheldNodes returns the ids of the Nodes the caller must learn nothing
// about: nil (withhold nothing) when the caller sees secrets, else the
// Campaign's gm_private Nodes. Handlers use it to drop whatever merely points
// at such a Node — an Edge, a tag, a board entry, a Pin.
func (a *activeCampaignSource) withheldNodes(ctx context.Context, campaignID uuid.UUID) (map[uuid.UUID]struct{}, error) {
	if seesSecrets(ctx) {
		return nil, nil
	}
	if a == nil || a.secrets == nil {
		return nil, errWithheldUnavailable
	}
	return a.secrets.PrivateNodeIDs(ctx, campaignID)
}

// withheldFor is [activeCampaignSource.withheldNodes] mapped onto the connect
// error a handler returns: a logged CodeInternal.
func (a *activeCampaignSource) withheldFor(ctx context.Context, op string, campaignID uuid.UUID) (map[uuid.UUID]struct{}, error) {
	withheld, err := a.withheldNodes(ctx, campaignID)
	if err != nil {
		a.logger().Error(op+": load private entries failed", "campaign_id", campaignID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return withheld, nil
}

// isWithheld reports whether id is in the withheld set (nil set = nothing is).
func isWithheld(withheld map[uuid.UUID]struct{}, id uuid.UUID) bool {
	_, ok := withheld[id]
	return ok
}

// redactNodes drops what a caller without wiki.secrets must not read from a
// Node list: gm_private Nodes entirely, and the private Aspects of the Nodes
// that remain. It needs no extra read — each Node carries its own flags.
func redactNodes(ctx context.Context, nodes []storage.KGNode) []storage.KGNode {
	if seesSecrets(ctx) {
		return nodes
	}
	out := make([]storage.KGNode, 0, len(nodes))
	for _, n := range nodes {
		if n.GMPrivate {
			continue
		}
		if len(n.Aspects) > 0 {
			public := make([]storage.KGNodeAspect, 0, len(n.Aspects))
			for _, a := range n.Aspects {
				if !a.GMPrivate {
					public = append(public, a)
				}
			}
			n.Aspects = public
		}
		out = append(out, n)
	}
	return out
}

// guestSearchAccess maps the caller onto the retrieval engine's capability
// tier (search.Access). The tier is derived from the request's authorized
// principal, never from wire input. A GM is the operator tier. A guest gets
// the narrowest tier that covers what is being searched: transcript text only
// with transcripts.read, promoted Highlights only with highlights.read, and
// otherwise the tier that fails closed. None of the guest tiers ever reads
// gm_private Node material.
func guestSearchAccess(ctx context.Context) search.Access {
	g, ok := auth.Guest(ctx)
	switch {
	case !ok:
		return search.AccessOperator
	case g.Rights.Has(auth.RightTranscriptsRead):
		return search.AccessCampaignTranscripts
	case g.Rights.Has(auth.RightHighlightsRead):
		return search.AccessCampaignHighlights
	default:
		return search.AccessOwnCharacter
	}
}

// guestCampaignMismatch reports whether a client-supplied campaign id is
// anything but the guest's own Campaign. A handler that takes a campaign id
// from the request answers NotFound on a mismatch — another Campaign of the
// host Tenant must look exactly like one that does not exist.
func guestCampaignMismatch(ctx context.Context, campaignID uuid.UUID) bool {
	g, ok := auth.Guest(ctx)
	return ok && g.CampaignID != campaignID
}
