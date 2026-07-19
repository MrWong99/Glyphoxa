package rpc

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// activeCampaignResolver is the narrow store surface the Active Campaign
// resolution reads, ALL tenant-scoped (#473): the LIVE Voice Session's campaign by
// id within the tenant, the logged-in operator's durable /glyphoxa use selection
// within the tenant, and the tenant's most-recently-created campaign as the
// fallback. Every method takes the caller's tenant so a stranger can never resolve
// another tenant's campaign onto a campaign-scoped surface (self-signup design
// §0a). *storage.Store and the handler fakes satisfy it.
type activeCampaignResolver interface {
	GetCampaignInTenant(ctx context.Context, tenantID, id uuid.UUID) (storage.Campaign, error)
	GetActiveCampaignForUserInTenant(ctx context.Context, tenantID uuid.UUID, discordUserID string) (storage.Campaign, error)
	GetActiveCampaignInTenant(ctx context.Context, tenantID uuid.UUID) (storage.Campaign, error)
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
// live reports the CALLER's Tenant live Voice Session campaign id (#488 review item
// 1): it is Tenant-scoped, reading the ctx's tenant, so with N concurrent sessions
// step 1 pivots on THIS operator's own Tenant's session — never an arbitrary session
// belonging to another Tenant (which would hard-fail every CampaignService surface
// with NotFound while another Tenant voices). nil when the caller has no session
// source (keyless unit tests), which skips step 1.
//
// Two sibling surfaces implement DELIBERATE variants of this walk, not drift:
// the slash surface (internal/presence.resolveActiveCampaign) drops step 3 and
// fails instead (ADR-0009 strictness — the GM has /glyphoxa use right there),
// and the standalone voice boot (cmd/glyphoxa.resolveStandaloneCampaign) drops
// step 1 because no session source exists at boot. They also differ on a live
// session whose campaign row vanished: this surface propagates the store error,
// presence falls through to the durable selection rather than fail the command.
// Change the policy only after deciding it for all three.
//
// Every read is TENANT-SCOPED to auth.TenantID(ctx) (#473): the live campaign, the
// durable selection, and the most-recent fallback all refuse a campaign outside the
// caller's tenant, so a stranger who pointed their durable selection at a foreign
// campaign (or whose live id is foreign) resolves to ErrNotFound rather than
// pivoting every campaign-scoped surface onto the victim tenant (self-signup design
// §0a). The RPC interceptor stack always injects a tenant; a request with none
// resolves against the nil tenant and matches nothing (fail closed).
func resolveActiveCampaign(ctx context.Context, live func(context.Context) (uuid.UUID, bool), store activeCampaignResolver) (storage.Campaign, error) {
	tenantID, _ := auth.TenantID(ctx)
	if live != nil {
		if id, active := live(ctx); active {
			return store.GetCampaignInTenant(ctx, tenantID, id)
		}
	}
	if u, ok := auth.CurrentUser(ctx); ok && u.DiscordUserID != "" {
		c, err := store.GetActiveCampaignForUserInTenant(ctx, tenantID, u.DiscordUserID)
		if err == nil {
			return c, nil
		}
		if !errors.Is(err, storage.ErrNotFound) {
			return storage.Campaign{}, err
		}
		// No durable selection yet — fall back to the implicit default.
	}
	return store.GetActiveCampaignInTenant(ctx, tenantID)
}

// activeCampaignSource is the ONE Active-Campaign resolution every CampaignServer
// feature module shares (#445): the live Voice Session closure SetSessions wires
// plus the 3-method resolver slice. The modules hold a pointer to the same
// instance, so the header, CRUD, KG, proposal, and grant surfaces always resolve
// the same campaign — and SetSessions wiring the closure once at boot reaches all
// of them.
type activeCampaignSource struct {
	// live reports the CALLER's Tenant live Voice Session campaign id, if any
	// (Tenant-scoped, #488). Nil until SetSessions wires it (keyless deployments and
	// most unit tests leave it nil, which skips the live-first step). Set once at boot
	// before serving, so no lock is needed.
	live func(context.Context) (uuid.UUID, bool)
	// campaignLive reports whether campaignID is live in ANY session across all
	// Tenants (#488 review item 2): the archive/delete guard needs the process-wide
	// truth, not one Tenant's, so a GM can never archive/DELETE a campaign live in
	// another Tenant's session. Nil until SetSessions wires it (unwired = never live).
	campaignLive func(campaignID uuid.UUID) bool
	store        activeCampaignResolver
}

// resolve resolves the operator's Active Campaign via the one shared
// resolveActiveCampaign policy (live Voice Session → durable /glyphoxa use
// selection → most-recent fallback, #222).
func (a *activeCampaignSource) resolve(ctx context.Context) (storage.Campaign, error) {
	return resolveActiveCampaign(ctx, a.live, a.store)
}

// isCampaignLive reports whether campaignID is currently voicing in ANY session
// (#488 review item 2), nil-safe for an unwired source. The archive/delete
// live-guard reads it — the campaign that is currently voicing must not be archived
// or deleted, whether or not it is the resolved Active Campaign and regardless of
// which Tenant is running it (#265).
func (a *activeCampaignSource) isCampaignLive(campaignID uuid.UUID) bool {
	return a.campaignLive != nil && a.campaignLive(campaignID)
}
