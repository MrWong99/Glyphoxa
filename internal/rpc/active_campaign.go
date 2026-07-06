package rpc

import (
	"context"
	"errors"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// activeCampaignResolver is the narrow store surface the profile-first Active
// Campaign resolution reads: the logged-in operator's durable /glyphoxa use
// selection, and the most-recently-created campaign as the legacy fallback.
// Both *storage.Store (production) and the handler fakes satisfy it.
type activeCampaignResolver interface {
	GetActiveCampaignForUser(ctx context.Context, discordUserID string) (storage.Campaign, error)
	GetActiveCampaign(ctx context.Context) (storage.Campaign, error)
}

// resolveActiveCampaign resolves the operator's Active Campaign profile-first
// (ADR-0039, #216/#219/#220/#222): the logged-in operator's durable /glyphoxa
// use selection (active_campaign_id) when set, else the most-recently-created
// campaign as the legacy fallback so a fresh install that has never run
// /glyphoxa use still resolves. This is the ONE web-tier resolution the Session
// Start button, the idle summary, the header, campaign CRUD, and the KG wiki all
// share — extracted once so a durable selection of an older campaign scopes
// every surface, not just the ones already fixed (#216/#219/#220). The slash
// surface is strict (no fallback) because it has the /use affordance; the web
// has no such hint, so it keeps the legacy default.
func resolveActiveCampaign(ctx context.Context, store activeCampaignResolver) (storage.Campaign, error) {
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
