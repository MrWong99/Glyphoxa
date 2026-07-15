package rpc

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// activeCampaignResolver is the narrow store surface the Active Campaign
// resolution reads: the LIVE Voice Session's campaign by id, the logged-in
// operator's durable /glyphoxa use selection, and the most-recently-created
// campaign as the legacy fallback. *storage.Store and the handler fakes satisfy
// it.
type activeCampaignResolver interface {
	GetCampaign(ctx context.Context, id uuid.UUID) (storage.Campaign, error)
	GetActiveCampaignForUser(ctx context.Context, discordUserID string) (storage.Campaign, error)
	GetActiveCampaign(ctx context.Context) (storage.Campaign, error)
}

// resolveActiveCampaign resolves the operator's Active Campaign with ONE policy
// every web-tier surface shares (ADR-0039, CONTEXT.md, #222). CONTEXT.md defines
// the Active Campaign as "resolved from the Voice Session binding when present,
// otherwise from the GM's profile", so the precedence is:
//
//  1. LIVE FIRST — while a Voice Session is live, its campaign wins on every
//     surface (header, roster/mute panel, campaign CRUD, KG wiki, Start/idle), so a
//     screen's reads and writes never disagree and a durable selection changed
//     mid-session cannot split one screen across two campaigns.
//  2. else the operator's durable /glyphoxa use selection (active_campaign_id).
//  3. else the most-recently-created campaign, so a fresh install that has never
//     run /glyphoxa use still resolves.
//
// live reports the live Voice Session's campaign id; it is nil when the caller has
// no session source (e.g. keyless unit tests), which skips step 1. The Manager
// enforces a single active session (ErrSessionActive), so there is at most one
// live campaign — no multi-session tie-break is needed.
//
// Two sibling surfaces implement DELIBERATE variants of this walk, not drift:
// the slash surface (internal/presence.resolveActiveCampaign) drops step 3 and
// fails instead (ADR-0009 strictness — the GM has /glyphoxa use right there),
// and the standalone voice boot (cmd/glyphoxa.resolveStandaloneCampaign) drops
// step 1 because no session source exists at boot. They also differ on a live
// session whose campaign row vanished: this surface propagates the store error,
// presence falls through to the durable selection rather than fail the command.
// Change the policy only after deciding it for all three.
func resolveActiveCampaign(ctx context.Context, live func() (uuid.UUID, bool), store activeCampaignResolver) (storage.Campaign, error) {
	if live != nil {
		if id, active := live(); active {
			return store.GetCampaign(ctx, id)
		}
	}
	if u, ok := auth.CurrentUser(ctx); ok && u.DiscordUserID != "" {
		c, err := store.GetActiveCampaignForUser(ctx, u.DiscordUserID)
		if err == nil {
			return c, nil
		}
		if !errors.Is(err, storage.ErrNotFound) {
			return storage.Campaign{}, err
		}
		// No durable selection yet — fall back to the implicit default.
	}
	return store.GetActiveCampaign(ctx)
}

// activeCampaignSource is the ONE Active-Campaign resolution every CampaignServer
// feature module shares (#445): the live Voice Session closure SetSessions wires
// plus the 3-method resolver slice. The modules hold a pointer to the same
// instance, so the header, CRUD, KG, proposal, and grant surfaces always resolve
// the same campaign — and SetSessions wiring the closure once at boot reaches all
// of them.
type activeCampaignSource struct {
	// live reports the live Voice Session's campaign id, if any. Nil until
	// SetSessions wires it (keyless deployments and most unit tests leave it nil,
	// which skips the live-first step). Set once at boot before serving, so no
	// lock is needed.
	live  func() (uuid.UUID, bool)
	store activeCampaignResolver
}

// resolve resolves the operator's Active Campaign via the one shared
// resolveActiveCampaign policy (live Voice Session → durable /glyphoxa use
// selection → most-recent fallback, #222).
func (a *activeCampaignSource) resolve(ctx context.Context) (storage.Campaign, error) {
	return resolveActiveCampaign(ctx, a.live, a.store)
}

// liveID reports the LIVE Voice Session's campaign id, nil-safe for an unwired
// source. The archive/delete live-guard reads it directly — the campaign that is
// currently voicing must not be archived or deleted, whether or not it is the
// resolved Active Campaign (#265).
func (a *activeCampaignSource) liveID() (uuid.UUID, bool) {
	if a.live == nil {
		return uuid.Nil, false
	}
	return a.live()
}
