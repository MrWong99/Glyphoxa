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
